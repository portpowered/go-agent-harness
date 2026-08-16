package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const turnFact = "fact: the marigold key is hidden under stone seven"

var noTurnSetup = func(*SessionTurns) {}

func TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle(t *testing.T) {
	inferencer := &turnTestSession{scriptedSession: newScriptedSession()}
	var session *SessionTurns
	events := []TurnEvent{}
	session = NewSessionTurns(SessionTurnsOptions{SessionInferencer: inferencer, EventSink: func(event TurnEvent) {
		events = append(events, event)
		_ = session.History()
		_ = session.NextTurnIndex()
	}})
	failure := errors.New("provider response failed")
	inferencer.failNext = failure
	if _, err := session.RunTurn(context.Background(), NewTextTurnInput("failed attempt"), TurnDirectionUser, 1, 2); err == nil || !errors.Is(err, failure) {
		t.Fatalf("failed turn error = %v", err)
	}
	assertTurnState(t, session, events, 1, 0, 1, false)
	events = nil
	inputs := []TurnInput{NewTextTurnInput(turnFact), NewAudioTurnInput([]byte{1, 2}, "audio/pcm"), NewAudioTurnInput([]byte{3, 4}, "audio/pcm"), NewTextTurnInput("Please recall the fact."), NewTextTurnInput("End the scripted conversation.")}
	for i, input := range inputs {
		turn, err := session.RunTurn(context.Background(), input, TurnDirectionUser, uint64(2*i+1), uint64(2*i+2))
		if err != nil || turn.Index != uint64(i+1) || turn.Response.TextContent() == "" {
			t.Fatalf("turn %d = %#v, err=%v", i+1, turn, err)
		}
	}
	history := session.History()
	if inferencer.connects != 1 || len(history) != 5 || len(events) != 10 || session.NextTurnIndex() != 6 || !strings.Contains(history[3].Response.TextContent(), "marigold key") || string(history[1].Input.Audio) != string([]byte{1, 2}) || string(history[2].Input.Audio) != string([]byte{3, 4}) {
		t.Fatalf("connection/history/events/next/recall/audio = %d/%d/%d/%d/%q/%v/%v", inferencer.connects, len(history), len(events), session.NextTurnIndex(), history[3].Response.TextContent(), history[1].Input.Audio, history[2].Input.Audio)
	}
	for i, event := range events {
		tick, start := uint64(i+1), i%2 == 0
		valid := event.Index == uint64(i/2+1) && event.Direction == TurnDirectionUser && event.Tick == tick && event.StartTick == tick-uint64(i%2) && ((start && event.Type == TurnEventStart && event.EndTick == 0) || (!start && event.Type == TurnEventEnd && event.EndTick == tick))
		if !valid {
			t.Fatalf("event %d invalid: %#v", i, event)
		}
	}
	if err := session.Close(); err != nil || session.NextTurnIndex() != 6 || len(session.History()) != 5 {
		t.Fatalf("clean close = %v, next/history=%d/%d", err, session.NextTurnIndex(), len(session.History()))
	}
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
		{"overlap", setupText("one", 1), tryStart(NewTextTurnInput("two"), TurnDirectionUser, 2), ErrTurnAlreadyActive, 1, 0, 1, true},
		{"end without start", noTurnSetup, tryText("ok", 1), ErrTurnEndWithoutStart, 0, 0, 1, false},
		{"empty text", noTurnSetup, tryStart(NewTextTurnInput(" "), TurnDirectionUser, 1), ErrEmptyTurn, 0, 0, 1, false},
		{"zero audio", noTurnSetup, tryStart(NewAudioTurnInput(nil, "audio/pcm"), TurnDirectionUser, 1), ErrEmptyTurn, 0, 0, 1, false},
		{"bad direction", noTurnSetup, tryStart(NewTextTurnInput("input"), TurnDirection("sideways"), 1), ErrInvalidTurnDirection, 0, 0, 1, false},
		{"non increasing tick", setupComplete("one", 3, 4), tryStart(NewTextTurnInput("two"), TurnDirectionUser, 4), ErrInvalidTurnTick, 2, 1, 2, false},
		{"invalid end tick", setupText("input", 10), tryText("ok", 10), ErrInvalidTurnTick, 1, 0, 1, true},
		{"empty response text part", setupText("input", 1), tryMessage(messages.Message{ContentParts: []messages.ContentPart{messages.TextPart{Text: " "}}}, 2), ErrEmptyTurn, 1, 0, 1, true},
		{"empty response audio part", setupText("input", 1), tryMessage(messages.Message{ContentParts: []messages.ContentPart{messages.AudioPart{}}}, 2), ErrEmptyTurn, 1, 0, 1, true},
		{"close active", setupText("input", 1), (*SessionTurns).Close, ErrSessionEndedWithActiveTurn, 1, 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := []TurnEvent{}
			session := NewSessionTurns(SessionTurnsOptions{EventSink: func(event TurnEvent) { events = append(events, event) }})
			tc.setup(session)
			err := tc.try(session)
			if err == nil || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			assertTurnState(t, session, events, tc.events, tc.history, tc.next, tc.active)
		})
	}
}
func start(s *SessionTurns, in TurnInput, d TurnDirection, n uint64) (err error) {
	_, err = s.StartTurn(in, d, n)
	return
}
func finish(s *SessionTurns, x string, n uint64) (err error) {
	return finishMessage(s, messages.NewTextMessage(messages.RoleAssistant, x), n)
}
func finishMessage(s *SessionTurns, message messages.Message, n uint64) (err error) {
	_, err = s.EndTurn(0, "", message, n)
	return
}
func setupText(text string, tick uint64) func(*SessionTurns) {
	return func(s *SessionTurns) { _ = start(s, NewTextTurnInput(text), TurnDirectionUser, tick) }
}
func setupComplete(text string, startTick, endTick uint64) func(*SessionTurns) {
	return func(s *SessionTurns) {
		_ = start(s, NewTextTurnInput(text), TurnDirectionUser, startTick)
		_ = finish(s, "ok", endTick)
	}
}
func tryStart(in TurnInput, d TurnDirection, tick uint64) func(*SessionTurns) error {
	return func(s *SessionTurns) error { return start(s, in, d, tick) }
}
func tryText(text string, tick uint64) func(*SessionTurns) error {
	return func(s *SessionTurns) error { return finish(s, text, tick) }
}
func tryMessage(message messages.Message, tick uint64) func(*SessionTurns) error {
	return func(s *SessionTurns) error { return finishMessage(s, message, tick) }
}
func assertTurnState(t *testing.T, s *SessionTurns, events []TurnEvent, wantEvents, wantHistory, wantNext int, active bool) {
	history, next, gotActive := s.History(), s.NextTurnIndex(), s.active != nil
	if len(events) != wantEvents || len(history) != wantHistory || int(next) != wantNext || gotActive != active {
		t.Fatalf("state events/history/next/active = %d/%d/%d/%v", len(events), len(history), next, gotActive)
	}
}

