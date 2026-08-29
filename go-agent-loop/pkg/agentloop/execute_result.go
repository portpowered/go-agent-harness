package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// FinalTextStatus is the explicit terminal outcome reported by
// [ExecuteResult.FinalText].
type FinalTextStatus string

const (
	// FinalTextSuccess means the turn completed and produced non-empty final text.
	FinalTextSuccess FinalTextStatus = "success"
	// FinalTextEmptySuccess means the turn completed with an explicit empty final
	// assistant text part.
	FinalTextEmptySuccess FinalTextStatus = "empty_success"
	// FinalTextNoFinalMessage means the turn completed but did not produce a
	// final assistant text message.
	FinalTextNoFinalMessage FinalTextStatus = "no_final_message"
	// FinalTextCanceled means the turn ended with caller-owned cancellation or a
	// deadline. Text may contain partial output when Partial is true.
	FinalTextCanceled FinalTextStatus = "canceled"
	// FinalTextFailed means the turn ended with a non-cancellation terminal
	// error. Text may contain partial output when Partial is true.
	FinalTextFailed FinalTextStatus = "failed"
)

// FinalTextResult is the explicit final text contract for [ExecuteResult].
//
// It distinguishes an explicit empty assistant answer from a missing final
// message, cancellation, terminal failure, and partial output. Legacy callers can
// keep using [ExecuteResult.Text], which returns only the final non-empty text.
type FinalTextResult struct {
	Text           string
	Status         FinalTextStatus
	Err            error
	Partial        bool
	TerminalSource messages.TerminalSource
}

// ExecuteResult is the response returned by [AgenticLoop.Execute].
// It contains all messages produced during the execution turn (model responses,
// tool-call messages, tool results) and provides a convenience accessor for the
// final model text.
type ExecuteResult struct {
	Deltas []messages.StreamMessage
	// Messages contains every message dispatched by the engine during this turn,
	// in order: model responses (including intermediate tool-call messages) and
	// tool results. The user's input message is not included.
	Messages []messages.Message
	// Err records the terminal execution error for callers that receive or retain
	// an ExecuteResult alongside the error returned by Execute.
	Err error
}

// Text returns the text of the last assistant message that contains text content
// and carries no pending tool calls. This is the final "answer" of the turn.
//
// Text is retained for source compatibility. New integrations that need to
// distinguish empty success, missing final text, cancellation, terminal failure,
// or partial output should use [ExecuteResult.FinalText].
func (r ExecuteResult) Text() string {
	text, found := r.finalAssistantText()
	if !found || text == "" {
		return ""
	}
	return text
}

// FinalText returns the explicit final text outcome for this execution.
func (r ExecuteResult) FinalText() FinalTextResult {
	text, found := r.finalAssistantText()
	terminalSource := r.terminalSourceFromDeltas()
	if r.Err != nil {
		status := FinalTextFailed
		if errors.Is(r.Err, context.Canceled) || errors.Is(r.Err, context.DeadlineExceeded) {
			status = FinalTextCanceled
		}
		if !found {
			text, found = r.partialTextFromDeltas()
		}
		return FinalTextResult{
			Text:           text,
			Status:         status,
			Err:            r.Err,
			Partial:        found,
			TerminalSource: terminalSource,
		}
	}
	if found {
		if text == "" {
			return FinalTextResult{Status: FinalTextEmptySuccess, TerminalSource: terminalSource}
		}
		return FinalTextResult{Text: text, Status: FinalTextSuccess, TerminalSource: terminalSource}
	}
	return FinalTextResult{Status: FinalTextNoFinalMessage, TerminalSource: terminalSource}
}

func (r ExecuteResult) finalAssistantText() (string, bool) {
	var finalText string
	found := false
	for _, m := range r.Messages {
		if m.Role != messages.RoleAssistant {
			continue
		}
		if len(m.ToolCalls) > 0 || m.HasOnlyReasoning() {
			continue
		}
		if m.HasText() {
			finalText = m.TextContent()
			found = true
		}
	}
	return finalText, found
}

func (r ExecuteResult) partialTextFromDeltas() (string, bool) {
	var partial string
	found := false
	for _, delta := range r.Deltas {
		if delta.Type != messages.StreamTypeTextDelta {
			continue
		}
		if v, ok := delta.Value.(*messages.TextDeltaValue); ok {
			partial += v.Content
			found = true
		}
	}
	return partial, found
}

func (r ExecuteResult) terminalSourceFromDeltas() messages.TerminalSource {
	var source messages.TerminalSource
	for _, delta := range r.Deltas {
		if delta.Type != messages.StreamTypeMessageEnd {
			continue
		}
		if v, ok := delta.Value.(*messages.MessageEndValue); ok {
			source = messages.MessageEndTerminalSource(v)
		}
	}
	return source
}

// Response is a single streaming event delivered by a [Stream].
type Response = messages.StreamMessage

// StreamStatus is the explicit lifecycle outcome reported by [Stream.Outcome].
type StreamStatus string

const (
	// StreamOpen means the stream has not reached a terminal state.
	StreamOpen StreamStatus = "open"
	// StreamDrained means the producer closed the stream after all events were read.
	StreamDrained StreamStatus = "drained"
	// StreamClosed means the caller closed the stream before draining it.
	StreamClosed StreamStatus = "closed"
	// StreamCanceled means the stream ended because caller-owned context was
	// cancelled or reached its deadline.
	StreamCanceled StreamStatus = "canceled"
	// StreamFailed means the stream ended with a non-cancellation terminal error.
	StreamFailed StreamStatus = "failed"
)

