package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type compatibilitySession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	connects  int
	closeCall int
}

func newCompatibilitySession() *compatibilitySession {
	return &compatibilitySession{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{})}
}

func (s *compatibilitySession) ConnectSession(context.Context) (messages.Session, error) {
	s.connects++
	return s, nil
}

func (s *compatibilitySession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if message.Type != messages.StreamTypeTextDelta && message.Type != messages.StreamTypeAudioDelta {
		return true
	}
	s.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ack")})
	s.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	return true
}

func (s *compatibilitySession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *compatibilitySession) Done() <-chan struct{} { return s.done }
func (s *compatibilitySession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCall++
		close(s.done)
	})
	return nil
}

func TestDeprecatedSessionTurnsDelegatesToReusableRuntime(t *testing.T) {
	provider := newCompatibilitySession()
	var events []TurnEvent
	session := NewSessionTurns(SessionTurnsOptions{
		SessionInferencer: provider,
		EventSink: func(event TurnEvent) {
			events = append(events, event)
		},
	})
	if _, err := session.RunTurn(context.Background(), NewTextTurnInput("text"), TurnDirectionUser, 1, 2); err != nil {
		t.Fatal(err)
	}
	audio := []byte{1, 2, 3}
	if _, err := session.RunTurn(context.Background(), NewAudioTurnInput(audio, "audio/pcm"), TurnDirectionUser, 3, 4); err != nil {
		t.Fatal(err)
	}
	audio[0] = 99
	history := session.History()
	if provider.connects != 1 || len(history) != 2 || session.NextTurnIndex() != 3 || len(events) != 4 {
		t.Fatalf("connections/history/next/events = %d/%d/%d/%d", provider.connects, len(history), session.NextTurnIndex(), len(events))
	}
	if string(history[1].Input.Audio) != string([]byte{1, 2, 3}) || history[1].Response.TextContent() != "ack" {
		t.Fatalf("history = %#v", history)
	}
	history[1].Input.Audio[0] = 77
	if session.History()[1].Input.Audio[0] != 1 {
		t.Fatal("compatibility adapter returned aliased history")
	}
	if err := session.Close(); err != nil || provider.closeCall != 1 {
		t.Fatalf("close = %v, calls=%d", err, provider.closeCall)
	}
	if err := session.Close(); err != nil || provider.closeCall != 1 {
		t.Fatalf("repeated close = %v, calls=%d", err, provider.closeCall)
	}
	if _, err := session.StartTurn(NewTextTurnInput("closed"), TurnDirectionUser, 5); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed adapter start = %v", err)
	}
}

func TestDeprecatedSessionTurnsRetainsTransitionErrors(t *testing.T) {
	session := NewSessionTurns(SessionTurnsOptions{})
	if _, err := session.StartTurn(NewTextTurnInput(" "), TurnDirectionUser, 1); !errors.Is(err, ErrEmptyTurn) {
		t.Fatalf("empty start = %v", err)
	}
	if _, err := session.EndTurn(0, "", messages.NewTextMessage(messages.RoleAssistant, "response"), 1); !errors.Is(err, ErrTurnEndWithoutStart) {
		t.Fatalf("end without start = %v", err)
	}
}
