package agentloop

import (
	"sync"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

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
}

// Text returns the text of the last assistant message that contains text content
// and carries no pending tool calls. This is the final "answer" of the turn.
func (r ExecuteResult) Text() string {
	var finalText string
	for _, m := range r.Messages {
		if m.Role != messages.RoleAssistant {
			continue
		}
		if len(m.ToolCalls) > 0 || m.HasOnlyReasoning() {
			continue
		}
		if t := m.TextContent(); t != "" {
			finalText = t
		}
	}
	return finalText
}

// Response is a single streaming event delivered by a [Stream].
type Response = messages.StreamMessage

// Stream is an iterator-style interface wrapping the event channel produced by
// [AgenticLoop.ExecuteStreaming]. Use HasNext/Response to consume events in a loop;
// call Close when done to release underlying resources.
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
	// Close discards any remaining events and releases resources. Safe to call
	// multiple times; subsequent calls are no-ops.
	Close() error
}

var _ Stream = &chanStream{}

// chanStream implements [Stream] over a receive-only channel.
type chanStream struct {
	ch      <-chan messages.StreamMessage
	current messages.StreamMessage
	done    bool
	err     error
	once    sync.Once
	closed  chan struct{}
}

func newChanStream(ch <-chan messages.StreamMessage) *chanStream {
	return &chanStream{ch: ch, closed: make(chan struct{})}
}

// HasNext blocks until the next event arrives or the stream ends or is closed.
func (s *chanStream) HasNext() bool {
	if s.done {
		return false
	}
	select {
	case evt, ok := <-s.ch:
		if !ok {
			s.done = true
			return false
		}
		s.current = evt
		return true
	case <-s.closed:
		s.done = true
		return false
	}
}

func (s *chanStream) Response() Response { return s.current }
func (s *chanStream) Err() error         { return s.err }

// Close signals that no more events will be consumed. A background goroutine
// drains the underlying channel so the producer never blocks.
func (s *chanStream) Close() error {
	s.once.Do(func() {
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