// StreamOutcome is the explicit terminal state for a streaming response.
//
// Partial is true when at least one event was delivered before a cancellation or
// terminal failure, allowing callers to distinguish total failure from partial
// success with inspectable Err metadata.
type StreamOutcome struct {
	Status         StreamStatus
	Err            error
	Partial        bool
	TerminalSource messages.TerminalSource
}

// Stream is an iterator-style interface wrapping the event channel produced by
// [AgenticLoop.ExecuteStreaming]. Use HasNext/Response to consume events in a loop;
// call Close when done to release underlying resources. New integrations that
// need to distinguish drained, closed, cancelled, and failed terminal states
// should inspect Outcome after HasNext returns false.
type Stream interface {
	// HasNext blocks until the next event is available or the stream is exhausted.
	// Returns true when an event is ready; call Response() to retrieve it.
	// Returns false when the stream has ended or Close has been called.
	HasNext() bool
	// Response returns the most recent event received by the last HasNext call.
	// Only valid after HasNext returned true.
	Response() Response
	// Err returns any error that stopped the stream, or nil on clean completion.
	Err() error
	// Outcome returns the current or terminal stream state. Before termination it
	// reports StreamOpen. After HasNext returns false, it distinguishes clean
	// drain, caller Close, cancellation, and terminal failure.
	Outcome() StreamOutcome
	// Close discards any remaining events and releases resources. Safe to call
	// multiple times; subsequent calls are no-ops.
	Close() error
}

var _ Stream = &chanStream{}

// chanStream implements [Stream] over a receive-only channel.
type chanStream struct {
	ch        <-chan streamEvent
	current   messages.StreamMessage
	done      bool
	err       error
	status    StreamStatus
	partial   bool
	delivered bool
	source    messages.TerminalSource
	once      sync.Once
	closed    chan struct{}
}

type streamEvent struct {
	event messages.StreamMessage
	err   error
}

func newChanStream(ch <-chan streamEvent) *chanStream {
	return &chanStream{ch: ch, status: StreamOpen, closed: make(chan struct{})}
}

// HasNext blocks until the next event arrives or the stream ends or is closed.
func (s *chanStream) HasNext() bool {
	if s.done {
		return false
	}
	select {
	case item, ok := <-s.ch:
		if !ok {
			s.done = true
			if s.status == StreamOpen {
				s.status = StreamDrained
			}
			return false
		}
		s.recordEvent(item)
		return true
	case <-s.closed:
		s.done = true
		if s.status == StreamOpen {
			s.status = StreamClosed
		}
		return false
	}
}

func (s *chanStream) Response() Response { return s.current }
func (s *chanStream) Err() error         { return s.err }
func (s *chanStream) Outcome() StreamOutcome {
	return StreamOutcome{Status: s.status, Err: s.err, Partial: s.partial, TerminalSource: s.source}
}

func (s *chanStream) recordEvent(item streamEvent) {
	s.current = item.event
	if item.err != nil {
		s.setTerminalError(item.err)
	} else if item.event.Type == messages.StreamTypeError {
		if value, ok := item.event.Value.(*messages.ErrorValue); !ok || value.IsTerminal() {
			s.setTerminalError(errorFromStreamEvent(item.event))
		}
	} else if item.event.Type == messages.StreamTypeMessageEnd {
		if v, ok := item.event.Value.(*messages.MessageEndValue); ok {
			s.source = messages.MessageEndTerminalSource(v)
		}
	}
	s.delivered = true
}

func (s *chanStream) setTerminalError(err error) {
	s.err = err
	s.partial = s.delivered
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.status = StreamCanceled
		return
	}
	s.status = StreamFailed
}

func errorFromStreamEvent(evt messages.StreamMessage) error {
	if v, ok := evt.Value.(*messages.ErrorValue); ok {
		if v.Err != nil {
			return v.Err
		}
		if v.Message != "" {
			return errors.New(v.Message)
		}
	}
	return errors.New("stream error")
}

// Close signals that no more events will be consumed. A background goroutine
// drains the underlying channel so the producer never blocks.
func (s *chanStream) Close() error {
	s.once.Do(func() {
		if s.status == StreamOpen {
			s.status = StreamClosed
		}
		close(s.closed)
		go func() {
			for range s.ch {
			}
		}()
	})
	return nil
}

// StreamingExecuteResult is the response returned by [AgenticLoop.ExecuteStreaming].
//
// Consume EventStream using HasNext/Response to receive typed delta events
// (TEXT.DELTA, REASONING.DELTA, etc.). The stream is exhausted after LOOP.END is
// processed. Consuming EventStream to completion avoids blocking the kernel.
// Messages() blocks until the turn is complete and returns all messages produced
// during this execution turn.
type StreamingExecuteResult struct {
	// EventStream delivers typed delta events as the model produces them.
	// Call HasNext to advance and Response to read each event. Call Close when done.
	EventStream Stream

	done chan struct{}
	mu   sync.Mutex
	msgs []messages.Message
}

// Messages blocks until the turn is complete and returns all messages produced
// during this execution turn. Safe to call multiple times.
func (r *StreamingExecuteResult) Messages() []messages.Message {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgs
}

// newStreamingExecuteResult creates an empty StreamingExecuteResult.
func newStreamingExecuteResult() *StreamingExecuteResult {
	return &StreamingExecuteResult{done: make(chan struct{})}
}
