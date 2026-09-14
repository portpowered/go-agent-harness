package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
	textseedwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed/wire"
)

type consumerSession struct {
	mu            sync.Mutex
	sent          []messages.StreamMessage
	complete      []messages.Message
	queued        []messages.Message
	responseCalls int
	incoming      *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	closed        bool
}

func newConsumerSession() *consumerSession {
	return &consumerSession{
		incoming: messages.NewTypedBuffer[messages.StreamMessage](4),
		done:     make(chan struct{}),
	}
}

func (s *consumerSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *consumerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.incoming }
func (s *consumerSession) Done() <-chan struct{}                                  { return s.done }

func (s *consumerSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

func (s *consumerSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.mu.Lock()
	s.responseCalls++
	s.mu.Unlock()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *consumerSession) SupportsResponseRequests() bool { return true }

func (s *consumerSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.complete = append(s.complete, msg)
	s.mu.Unlock()
	return true
}

func (s *consumerSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.queued = append(s.queued, msg)
	s.mu.Unlock()
	return true
}

func (s *consumerSession) SupportsCompleteMessages() bool { return true }

func (s *consumerSession) SupportsCompleteMessagesWithoutResponse() bool { return true }

func (s *consumerSession) TerminalError() error { return nil }

func TestExternalConsumerUsesOnlyPublicTextSeedAndWireContracts(t *testing.T) {
	service := textseedwire.NewService(textseed.AllocatorFunc(func() string { return "external-wire" }))
	if got := service.Allocate(); got != "external-wire" {
		t.Fatalf("injected allocation = %q", got)
	}
	inner := newConsumerSession()
	wrapper := service.WrapSession(context.Background(), inner, "external-wire", textseed.Seed{Value: "", Present: true})
	if !wrapper.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("external-wire"),
	}) {
		t.Fatal("seed send rejected")
	}
	inner.mu.Lock()
	if got := inner.sent[0].Value.(*messages.TextDeltaValue).Content; got != "" {
		t.Fatalf("sent seed = %q, want explicit empty seed", got)
	}
	inner.mu.Unlock()

	writeErr := errors.New("consumer write failed")
	output := service.NewOutput(consumerWriter{err: writeErr})
	if n, err := output.Write([]byte("ok")); n != 1 || err != writeErr || output.Err() != writeErr {
		t.Fatalf("output write = (%d, %v), retained=%v", n, err, output.Err())
	}
	if wrapper.Receive().Cap() != textseed.ReceiveCapacity {
		t.Fatalf("receive capacity = %d", wrapper.Receive().Cap())
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-wrapper.Done():
	case <-time.After(time.Second):
		t.Fatal("empty-seed wrapper did not shut down")
	}

	nonemptyInner := newConsumerSession()
	nonempty := service.WrapSession(context.Background(), nonemptyInner, "external-wire-2", textseed.Seed{Value: "nonempty seed", Present: true})
	if !nonempty.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("external-wire-2"),
	}) {
		t.Fatal("nonempty seed send rejected")
	}
	nonemptyInner.mu.Lock()
	if got := nonemptyInner.sent[0].Value.(*messages.TextDeltaValue).Content; got != "nonempty seed" {
		t.Fatalf("nonempty seed = %q, want nonempty seed", got)
	}
	nonemptyInner.mu.Unlock()
	if !nonempty.SupportsResponseRequests() || !nonempty.RequestResponse(context.Background()).OK() {
		t.Fatal("response capability was not forwarded")
	}
	if !nonempty.SupportsCompleteMessages() || !nonempty.SendMessage(context.Background(), messages.Message{}) {
		t.Fatal("complete-message capability was not forwarded")
	}
	if !nonempty.SupportsCompleteMessagesWithoutResponse() || !nonempty.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("deferred complete-message capability was not forwarded")
	}
	if err := nonempty.Close(); err != nil {
		t.Fatalf("nonempty close: %v", err)
	}
	select {
	case <-nonempty.Done():
	case <-time.After(time.Second):
		t.Fatal("nonempty wrapper did not shut down")
	}
}

type consumerWriter struct{ err error }

func (w consumerWriter) Write([]byte) (int, error) { return 1, w.err }
