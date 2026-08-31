package participants

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

	// admittedCallIDs is scoped to this runner, which is scoped to one agent
	// loop/session. A provider may surface the same function call again after a
	// delayed or lost result; once admitted, that call ID must never reach the
	// executor a second time.
	admissionMu     sync.Mutex
	admittedCallIDs map[string]struct{}

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle

	acknowledgementThreshold time.Duration
	isLongRunningTool        func(string) bool
	sendAcknowledgement      func(context.Context, []messages.ToolCall)
}

func NewToolRunner(executor messages.ToolExecutor, bufferCapacity int) *ToolRunner {
	return &ToolRunner{
		executor:        executor,
		admittedCallIDs: make(map[string]struct{}),
		Inbox:           messages.NewTypedBuffer[messages.ToolBatchRequest](bufferCapacity),
		DeltaOutbox:     messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
	}
}

// ConfigureAcknowledgement enables a one-shot callback when at least one
// admitted long-running call remains pending after the configured threshold.
// It is configured before Run starts and is intentionally independent from the
// tool executor's timeout policy.
func (r *ToolRunner) ConfigureAcknowledgement(threshold time.Duration, isLongRunning func(string) bool, send func(context.Context, []messages.ToolCall)) {
	r.acknowledgementThreshold = threshold
	r.isLongRunningTool = isLongRunning
	r.sendAcknowledgement = send
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
	if len(results) > 0 {
		r.emitResultDeltas(ctx, r.currentPassID, results)
	}
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
		contentEmitted := false
		writeToolDelta := func(sm messages.StreamMessage) {
			writeInStream(toolStreamID, idx, sm)
			idx++
		}
		if len(result.ContentParts) > 0 {
			// Emit each content part as the appropriate delta sequence.
			for _, part := range result.ContentParts {
				switch p := part.(type) {
				case messages.TextPart:
					// Preserve the text boundaries even for an empty result. The
					// ToolCallId on TEXT.START is the stream-only correlation
					// mechanism used by the ordering layer and must survive a
					// successful empty tool response.
					contentEmitted = true
					writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, Value: messages.NewTextStartValue(), ToolCallId: result.ToolCallID})
					if p.Text != "" {
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue(p.Text), ToolCallId: result.ToolCallID})
					}
					writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, Value: messages.NewTextEndValue(), ToolCallId: result.ToolCallID})
				case messages.ImagePart:
					if len(p.Bytes) > 0 {
						contentEmitted = true
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageStart, Role: messages.RoleTool, Value: messages.NewImageStartValue(p.MediaType), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageDelta, Role: messages.RoleTool, Value: messages.NewImageDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeImageEnd, Role: messages.RoleTool, Value: messages.NewImageEndValue(), ToolCallId: result.ToolCallID})
						contentEmitted = true
					}
				case messages.AudioPart:
					if len(p.Bytes) > 0 {
						contentEmitted = true
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleTool, Value: messages.NewAudioStartValue(), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleTool, Value: messages.NewAudioDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleTool, Value: messages.NewAudioEndValue(), ToolCallId: result.ToolCallID})
						contentEmitted = true
					}
				case messages.VideoPart:
					if len(p.Bytes) > 0 {
						contentEmitted = true
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoStart, Role: messages.RoleTool, Value: messages.NewVideoStartValue(p.MediaType), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoDelta, Role: messages.RoleTool, Value: messages.NewVideoDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeVideoEnd, Role: messages.RoleTool, Value: messages.NewVideoEndValue(), ToolCallId: result.ToolCallID})
						contentEmitted = true
					}
				case messages.FilePart:
					if len(p.Bytes) > 0 {
						contentEmitted = true
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileStart, Role: messages.RoleTool, Value: messages.NewFileStartValue(p.MediaType, p.Name), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileDelta, Role: messages.RoleTool, Value: messages.NewFileDeltaValue(p.Bytes), ToolCallId: result.ToolCallID})
						writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeFileEnd, Role: messages.RoleTool, Value: messages.NewFileEndValue(), ToolCallId: result.ToolCallID})
						contentEmitted = true
					}
				}
			}

		} else if text := result.Content; text != "" {
			// Fallback: emit text from the flat Content field.
			contentEmitted = true
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, Value: messages.NewTextStartValue(), ToolCallId: result.ToolCallID})
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, Value: messages.NewTextDeltaValue(text), ToolCallId: result.ToolCallID})
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, Value: messages.NewTextEndValue(), ToolCallId: result.ToolCallID})
			contentEmitted = true
		}
		if !contentEmitted {
			// A successful empty result still needs one reconstructible content
			// boundary. Without it, the tool message loses its call ID and the
			// provider cannot receive the result or request its continuation.
			writeToolDelta(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, Value: messages.NewTextStartValue(), ToolCallId: result.ToolCallID})
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
// Results are returned in the same order as the input calls. A configured
// acknowledgement timer observes the same result channel, so calls that finish
// before the threshold never cause an acknowledgement and a batch emits at
// most one acknowledgement request.
func (r *ToolRunner) executeBatch(ctx context.Context, calls []messages.ToolCall) ([]messages.ToolCallResponse, error) {
	calls = r.admitCalls(calls)
	if len(calls) == 0 {
		return nil, nil
	}

	results := make([]messages.ToolCallResponse, len(calls))
	errs := make([]error, len(calls))
	type executionResult struct {
		index    int
		response messages.ToolCallResponse
		err      error
	}
	resultCh := make(chan executionResult, len(calls))
	pendingLongRunning := make(map[int]messages.ToolCall)
	for i, call := range calls {
		if r.acknowledgementThreshold > 0 && r.isLongRunningTool != nil && r.isLongRunningTool(call.Name) {
			pendingLongRunning[i] = call
		}
	}

	for i, tc := range calls {
		go func(idx int, call messages.ToolCall) {
			resp, err := r.executor.Execute(ctx, call)
			resultCh <- executionResult{index: idx, response: resp, err: err}
		}(i, tc)
	}

	var acknowledgementTimer *time.Timer
	var acknowledgementCh <-chan time.Time
	if len(pendingLongRunning) > 0 {
		acknowledgementTimer = time.NewTimer(r.acknowledgementThreshold)
		acknowledgementCh = acknowledgementTimer.C
		defer acknowledgementTimer.Stop()
	}
	ctxDone := ctx.Done()
	acknowledgementSent := false
	completed := 0
	for completed < len(calls) {
		select {
		case result := <-resultCh:
			if result.err != nil {
				errs[result.index] = fmt.Errorf("tool %q failed: %w", calls[result.index].Name, result.err)
			} else {
				results[result.index] = result.response
			}
			delete(pendingLongRunning, result.index)
			completed++
		case <-acknowledgementCh:
			if !acknowledgementSent && len(pendingLongRunning) > 0 && ctx.Err() == nil {
				acknowledgementSent = true
				if r.sendAcknowledgement != nil {
					pending := make([]messages.ToolCall, 0, len(pendingLongRunning))
					for i := range calls {
						if call, ok := pendingLongRunning[i]; ok {
							pending = append(pending, call)
						}
					}
					r.sendAcknowledgement(ctx, pending)
				}
			}
			acknowledgementCh = nil
		case <-ctxDone:
			// Keep collecting worker outcomes so the existing batch error
			// semantics remain intact, but never send an acknowledgement after
			// cancellation.
			ctxDone = nil
			if acknowledgementTimer != nil {
				acknowledgementTimer.Stop()
			}
			acknowledgementCh = nil
		}
	}

	// Return all errors aggregated so callers see every failure, not just the first.
	if joined := errors.Join(errs...); joined != nil {
		return nil, joined
	}

	return results, nil
}

// admitCalls records provider call IDs before execution starts and removes
// repeated IDs from the batch. The map belongs to the ToolRunner rather than
// the provider adapter so every execution entry point shares one session-scoped
// exactly-once boundary. Empty IDs remain executable for compatibility, but
// cannot participate in correlation or duplicate suppression.
func (r *ToolRunner) admitCalls(calls []messages.ToolCall) []messages.ToolCall {
	if r == nil || len(calls) == 0 {
		return calls
	}
	r.admissionMu.Lock()
	defer r.admissionMu.Unlock()
	if r.admittedCallIDs == nil {
		r.admittedCallIDs = make(map[string]struct{})
	}

	admitted := make([]messages.ToolCall, 0, len(calls))
	for _, call := range calls {
		if call.ID != "" {
			if _, seen := r.admittedCallIDs[call.ID]; seen {
				continue
			}
			r.admittedCallIDs[call.ID] = struct{}{}
		}
		admitted = append(admitted, call)
	}
	return admitted
}
