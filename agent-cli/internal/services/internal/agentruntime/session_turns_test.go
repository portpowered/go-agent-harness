package agentruntime

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"strings"
	"testing"
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
	}})
	failure := errors.New("provider response failed")
	inferencer.failNext = failure
	if _, err := session.RunTurn(context.Background(), NewTextTurnInput("failed attempt"), TurnDirectionUser, 1, 2); err == nil || !errors.Is(err, failure) || len(events) != 1 || len(session.History()) != 0 || session.NextTurnIndex() != 1 || session.active != nil {
		t.Fatalf("failed turn error = %v", err)
	}
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
		{"overlap", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("one"), TurnDirectionUser, 1) }, func(s *SessionTurns) error { return start(s, NewTextTurnInput("two"), TurnDirectionUser, 2) }, ErrTurnAlreadyActive, 1, 0, 1, true},
		{"end without start", noTurnSetup, func(s *SessionTurns) error { return end(s, messages.NewTextMessage(messages.RoleAssistant, "ok"), 1) }, ErrTurnEndWithoutStart, 0, 0, 1, false},
		{"empty text", noTurnSetup, func(s *SessionTurns) error { return start(s, NewTextTurnInput(" "), TurnDirectionUser, 1) }, ErrEmptyTurn, 0, 0, 1, false},
		{"zero audio", noTurnSetup, func(s *SessionTurns) error {
			return start(s, NewAudioTurnInput(nil, "audio/pcm"), TurnDirectionUser, 1)
		}, ErrEmptyTurn, 0, 0, 1, false},
		{"bad direction", noTurnSetup, func(s *SessionTurns) error { return start(s, NewTextTurnInput("input"), TurnDirection("sideways"), 1) }, ErrInvalidTurnDirection, 0, 0, 1, false},
		{"non increasing tick", func(s *SessionTurns) {
			_ = start(s, NewTextTurnInput("one"), TurnDirectionUser, 3)
			_ = end(s, messages.NewTextMessage(messages.RoleAssistant, "ok"), 4)
		}, func(s *SessionTurns) error { return start(s, NewTextTurnInput("two"), TurnDirectionUser, 4) }, ErrInvalidTurnTick, 2, 1, 2, false},
		{"invalid end tick", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 10) }, func(s *SessionTurns) error { return end(s, messages.NewTextMessage(messages.RoleAssistant, "ok"), 10) }, ErrInvalidTurnTick, 1, 0, 1, true},
		{"empty response text part", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 1) }, func(s *SessionTurns) error {
			return end(s, messages.Message{ContentParts: []messages.ContentPart{messages.TextPart{Text: " "}}}, 2)
		}, ErrEmptyTurn, 1, 0, 1, true},
		{"empty response audio part", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 1) }, func(s *SessionTurns) error {
			return end(s, messages.Message{ContentParts: []messages.ContentPart{messages.AudioPart{}}}, 2)
		}, ErrEmptyTurn, 1, 0, 1, true},
		{"close active", func(s *SessionTurns) { _ = start(s, NewTextTurnInput("input"), TurnDirectionUser, 1) }, (*SessionTurns).Close, ErrSessionEndedWithActiveTurn, 1, 0, 1, true},
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
			if len(events) != tc.events || len(session.History()) != tc.history || int(session.NextTurnIndex()) != tc.next || (session.active != nil) != tc.active {
				t.Fatalf("state events/history/next/active = %d/%d/%d/%v", len(events), len(session.History()), session.NextTurnIndex(), session.active != nil)
			}
		})
	}
}
func transitionErr[T any](_ T, err error) error { return err }
func start(s *SessionTurns, in TurnInput, d TurnDirection, n uint64) error {
	return transitionErr(s.StartTurn(in, d, n))
}
func end(s *SessionTurns, message messages.Message, n uint64) error {
	return transitionErr(s.EndTurn(0, "", message, n))
}
func TestSessionTurns_SerializesBlockedEventPublication(t *testing.T) {
	events := []TurnEvent{}
	startEntered, releaseStart := make(chan struct{}), make(chan struct{})
	s := NewSessionTurns(SessionTurnsOptions{EventSink: func(event TurnEvent) {
		if event.Type == TurnEventStart {
			close(startEntered)
			<-releaseStart
		}
		events = append(events, event)
	}})
	done := make(chan struct{}, 2)
	go func() { _, _ = s.StartTurn(NewTextTurnInput("input"), TurnDirectionUser, 1); done <- struct{}{} }()
	<-startEntered
	response := messages.NewTextMessage(messages.RoleAssistant, "response")
	go func() { _ = end(s, response, 2); done <- struct{}{} }()
	close(releaseStart)
	<-done
	<-done
	if len(events) != 2 || events[0].Type != TurnEventStart || events[1].Type != TurnEventEnd {
		t.Fatalf("events = %#v, want start then end", events)
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
		s.respond(value.Content)
	case *messages.AudioDeltaValue:
		s.respond("")
	}
	return true
}
func (s *turnTestSession) respond(input string) {
	if s.failNext != nil {
		s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(s.failNext)})
		s.failNext = nil
		return
	}
	response := "acknowledged"
	if strings.HasPrefix(input, "fact:") {
		s.fact = input
	} else if s.fact != "" && strings.Contains(input, "recall") {
		response = "I remember " + s.fact
	}
	s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(response)})
	s.recv.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}
