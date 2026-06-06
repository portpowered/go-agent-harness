package participants

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// ToolRunner executes tool calls asynchronously as an active participant.
// It reads ToolBatchRequest from its inbox, executes all calls in parallel,
// and writes results to DeltaOutbox as a structured delta sequence so the
// ordering layer (GlobalOrdering) can assemble the tool result Messages.
//
// Streaming: tool result content is written to DeltaOutbox as a sequence of
// MESSAGE.START → (TEXT.START / TEXT.DELTA / TEXT.END per result) → MESSAGE.END
// deltas, matching the delta protocol used by ModelRunner. GlobalOrdering
// assembles these into ToolOutputMessage entries on MESSAGE.END.
//
// Errors: a single ERROR delta is written to DeltaOutbox on execution failure
// so the ordering layer can return the error via the normal error path.
type ToolRunner struct {
	executor    messages.ToolExecutor
	Inbox       *messages.TypedBuffer[messages.ToolBatchRequest]
	DeltaOutbox *messages.TypedBuffer[messages.StreamMessage]

	currentPassID int // LoopPassID from the current ToolBatchRequest

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle
}

func NewToolRunner(executor messages.ToolExecutor, bufferCapacity int) *ToolRunner {
	return &ToolRunner{
		executor:    executor,
		Inbox:       messages.NewTypedBuffer[messages.ToolBatchRequest](bufferCapacity),
		DeltaOutbox: messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
	}
}

func (r *ToolRunner) Run(ctx context.Context) error {
	for {
		err := r.Tick(ctx)
		if err != nil {
			return err
		}
	}
}

// CancelCurrentExecution cancels the per-execution context for the tool batch
// that is currently in flight. The runner's outer goroutine continues running and
// will block on the next Inbox.ReadBlocking call; only the active batch is failed.
// Safe to call from any goroutine; no-op when no batch is in flight.
func (r *ToolRunner) CancelCurrentExecution() {
	r.execMu.Lock()
	defer r.execMu.Unlock()
	if r.execCancel != nil {
		r.execCancel()
	}
}

func (r *ToolRunner) Tick(ctx context.Context) error {
	req, ok := r.Inbox.ReadBlocking(ctx.Done())
	if !ok {
		return ctx.Err()
	}

	execCtx, execCancel := context.WithCancel(ctx)
	r.execMu.Lock()
	r.execCancel = execCancel
	r.execMu.Unlock()
	r.currentPassID = req.LoopPassID

	results, err := r.executeBatch(execCtx, req.Calls)

	r.execMu.Lock()
	r.execCancel = nil
	r.execMu.Unlock()
	execCancel()

	if err != nil {
		// Signal the error through the delta stream so the ordering layer can
		// return it via the normal error path (consumeToolDelta).
		errStreamID := mustStreamID("tool-error")
		r.DeltaOutbox.Write(ctx, messages.StreamMessage{
			Type:               messages.StreamTypeError,
			Value:              messages.NewErrorValue(err.Error()),
			ActorID:            messages.Tool,
			ActorStreamID:      errStreamID,
			ActorProvidedIndex: 0,
			ActorProvidedID:    fmt.Sprintf("tool-%s-0", errStreamID),
			LoopPassID:         r.currentPassID,
		})
		return nil
	}
	r.emitResultDeltas(ctx, r.currentPassID, results)
	return nil
}