func TestSessionTurns_SerializesBlockedEventPublication(t *testing.T) {
	events := make(chan TurnEvent, 2)
	startEntered, releaseStart := make(chan struct{}), make(chan struct{})
	s := NewSessionTurns(SessionTurnsOptions{EventSink: func(event TurnEvent) {
		if event.Type == TurnEventStart {
			close(startEntered)
			<-releaseStart
		}
		events <- event
	}})
	startDone := make(chan error, 1)
	go func() { _, err := s.StartTurn(NewTextTurnInput("input"), TurnDirectionUser, 1); startDone <- err }()
	<-startEntered
	endDone := make(chan error, 1)
	go func() {
		_, err := s.EndTurn(1, TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "response"), 2)
		endDone <- err
	}()
	if s.transitionMu.TryLock() {
		s.transitionMu.Unlock()
		t.Fatal("transition publication was not serialized")
	}
	close(releaseStart)
	if err := <-startDone; err != nil {
		t.Fatalf("start error = %v", err)
	}
	if err := <-endDone; err != nil {
		t.Fatalf("end error = %v", err)
	}
	first, second := <-events, <-events
	if first.Type != TurnEventStart || second.Type != TurnEventEnd {
		t.Fatalf("events = %#v, %#v, want start then end", first, second)
	}
}

type turnTestSession struct {
	*scriptedSession
	failNext error
	fact     string
	connects int
}

func (s *turnTestSession) ConnectSession(context.Context) (messages.Session, error) {
	s.connects++
	return s, nil
}
func (s *turnTestSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		s.respond(NewTextTurnInput(value.Content))
	case *messages.AudioDeltaValue:
		s.respond(NewAudioTurnInput(value.Content, ""))
	}
	return true
}
func (s *turnTestSession) respond(input TurnInput) {
	if s.failNext != nil {
		s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(s.failNext)})
		s.failNext = nil
		return
	}
	response := "acknowledged"
	if strings.HasPrefix(input.Text, "fact:") {
		s.fact = input.Text
	} else if s.fact != "" && strings.Contains(input.Text, "recall") {
		response = "I remember " + s.fact
	}
	s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(response)})
	s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}
