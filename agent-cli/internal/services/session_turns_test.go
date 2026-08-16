package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const turnFact = "fact: the marigold key is hidden under stone seven"

func TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle(t *testing.T) {
	inferencer := newTurnTestSessionInferencer()
	var session *SessionTurns
	events := []TurnEvent{}
	session = NewSessionTurns(SessionTurnsOptions{SessionInferencer: inferencer, EventSink: func(event TurnEvent) {
		events = append(events, event)
		_ = session.History()
		_ = session.NextTurnIndex()
	}})
	inputs := []TurnInput{NewTextTurnInput(turnFact), NewAudioTurnInput([]byte{1, 2}, "audio/pcm"), NewAudioTurnInput([]byte{3, 4}, "audio/pcm"), NewTextTurnInput("Please recall the fact."), NewTextTurnInput("End the scripted conversation.")}
	for i, input := range inputs {
		turn, err := session.RunTurn(context.Background(), input, TurnDirectionUser, uint64(2*i+1), uint64(2*i+2))
		if err != nil || turn.Index != uint64(i+1) || strings.TrimSpace(turn.Response.TextContent()) == "" {
			t.Fatalf("turn %d = %#v, err=%v", i+1, turn, err)
		}
	}
	history := session.History()
	if inferencer.connects != 1 || len(inferencer.session.inputs) != 5 || len(history) != 5 || session.NextTurnIndex() != 6 || !strings.Contains(history[3].Response.TextContent(), "marigold key") {
		t.Fatalf("connection/input/history/next/recall = %d/%d/%d/%d/%q", inferencer.connects, len(inferencer.session.inputs), len(history), session.NextTurnIndex(), history[3].Response.TextContent())
	}
	if string(history[1].Input.Audio) != string([]byte{1, 2}) || string(history[2].Input.Audio) != string([]byte{3, 4}) {
		t.Fatalf("audio order = %v/%v", history[1].Input.Audio, history[2].Input.Audio)
	}
	if len(events) != 10 {
		t.Fatalf("events = %d, want 10", len(events))
	}
	for i, event := range events {
		start, end := uint64(2*(i/2)+1), uint64(2*(i/2)+2)
		want := TurnEvent{Type: TurnEventStart, Index: uint64(i/2 + 1), Direction: TurnDirectionUser, Tick: start, StartTick: start}
		if i%2 == 1 {
			want = TurnEvent{Type: TurnEventEnd, Index: want.Index, Direction: want.Direction, Tick: end, StartTick: start, EndTick: end}
		}
		if event != want {
			t.Fatalf("event %d = %#v, want %#v", i, event, want)
		}
	}
	if err := session.Close(); err != nil || session.NextTurnIndex() != 6 || len(session.History()) != 5 {
		t.Fatalf("clean close = %v, next/history=%d/%d", err, session.NextTurnIndex(), len(session.History()))
	}
}

func TestSessionTurns_InferenceFailureAbortsWithoutReconnectOrStateLeak(t *testing.T) {
	inferencer := newTurnTestSessionInferencer()
	failure := errors.New("provider response failed")
	inferencer.session.failNext = failure
	events := []TurnEvent{}
	session := NewSessionTurns(SessionTurnsOptions{SessionInferencer: inferencer, EventSink: func(event TurnEvent) { events = append(events, event) }})
	if _, err := session.RunTurn(context.Background(), NewTextTurnInput("first attempt"), TurnDirectionUser, 1, 2); err == nil || !errors.Is(err, failure) {
		t.Fatalf("failed turn error = %v", err)
	}
	assertTurnState(t, session, events, 1, 0, 1, false)
	turn, err := session.RunTurn(context.Background(), NewTextTurnInput("recovered"), TurnDirectionUser, 1, 2)
	if err != nil || turn.Index != 1 || inferencer.connects != 1 {
		t.Fatalf("recovered turn = %#v, err=%v, connections=%d", turn, err, inferencer.connects)
	}
	assertTurnState(t, session, events, 3, 1, 2, false)
}

