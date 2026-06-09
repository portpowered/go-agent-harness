package participants

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// KernelRunner is responsible for dispatching messages to IO.
// It has a single inbox (DeltaInbox) that carries both streaming deltas and
// fully assembled messages wrapped as SYSTEM.FULL_MESSAGE events.
// Using a single queue eliminates ordering races between the two paths.
//
// Streaming readers:
//   - Call [NewDeltaEventReader] before starting the hot loop to obtain a channel of [StreamMessage].
//   - All delta types (TEXT.DELTA, AUDIO.DELTA, REASONING.DELTA, etc.) are forwarded to this channel.
//   - The channel is closed on LOOP.END or context cancellation.
type KernelRunner struct {
	logger logging.Logger
	// DeltaInbox receives messages as they come in.
	DeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest]

	// messageOutMu protects messageOutCh for concurrent access between
	// dispatchDelta (writer) and NewMessageReader/LoopEndValue (closer).
	messageOutMu sync.Mutex
	// messageOutCh, when non-nil, receives every full message dispatched during a
	// run via SYSTEM.FULL_MESSAGE events. Created by NewMessageReader; closed
	// on LOOP.END or when the context is cancelled via closeStreamWithError.
	messageOutCh chan messages.KernelMessageRequest

	// deltaEventMu protects deltaEventCh for concurrent access.
	deltaEventMu sync.Mutex
	// deltaEventCh, when non-nil, receives every StreamMessage delta dispatched during
	// a run. Created by NewDeltaEventReader; closed after LOOP.END or on context cancel.
	deltaEventCh chan messages.StreamMessage
}

func NewKernelRunner(logger logging.Logger, bufferCapacity int) *KernelRunner {
	return &KernelRunner{
		logger:     logger,
		DeltaInbox: messages.NewTypedBuffer[messages.KernelDeltaRequest](bufferCapacity),
	}
}

// Run processes the delta inbox, forwarding to output channels and the streaming reader.
func (r *KernelRunner) Run(ctx context.Context) error {
	for {
		err := r.Tick(ctx)
		if err != nil {
			return err
		}
	}
}

func (r *KernelRunner) Tick(ctx context.Context) error {
	select {
	case <-ctx.Done():
		r.closeStreamWithError(ctx.Err())
		return ctx.Err()
	case delta := <-r.DeltaInbox.Chan():
		r.dispatchDelta(ctx, delta)
	}
	return nil
}

func (r *KernelRunner) logInfo(msg string, fields ...logging.Field) {
	if r.logger != nil {
		r.logger.Info(msg, fields...)
	}
}

// NewDeltaEventReader creates and returns a channel that receives every StreamMessage
// delta dispatched during this run. Must be called before [Run] starts.
// The channel is closed after LOOP.END is processed (or on context cancellation),
// so callers can range over it to observe all delta events for the turn.
func (r *KernelRunner) NewDeltaEventReader(capacity int) <-chan messages.StreamMessage {
	r.deltaEventMu.Lock()
	defer r.deltaEventMu.Unlock()
	ch := make(chan messages.StreamMessage, capacity)
	r.deltaEventCh = ch
	return ch
}

