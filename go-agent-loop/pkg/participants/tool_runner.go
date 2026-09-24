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
		// Signal the expected tool failure through the delta stream. Tool
		// execution failures are user-visible diagnostics for one-shot and
		// turn-taking loops; the ordering layer emits LOOP.END after forwarding
		// this nonterminal ERROR instead of converting the diagnostic into a
		// provider/engine failure.
		errStreamID := mustStreamID("tool-error")
		r.DeltaOutbox.Write(ctx, messages.StreamMessage{
			Type:               messages.StreamTypeError,
			Value:              messages.NewNonTerminalErrorValue(err.Error(), "tool_execution"),
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
	envelope := toolStreamWriter{runner: r, ctx: ctx, loopPassID: loopPassID, streamID: mustStreamID("tool-msg")}
	envelope.write(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, Value: messages.NewMessageStartValue()})
	for _, result := range results {
		call := toolStreamWriter{runner: r, ctx: ctx, loopPassID: loopPassID, streamID: mustStreamID("tool-call"), toolCallID: result.ToolCallID}
		call.emitResult(result)
	}
	usage := messages.TokenUsage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0}
	envelope.write(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(usage)})
}

// toolStreamWriter tags deltas for one tool stream with sequential actor
// indexes. The envelope stream and each tool call stream own separate writers.
type toolStreamWriter struct {
	runner     *ToolRunner
	ctx        context.Context
	loopPassID int
	streamID   string
	toolCallID string
	idx        int
}

func (w *toolStreamWriter) write(sm messages.StreamMessage) {
	sm.ActorStreamID = w.streamID
	sm.ActorProvidedIndex = w.idx
	sm.ActorProvidedID = fmt.Sprintf("tool-%s-%d", w.streamID, w.idx)
	sm.ActorID = messages.Tool
	sm.LoopPassID = w.loopPassID
	w.idx++
	w.runner.DeltaOutbox.Write(w.ctx, sm)
}

func (w *toolStreamWriter) writeContent(streamType messages.StreamMessageType, value messages.StreamMessageValue) {
	w.write(messages.StreamMessage{Type: streamType, Role: messages.RoleTool, Value: value, ToolCallId: w.toolCallID})
}

// emitResult writes one tool result's content boundaries. Structured parts
// take precedence over the flat Content fallback.
func (w *toolStreamWriter) emitResult(result messages.ToolCallResponse) {
	contentEmitted := false
	if len(result.ContentParts) > 0 {
		for _, part := range result.ContentParts {
			if w.emitContentPart(part) {
				contentEmitted = true
			}
		}
	} else if text := result.Content; text != "" {
		// Fallback: emit text from the flat Content field.
		w.emitText(text)
		contentEmitted = true
	}
	if !contentEmitted {
		// A successful empty result still needs one reconstructible content
		// boundary. Without it, the tool message loses its call ID and the
		// provider cannot receive the result or request its continuation.
		w.writeContent(messages.StreamTypeTextStart, messages.NewTextStartValue())
		w.writeContent(messages.StreamTypeTextEnd, messages.NewTextEndValue())
	}
}

// emitText preserves the text boundaries even for an empty result. The
// ToolCallId on TEXT.START is the stream-only correlation mechanism used by
// the ordering layer and must survive a successful empty tool response.
func (w *toolStreamWriter) emitText(text string) {
	w.writeContent(messages.StreamTypeTextStart, messages.NewTextStartValue())
	if text != "" {
		w.writeContent(messages.StreamTypeTextDelta, messages.NewTextDeltaValue(text))
	}
	w.writeContent(messages.StreamTypeTextEnd, messages.NewTextEndValue())
}

// binaryBoundary is one START/DELTA/END triple for a binary content part.
type binaryBoundary struct {
	startType, deltaType, endType messages.StreamMessageType
	start, delta, end             messages.StreamMessageValue
}

// emitContentPart writes the delta sequence for one content part and reports
// whether any content boundary was emitted. Empty binary parts are skipped.
func (w *toolStreamWriter) emitContentPart(part messages.ContentPart) bool {
	switch p := part.(type) {
	case messages.TextPart:
		w.emitText(p.Text)
		return true
	case messages.ImagePart:
		return w.emitBinary(p.Bytes, binaryBoundary{messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd, messages.NewImageStartValue(p.MediaType), messages.NewImageDeltaValue(p.Bytes), messages.NewImageEndValue()})
	case messages.AudioPart:
		return w.emitBinary(p.Bytes, binaryBoundary{messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd, messages.NewAudioStartValue(), messages.NewAudioDeltaValue(p.Bytes), messages.NewAudioEndValue()})
	case messages.VideoPart:
		return w.emitBinary(p.Bytes, binaryBoundary{messages.StreamTypeVideoStart, messages.StreamTypeVideoDelta, messages.StreamTypeVideoEnd, messages.NewVideoStartValue(p.MediaType), messages.NewVideoDeltaValue(p.Bytes), messages.NewVideoEndValue()})
	case messages.FilePart:
		return w.emitBinary(p.Bytes, binaryBoundary{messages.StreamTypeFileStart, messages.StreamTypeFileDelta, messages.StreamTypeFileEnd, messages.NewFileStartValue(p.MediaType, p.Name), messages.NewFileDeltaValue(p.Bytes), messages.NewFileEndValue()})
	}
	return false
}