func TestSessionTurns_InvalidTransitionsKeepStateAndEvents(t *testing.T) {
	cases := []struct {
		name                  string
		setup                 func(*SessionTurns)
		try                   func(*SessionTurns) error
		want                  error
		events, history, next int
		active                bool
	}{
		{"overlap", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("one"), TurnDirectionUser, 1) }, func(s *SessionTurns) error { return start(s, NewTextTurnInput("two"), TurnDirectionUser, 2) }, ErrTurnAlreadyActive, 1, 0, 1, true},
		{"end without start", nil, func(s *SessionTurns) error { return finish(s, "ok", 1) }, ErrTurnEndWithoutStart, 0, 0, 1, false},
		{"empty text", nil, func(s *SessionTurns) error { return start(s, NewTextTurnInput(" "), TurnDirectionUser, 1) }, ErrEmptyTurn, 0, 0, 1, false},
		{"zero audio", nil, func(s *SessionTurns) error {
			return start(s, NewAudioTurnInput(nil, "audio/pcm"), TurnDirectionUser, 1)
		}, ErrEmptyTurn, 0, 0, 1, false},
		{"bad direction", nil, func(s *SessionTurns) error { return start(s, NewTextTurnInput("input"), TurnDirection("sideways"), 1) }, ErrInvalidTurnDirection, 0, 0, 1, false},
		{"non increasing tick", func(s *SessionTurns) {
			_ = start(s, NewTextTurnInput("one"), TurnDirectionUser, 3)
			_ = finish(s, "ok", 4)
		}, func(s *SessionTurns) error { return start(s, NewTextTurnInput("two"), TurnDirectionUser, 4) }, ErrInvalidTurnTick, 2, 1, 2, false},
		{"invalid end tick", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 10) }, func(s *SessionTurns) error { return finish(s, "ok", 10) }, ErrInvalidTurnTick, 1, 0, 1, true},
		{"close active", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 1) }, (*SessionTurns).Close, ErrSessionEndedWithActiveTurn, 1, 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := []TurnEvent{}
			session := NewSessionTurns(SessionTurnsOptions{EventSink: func(event TurnEvent) { events = append(events, event) }})
			if tc.setup != nil {
				tc.setup(session)
			}
			err := tc.try(session)
			if err == nil || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			assertTurnState(t, session, events, tc.events, tc.history, tc.next, tc.active)
		})
	}
}

func start(s *SessionTurns, input TurnInput, direction TurnDirection, tick uint64) error {
	_, err := s.StartTurn(input, direction, tick)
	return err
}
func finish(s *SessionTurns, text string, tick uint64) error {
	_, err := s.CompleteTurn(messages.NewTextMessage(messages.RoleAssistant, text), tick)
	return err
}
func assertTurnState(t *testing.T, s *SessionTurns, events []TurnEvent, wantEvents, wantHistory, wantNext int, active bool) {
	t.Helper()
	if len(events) != wantEvents || len(s.History()) != wantHistory || int(s.NextTurnIndex()) != wantNext || (s.active != nil) != active {
		t.Fatalf("state events/history/next/active = %d/%d/%d/%v", len(events), len(s.History()), s.NextTurnIndex(), s.active != nil)
	}
}

type turnTestSessionInferencer struct {
	session  *turnTestSession
	connects int
}

func newTurnTestSessionInferencer() *turnTestSessionInferencer {
	return &turnTestSessionInferencer{session: &turnTestSession{recv: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{})}}
}
func (i *turnTestSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return i.session, nil
}

type turnTestSession struct {
	recv     *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	inputs   []TurnInput
	failNext error
}

func (s *turnTestSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	var input TurnInput
	switch msg.Type {
	case messages.StreamTypeTextDelta:
		input.Text = msg.Value.(*messages.TextDeltaValue).Content
	case messages.StreamTypeAudioDelta:
		input.Audio = msg.Value.(*messages.AudioDeltaValue).Content
	default:
		return true
	}
	s.respond(input)
	return true
}
func (s *turnTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *turnTestSession) Done() <-chan struct{}                                  { return s.done }
func (s *turnTestSession) Close() error                                           { close(s.done); return nil }
func (s *turnTestSession) respond(input TurnInput) {
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(err)})
		return
	}
	s.inputs = append(s.inputs, cloneInput(input))
	response := "acknowledged"
	if strings.Contains(input.Text, "recall") {
		for _, prior := range s.inputs {
			if strings.HasPrefix(prior.Text, "fact:") {
				response = "I remember " + prior.Text
				break
			}
		}
	}
	for _, msg := range []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}, {Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}, {Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(response)}, {Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}, {Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}} {
		s.recv.Write(context.Background(), msg)
	}
}