// emitResultDeltas writes TEXT.DELTA for each tool result's content and MESSAGE.END.
// Each delta is tagged with runner-specific ordering (see ORDERING.md). The message envelope
// (MESSAGE.START, MESSAGE.END) uses one stream; each tool call's TEXT.START/DELTA/END uses
// its own stream so parallel tool calls have separate streams.
func (r *ToolRunner) emitResultDeltas(ctx context.Context, loopPassID int, results []messages.ToolCallResponse) {
	messageStreamID := mustStreamID("tool-msg")
	writeInStream := func(streamID string, idx int, sm messages.StreamMessage) {
		sm.ActorStreamID = streamID
		sm.ActorProvidedIndex = idx
		sm.ActorProvidedID = fmt.Sprintf("tool-%s-%d", streamID, idx)
		sm.ActorID = messages.Tool
		sm.LoopPassID = loopPassID
		r.DeltaOutbox.Write(ctx, sm)
	}

	writeInStream(messageStreamID, 0, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleTool,
		Value: messages.NewMessageStartValue(),
	})

	for _, result := range results {
		toolStreamID := mustStreamID("tool-call")
		idx := 0
		writeToolDelta := func(sm messages.StreamMessage) {
			writeInStream(toolStreamID, idx, sm)
			idx++
		}
		if len(result.ContentParts) > 0 {
			// Emit each content part as the appropriate delta sequence.
			for _, part := range result.ContentParts {
				switch p := part.(type) {
				case messages.TextPart:
					if p.Text != "" {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, Value: messages.NewTextStartValue(), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue(p.Text), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, Value: messages.NewTextEndValue(), ToolCallId: result.ToolCallID})
					}
				case messages.ImagePart:
					if len(p.Bytes) > 0 {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageStart, Role: messages.RoleTool, Value: messages.NewImageStartValue(p.MediaType), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageDelta, Role: messages.RoleTool, Value: messages.NewImageDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageEnd, Role: messages.RoleTool, Value: messages.NewImageEndValue(), ToolCallId: result.ToolCallID})
					}
				case messages.AudioPart:
					if len(p.Bytes) > 0 {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleTool, Value: messages.NewAudioStartValue(), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleTool, Value: messages.NewAudioDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleTool, Value: messages.NewAudioEndValue(), ToolCallId: result.ToolCallID})
					}
				case messages.VideoPart:
					if len(p.Bytes) > 0 {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoStart, Role: messages.RoleTool, Value: messages.NewVideoStartValue(p.MediaType), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoDelta, Role: messages.RoleTool, Value: messages.NewVideoDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoEnd, Role: messages.RoleTool, Value: messages.NewVideoEndValue(), ToolCallId: result.ToolCallID})
					}
				case messages.FilePart:
					if len(p.Bytes) > 0 {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileStart, Role: messages.RoleTool, Value: messages.NewFileStartValue(p.MediaType, p.Name), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileDelta, Role: messages.RoleTool, Value: messages.NewFileDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileEnd, Role: messages.RoleTool, Value: messages.NewFileEndValue(), ToolCallId: result.ToolCallID})
					}
				}
			}

		} else if text := result.Content; text != "" {
			// Fallback: emit text from the flat Content field.
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, Value: messages.NewTextStartValue(), ToolCallId: result.ToolCallID})
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue(text), ToolCallId: result.ToolCallID})
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, Value: messages.NewTextEndValue(), ToolCallId: result.ToolCallID})
		}
	}

	writeInStream(messageStreamID, 1, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0}),
	})
}

func mustStreamID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// executeBatch runs all tool calls in parallel and collects results.
// Results are returned in the same order as the input calls.
func (r *ToolRunner) executeBatch(ctx context.Context, calls []messages.ToolCall) ([]messages.ToolCallResponse, error) {
	results := make([]messages.ToolCallResponse, len(calls))
	errs := make([]error, len(calls))

	var wg sync.WaitGroup
	wg.Add(len(calls))

	for i, tc := range calls {
		go func(idx int, call messages.ToolCall) {
			defer wg.Done()
			resp, err := r.executor.Execute(ctx, call)
			if err != nil {
				errs[idx] = fmt.Errorf("tool %q failed: %w", call.Name, err)
				return
			}
			results[idx] = resp
		}(i, tc)
	}

	wg.Wait()

	// Return all errors aggregated so callers see every failure, not just the first.
	if joined := errors.Join(errs...); joined != nil {
		return nil, joined
	}

	return results, nil
}
