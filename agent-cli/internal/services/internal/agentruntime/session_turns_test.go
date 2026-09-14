package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
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

func TestSessionTurnRuntimeDelegatesToReusableRuntime(t *testing.T) {
	provider := newCompatibilitySession()
	var events []sessionturn.TurnEvent
	service := sessionturnwire.NewDefaultService()
	runtime, err := service.Prepare(context.Background(), sessionturn.Request{
		SessionInferencer: provider,
		EventSink: func(event sessionturn.TurnEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(context.Background(), sessionturn.TurnRequest{Input: sessionturn.TurnInput{Text: "text"}, Direction: sessionturn.TurnDirectionUser, StartTick: 1, EndTick: 2}); err != nil {
		t.Fatal(err)
	}
	audio := []byte{1, 2, 3}
	if _, err := runtime.RunTurn(context.Background(), sessionturn.TurnRequest{Input: sessionturn.TurnInput{Audio: audio, MediaType: "audio/pcm"}, Direction: sessionturn.TurnDirectionUser, StartTick: 3, EndTick: 4}); err != nil {
		t.Fatal(err)
	}
	audio[0] = 99
	history := runtime.History()
	if provider.connects != 1 || len(history) != 2 || len(events) != 4 {
		t.Fatalf("connections/history/events = %d/%d/%d", provider.connects, len(history), len(events))
	}
	if string(history[1].Input.Audio) != string([]byte{1, 2, 3}) || history[1].Response.TextContent() != "ack" {
		t.Fatalf("history = %#v", history)
	}
	history[1].Input.Audio[0] = 77
	if runtime.History()[1].Input.Audio[0] != 1 {
		t.Fatal("compatibility adapter returned aliased history")
	}
	if err := runtime.Close(); err != nil || provider.closeCall != 1 {
		t.Fatalf("close = %v, calls=%d", err, provider.closeCall)
	}
	if err := runtime.Close(); err != nil || provider.closeCall != 1 {
		t.Fatalf("repeated close = %v, calls=%d", err, provider.closeCall)
	}
	if _, err := runtime.RunTurn(context.Background(), sessionturn.TurnRequest{Input: sessionturn.TurnInput{Text: "closed"}, Direction: sessionturn.TurnDirectionUser, StartTick: 5, EndTick: 6}); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("closed runtime start = %v", err)
	}
}

func TestSessionTurnRuntimeRetainsTransitionErrors(t *testing.T) {
	runtime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(context.Background(), sessionturn.TurnRequest{Input: sessionturn.TurnInput{Text: " "}, Direction: sessionturn.TurnDirectionUser, StartTick: 1, EndTick: 2}); !errors.Is(err, sessionturn.ErrEmptyTurn) {
		t.Fatalf("empty start = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}
