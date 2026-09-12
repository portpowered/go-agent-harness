package agentruntime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type capturingSeedSession struct {
	mu   sync.Mutex
	sent []messages.StreamMessage
	done chan struct{}
}

func (s *capturingSeedSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return true
}

func (s *capturingSeedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}

func (s *capturingSeedSession) Done() <-chan struct{} { return s.done }

func (s *capturingSeedSession) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

type capturingSeedInferencer struct{ session messages.Session }

func (i *capturingSeedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func TestSessionTextSeedAdapterDelegatesReplacementToRuntimeService(t *testing.T) {
	const seedValue = "Say hello in one short sentence."
	const wirePrompt = "\x00agent-cli-session-text-seed:test:1"
	capturing := &capturingSeedSession{done: make(chan struct{})}
	inferencer := &sessionTextSeedInferencer{
		inner:      &capturingSeedInferencer{session: capturing},
		wirePrompt: wirePrompt,
		value:      seedValue,
	}
	session, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("cleanup close: %v", err)
		}
	})

	ctx := context.Background()
	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(wirePrompt),
	}) {
		t.Fatal("first send rejected")
	}
	followUp := "second runtime message"
	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(followUp),
	}) {
		t.Fatal("second send rejected")
	}

	capturing.mu.Lock()
	sent := append([]messages.StreamMessage(nil), capturing.sent...)
	capturing.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("forwarded message count = %d, want 2", len(sent))
	}
	first, ok := sent[0].Value.(*messages.TextDeltaValue)
	if !ok || first.Content != seedValue {
		t.Fatalf("connect-time prompt = %#v, want %q", sent[0].Value, seedValue)
	}
	second, ok := sent[1].Value.(*messages.TextDeltaValue)
	if !ok || second.Content != followUp {
		t.Fatalf("runtime Send text = %#v, want %q", sent[1].Value, followUp)
	}
	for i, msg := range sent {
		value, _ := msg.Value.(*messages.TextDeltaValue)
		if value != nil && strings.Contains(value.Content, "agent-cli-session-text-seed:") && value.Content != seedValue {
			t.Fatalf("forwarded message %d still carries the wire sentinel: %q", i, value.Content)
		}
	}
}
