package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
	textseedwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed/wire"
)

type consumerSession struct {
	mu       sync.Mutex
	sent     []messages.StreamMessage
	incoming *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	closed   bool
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
}

type consumerWriter struct{ err error }

func (w consumerWriter) Write([]byte) (int, error) { return 1, w.err }
