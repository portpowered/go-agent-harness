package consumer_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate/wire"
)

type session struct {
	receive  *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	mu       sync.Mutex
	sent     []messages.StreamMessage
	sendErr  error
	closeErr error
}

func newSession() *session {
	return &session{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
}

func (s *session) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *session) SendWithOutcome(_ context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	if s.sendErr != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: s.sendErr}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *session) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *session) Done() <-chan struct{} { return s.done }

func (s *session) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return s.closeErr
}

func read(t *testing.T, buffer *messages.TypedBuffer[messages.StreamMessage]) messages.StreamMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := buffer.ReadContext(ctx)
	if err != nil {
		t.Fatalf("read public session event: %v", err)
	}
	return msg
}

func TestPublicWireSessionUpdateConsumer(t *testing.T) {
	inner := newSession()
	decorated := wire.NewService().DecorateSession(context.Background(), inner, sessionupdate.Config{Instructions: "consumer instructions"})
	if !decorated.SupportsResponseRequests() {
		// The public decorator is allowed to report unsupported optional
		// capabilities; this check documents that it remains callable without
		// a CLI session type.
		if _, ok := interface{}(decorated).(sessionupdate.DecoratedSession); !ok {
			t.Fatal("Wire service did not return the public contract")
		}
	}
	inner.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	inner.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("final output")})
	_ = inner.Close()
	if got := read(t, decorated.Receive()); got.Type != messages.StreamTypeSessionOpen {
		t.Fatalf("event type = %s, want SESSION.OPEN", got.Type)
	}
	if got := read(t, decorated.Receive()); got.Type != messages.StreamTypeTextDelta {
		t.Fatalf("final event type = %s, want TEXT.DELTA", got.Type)
	}
	inner.mu.Lock()
	sent := append([]messages.StreamMessage(nil), inner.sent...)
	inner.mu.Unlock()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("sent events = %#v, want exactly one SESSION.UPDATE", sent)
	}
	if err := decorated.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPublicWirePreservesTypedRejectedUpdate(t *testing.T) {
	inner := newSession()
	inner.sendErr = errors.New("rejected")
	decorated := wire.NewService().DecorateSession(context.Background(), inner, sessionupdate.Config{})
	inner.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	failure := read(t, decorated.Receive())
	value, ok := failure.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("failure value = %T, want *ErrorValue", failure.Value)
	}
	var updateErr *sessionupdate.UpdateSendError
	if !errors.As(value.Err, &updateErr) {
		t.Fatal("public consumer could not recover typed update error")
	}
}