func (w *toolStreamWriter) emitBinary(payload []byte, boundary binaryBoundary) bool {
	if len(payload) == 0 {
		return false
	}
	w.writeContent(boundary.startType, boundary.start)
	w.writeContent(boundary.deltaType, boundary.delta)
	w.writeContent(boundary.endType, boundary.end)
	return true
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

	batch := &toolBatch{
		calls:              calls,
		results:            make([]messages.ToolCallResponse, len(calls)),
		errs:               make([]error, len(calls)),
		pendingLongRunning: r.longRunningCalls(calls),
	}
	resultCh := make(chan toolExecutionResult, len(calls))
	for i, tc := range calls {
		go func(idx int, call messages.ToolCall) {
			resp, err := r.executor.Execute(ctx, call)
			resultCh <- toolExecutionResult{index: idx, response: resp, err: err}
		}(i, tc)
	}
	r.collectBatch(ctx, batch, resultCh)

	// Return all errors aggregated so callers see every failure, not just the first.
	if joined := errors.Join(batch.errs...); joined != nil {
		return nil, joined
	}

	return batch.results, nil
}

type toolExecutionResult struct {
	index    int
	response messages.ToolCallResponse
	err      error
}

// toolBatch holds the ordered outcomes of one executeBatch call and the
// long-running calls that have not completed yet.
type toolBatch struct {
	calls               []messages.ToolCall
	results             []messages.ToolCallResponse
	errs                []error
	pendingLongRunning  map[int]messages.ToolCall
	acknowledgementSent bool
}

func (b *toolBatch) record(result toolExecutionResult) {
	if result.err != nil {
		b.errs[result.index] = fmt.Errorf("tool %q failed: %w", b.calls[result.index].Name, result.err)
	} else {
		b.results[result.index] = result.response
	}
	delete(b.pendingLongRunning, result.index)
}

// pendingCalls returns the still-running long-running calls in input order.
func (b *toolBatch) pendingCalls() []messages.ToolCall {
	pending := make([]messages.ToolCall, 0, len(b.pendingLongRunning))
	for i := range b.calls {
		if call, ok := b.pendingLongRunning[i]; ok {
			pending = append(pending, call)
		}
	}
	return pending
}

func (r *ToolRunner) longRunningCalls(calls []messages.ToolCall) map[int]messages.ToolCall {
	pendingLongRunning := make(map[int]messages.ToolCall)
	for i, call := range calls {
		if r.acknowledgementThreshold > 0 && r.isLongRunningTool != nil && r.isLongRunningTool(call.Name) {
			pendingLongRunning[i] = call
		}
	}
	return pendingLongRunning
}

// collectBatch waits for every worker outcome while at most once requesting
// an acknowledgement for long-running calls that outlive the threshold.
func (r *ToolRunner) collectBatch(ctx context.Context, batch *toolBatch, resultCh <-chan toolExecutionResult) {
	var acknowledgementTimer *time.Timer
	var acknowledgementCh <-chan time.Time
	if len(batch.pendingLongRunning) > 0 {
		acknowledgementTimer = time.NewTimer(r.acknowledgementThreshold)
		acknowledgementCh = acknowledgementTimer.C
		defer acknowledgementTimer.Stop()
	}
	ctxDone := ctx.Done()
	completed := 0
	for completed < len(batch.calls) {
		select {
		case result := <-resultCh:
			batch.record(result)
			completed++
		case <-acknowledgementCh:
			r.acknowledgePending(ctx, batch)
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
}

func (r *ToolRunner) acknowledgePending(ctx context.Context, batch *toolBatch) {
	if batch.acknowledgementSent || len(batch.pendingLongRunning) == 0 || ctx.Err() != nil {
		return
	}
	batch.acknowledgementSent = true
	if r.sendAcknowledgement != nil {
		r.sendAcknowledgement(ctx, batch.pendingCalls())
	}
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
