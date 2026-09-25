package turns

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const providerDetail = "provider detail"

type scriptedProtocolSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	accept    int
	sent      []messages.StreamMessage
	connectEr error
}

func newScriptedProtocolSession(accept int, replies ...messages.StreamMessage) *scriptedProtocolSession {
	s := &scriptedProtocolSession{recv: messages.NewTypedBuffer[messages.StreamMessage](recvCapacity), done: make(chan struct{}), accept: accept}
	for _, reply := range replies {
		s.recv.Write(context.Background(), reply)
	}
	return s
}

func (s *scriptedProtocolSession) ConnectSession(context.Context) (messages.Session, error) {
	if s.connectEr != nil {
		return nil, s.connectEr
	}
	return s, nil
}

func (s *scriptedProtocolSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	if len(s.sent) >= s.accept {
		return false
	}
	s.sent = append(s.sent, msg)
	return true
}

func (s *scriptedProtocolSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}
func (s *scriptedProtocolSession) Done() <-chan struct{} { return s.done }
func (s *scriptedProtocolSession) Close() error          { return nil }

func errorMessage(value *messages.ErrorValue) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeError, Value: value}
}

func runOne(t *testing.T, session *scriptedProtocolSession, input sessionturn.TurnInput) error {
	t.Helper()
	_, err := New(sessionturn.TurnsOptions{SessionInferencer: session}).RunTurn(context.Background(), input, sessionturn.TurnDirectionUser, 1, 2)
	return err
}

func TestRunTurnInputRejections(t *testing.T) {
	if err := runOne(t, newScriptedProtocolSession(0), textInput(inputText)); !errors.Is(err, errInputRejected) {
		t.Fatalf("text rejection = %v", err)
	}
	if err := runOne(t, newScriptedProtocolSession(1), audioInput([]byte{1})); !errors.Is(err, errCommitRejected) {
		t.Fatalf("commit rejection = %v", err)
	}
	connectFailure := errors.New("dial failed")
	session := newScriptedProtocolSession(1)
	session.connectEr = connectFailure
	if err := runOne(t, session, textInput(inputText)); !errors.Is(err, connectFailure) {
		t.Fatalf("connect failure = %v", err)
	}
}

func TestRunTurnResponseErrors(t *testing.T) {
	cause := errors.New("typed cause")
	cases := []struct {
		name  string
		reply messages.StreamMessage
		want  string
	}{
		{name: "typed", reply: errorMessage(messages.NewErrorValueWithError(cause)), want: cause.Error()},
		{name: "nil value", reply: messages.StreamMessage{Type: messages.StreamTypeError}, want: sessionturn.ErrSessionResponse.Error()},
		{name: "message", reply: errorMessage(&messages.ErrorValue{Message: providerDetail}), want: providerDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runOne(t, newScriptedProtocolSession(1, tc.reply), textInput(inputText))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunTurnSkipsNonTerminalErrorsAndHonorsClosure(t *testing.T) {
	nonTerminal := messages.NewErrorValue(providerDetail)
	nonTerminal.NonTerminal = true
	session := newScriptedProtocolSession(1, errorMessage(nonTerminal),
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(okResponse)},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if err := runOne(t, session, textInput(inputText)); err != nil {
		t.Fatalf("non-terminal skip = %v", err)
	}
	closed := newScriptedProtocolSession(1)
	close(closed.done)
	if err := runOne(t, closed, textInput(inputText)); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("closed = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(sessionturn.TurnsOptions{SessionInferencer: newScriptedProtocolSession(1)}).RunTurn(ctx, textInput(inputText), sessionturn.TurnDirectionUser, 1, 2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}