// dispatchDelta forwards a delta to the out channel and writes content to the
// appropriate streaming pipe: TEXT.DELTA → text pipe, AUDIO.DELTA → audio pipe,
// REASONING.DELTA → reasoning pipe. All pipes and messageOutCh are closed on LOOP.END.
// SYSTEM.FULL_MESSAGE events are forwarded to messageOutCh as full messages.
func (r *KernelRunner) dispatchDelta(ctx context.Context, req messages.KernelDeltaRequest) {
	// Forward every delta except SYSTEM.FULL_MESSAGE to the event channel.
	// Full message events are internal coordination signals, not streaming events.
	if req.Delta.Type != messages.StreamTypeSystemFullMessage {
		r.deltaEventMu.Lock()
		evCh := r.deltaEventCh
		r.deltaEventMu.Unlock()
		if evCh != nil {
			evCh <- req.Delta
		}
	}

	switch v := req.Delta.Value.(type) {
	case *messages.InferenceResultValue:
		// Full message assembled by a participant (model, tool, or user); forward to
		// messageOutCh so Execute/ExecuteStreaming consumers can collect it.
		r.logInfo("KernelRunner: dispatching inference result", logging.Field{Key: "source", Value: v.Source})
		r.messageOutMu.Lock()
		ch := r.messageOutCh
		r.messageOutMu.Unlock()
		if ch != nil {
			ch <- messages.KernelMessageRequest{
				Source:  messages.ParticipantID(v.Source),
				Message: v.Message,
			}
		}
	case *messages.MessageStartValue:
		r.logInfo("KernelRunner: sending message start delta", logging.Field{Key: "actor", Value: req.Delta.Role})
	case *messages.TextStartValue:
		r.logInfo("KernelRunner: sending text start delta", logging.Field{Key: "actor", Value: req.Delta.Role}, logging.Field{Key: "index", Value: req.Delta.ActorProvidedIndex})
	case *messages.TextDeltaValue:
		r.logInfo("KernelRunner: sending text delta", logging.Field{Key: "delta", Value: v.Content}, logging.Field{Key: "actor", Value: req.Delta.Role}, logging.Field{Key: "index", Value: req.Delta.ActorProvidedIndex})

	case *messages.TextEndValue:
		r.logInfo("KernelRunner: sending text end delta", logging.Field{Key: "actor", Value: req.Delta.Role}, logging.Field{Key: "index", Value: req.Delta.ActorProvidedIndex})
	case *messages.AudioDeltaValue:
		r.logInfo("KernelRunner: sending audio delta", logging.Field{Key: "delta", Value: req.Delta})

	case *messages.ImageDeltaValue:
		r.logInfo("KernelRunner: sending image delta", logging.Field{Key: "bytes", Value: len(v.Content)})

	case *messages.VideoDeltaValue:
		r.logInfo("KernelRunner: sending video delta", logging.Field{Key: "bytes", Value: len(v.Content)})
	case *messages.FileDeltaValue:
		r.logInfo("KernelRunner: sending file delta", logging.Field{Key: "bytes", Value: len(v.Content)})

	case *messages.ReasoningStartValue:
		r.logInfo("KernelRunner: sending reasoning start delta", logging.Field{Key: "delta", Value: "<thinking>"})
	case *messages.ReasoningDeltaValue:
		r.logInfo("KernelRunner: sending reasoning delta", logging.Field{Key: "delta", Value: v.Content})

	case *messages.ReasoningEndValue:
		r.logInfo("KernelRunner: sending reasoning end delta", logging.Field{Key: "delta", Value: "</thinking>"})

	case *messages.MessageEndValue:
		// MESSAGE.END fires at the end of every model message, including intermediate ones
		// that trigger tool calls. Do not close the stream pipes here; wait for LOOP.END.
		r.logInfo("KernelRunner: message end delta (stream pipes remain open)", logging.Field{Key: "delta", Value: req.Delta})
	case *messages.UsageInfoValue:
		// USAGE.INFO carries token usage for the response that just ended; no stream write.
		r.logInfo("KernelRunner: usage info delta", logging.Field{Key: "usage", Value: v.Usage})
	case *messages.LoopEndValue:
		// LOOP.END is sent by CoordinatorDelta only when the agent has produced a final
		// user-facing response. Close all stream pipes, deltaEventCh, and messageOutCh
		// so that both delta stream consumers and full-message collectors exit cleanly.
		r.logInfo("KernelRunner: loop end delta, closing stream pipes", logging.Field{Key: "delta", Value: req.Delta})
		// Close the delta event channel so event stream consumers can exit.
		r.deltaEventMu.Lock()
		dch := r.deltaEventCh
		r.deltaEventCh = nil
		r.deltaEventMu.Unlock()
		if dch != nil {
			close(dch)
		}
		// Close messageOutCh so full-message collectors (Execute/ExecuteStreaming) exit.
		r.messageOutMu.Lock()
		mch := r.messageOutCh
		r.messageOutCh = nil
		r.messageOutMu.Unlock()
		if mch != nil {
			close(mch)
		}
	}
}

// closeStreamWithError is called when the kernel's context is cancelled (e.g. hot
// loop exited with error). We close both the message outbox and delta event channel
// so collectors can exit, but we do NOT close the delta stream pipes so that a
// retry can write RESET + new content to the same stream.
func (r *KernelRunner) closeStreamWithError(err error) {
	// Close the message outbox so any ranging goroutine can exit cleanly.
	r.messageOutMu.Lock()
	mch := r.messageOutCh
	r.messageOutCh = nil
	r.messageOutMu.Unlock()
	if mch != nil {
		close(mch)
	}
	// Close the delta event channel so event stream consumers can exit.
	r.deltaEventMu.Lock()
	dch := r.deltaEventCh
	r.deltaEventCh = nil
	r.deltaEventMu.Unlock()
	if dch != nil {
		close(dch)
	}
	// Drain DeltaInbox to discard any delta items written by CoordinatorDelta
	// during the failed run that the kernel goroutine did not get to process.
	// Without this drain, the retry run's kernel goroutine would process leftover
	// items (e.g. partial TEXT.DELTA from the failed run) as if they are from the
	// new inference, leading to incorrect REWRITE merges that include stale content.
	for {
		if _, ok := r.DeltaInbox.Read(); !ok {
			break
		}
	}
}
