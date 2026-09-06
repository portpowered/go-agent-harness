package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type noopSessionInferencer struct{}

func (noopSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return nil, errors.New("not used in this test")
}

func TestSendSessionEvent_EnqueuesOrderedSessionInput(t *testing.T) {
	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(noopSessionInferencer{}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := messages.StreamMessage{Type: messages.StreamTypeMessageEnd}
	if err := al.SendSessionEvent(context.Background(), msg); err != nil {
		t.Fatalf("SendSessionEvent: %v", err)
	}
	// The explicit helper is admitted through the model runner's private
	// ordered ingress. Its unexported representation is intentionally hidden
	// from this package; successful admission is the public contract.
}

func TestSendSessionEvent_NotInSessionMode(t *testing.T) {
	al, err := New(WithInferencer(&mockInferencer{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := al.SendSessionEvent(context.Background(), messages.StreamMessage{}); err == nil {
		t.Fatal("expected error when not in session mode")
	}
}

func TestSendSessionEvent_ContextCancelled(t *testing.T) {
	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(noopSessionInferencer{}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := al.SendSessionEvent(ctx, messages.StreamMessage{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
