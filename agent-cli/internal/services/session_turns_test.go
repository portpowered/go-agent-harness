package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const turnFact = "fact: the marigold key is hidden under stone seven"

func TestSessionTurns_FiveTurnsUseOneHistoryAndExactLifecycle(t *testing.T) {
	inferencer := &turnTestInferencer{}
	events := make([]TurnEvent, 0, 10)
	session := NewSessionTurns(SessionTurnsOptions{Inferencer: inferencer, EventSink: func(e TurnEvent) { events = append(events, e) }})
	inputs := []TurnInput{
		NewTextTurnInput(turnFact),
		NewAudioTurnInput([]byte{1, 2}, "audio/pcm"),
		NewAudioTurnInput([]byte{3, 4}, "audio/pcm"),
		NewTextTurnInput("Please recall the fact."),
		NewTextTurnInput("End the scripted conversation."),
	}
	for i, input := range inputs {
		turn, err := session.RunTurn(context.Background(), input, TurnDirectionUser, uint64(i*2+1), uint64(i*2+2))
		if err != nil || turn.Index != uint64(i+1) || strings.TrimSpace(turn.Response.TextContent()) == "" {
			t.Fatalf("turn %d = %#v, err=%v; want indexed non-empty response", i+1, turn, err)
		}
	}
	if inferencer.calls != 5 || len(inferencer.requests) != 5 {
		t.Fatalf("inference calls/requests = %d/%d, want one path serving five turns", inferencer.calls, len(inferencer.requests))
	}
	for i, request := range inferencer.requests {
		if len(request.Messages) != 2*i+1 {
			t.Fatalf("request %d message count = %d, want completed history plus current input", i+1, len(request.Messages))
		}
	}
	if got := inferencer.responses[3].TextContent(); !strings.Contains(got, "marigold key") {
		t.Fatalf("turn 4 response = %q, want fact derived from retained history", got)
	}
	if len(events) != 10 {
		t.Fatalf("events = %d, want exactly five starts and five ends", len(events))
	}
	for i, event := range events {
		index := uint64(i/2 + 1)
		if event.Index != index || event.Direction != TurnDirectionUser {
			t.Fatalf("event %d = %#v, want index=%d user", i, event, index)
		}
		if i%2 == 0 {
			if event.Type != TurnEventStart || event.Tick != uint64(i+1) || event.StartTick != uint64(i+1) || event.EndTick != 0 {
				t.Fatalf("event %d = %#v, want start tick %d", i, event, i+1)
			}
		} else if event.Type != TurnEventEnd || event.Tick != uint64(i+1) || event.StartTick != uint64(i) || event.EndTick != uint64(i+1) {
			t.Fatalf("event %d = %#v, want end from %d to %d", i, event, i, i+1)
		}
	}
	history := session.History()
	if len(history) != 5 || session.NextTurnIndex() != 6 {
		t.Fatalf("history/next = %d/%d, want 5/6", len(history), session.NextTurnIndex())
	}
	if string(history[1].Input.Audio) != string([]byte{1, 2}) || string(history[2].Input.Audio) != string([]byte{3, 4}) {
		t.Fatalf("audio history order = %v, %v", history[1].Input.Audio, history[2].Input.Audio)
	}
}

func TestSessionTurns_InvalidTransitionsKeepStateAndEvents(t *testing.T) {
	cases := []struct {
		name                string
		setup               func(*SessionTurns)
		try                 func(*SessionTurns) error
		want                error
		events, count, next int
		active              bool
	}{
		{"overlap", func(s *SessionTurns) { _, _ = s.StartTextTurn("one", TurnDirectionUser, 1) }, func(s *SessionTurns) error { _, e := s.StartTextTurn("two", TurnDirectionUser, 2); return e }, ErrTurnAlreadyActive, 1, 0, 1, true},
		{"end without start", nil, func(s *SessionTurns) error {
			_, e := s.CompleteTurn(messages.NewTextMessage(messages.RoleAssistant, "ok"), 1)
			return e
		}, ErrTurnEndWithoutStart, 0, 0, 1, false},
		{"empty text", nil, func(s *SessionTurns) error { _, e := s.StartTextTurn(" ", TurnDirectionUser, 1); return e }, ErrEmptyTurn, 0, 0, 1, false},
		{"zero audio", nil, func(s *SessionTurns) error {
			_, e := s.StartAudioTurn(nil, "audio/pcm", TurnDirectionUser, 1)
			return e
		}, ErrEmptyTurn, 0, 0, 1, false},
		{"bad direction", nil, func(s *SessionTurns) error { _, e := s.StartTextTurn("input", TurnDirection("sideways"), 1); return e }, ErrInvalidTurnDirection, 0, 0, 1, false},
		{"non increasing tick", func(s *SessionTurns) {
			_, _ = s.StartTextTurn("one", TurnDirectionUser, 3)
			_, _ = s.CompleteTurn(messages.NewTextMessage(messages.RoleAssistant, "ok"), 4)
		}, func(s *SessionTurns) error { _, e := s.StartTextTurn("two", TurnDirectionUser, 4); return e }, ErrInvalidTurnTick, 2, 1, 2, false},
		{"empty response", func(s *SessionTurns) { _, _ = s.StartTextTurn("input", TurnDirectionUser, 1) }, func(s *SessionTurns) error {
			_, e := s.CompleteTurn(messages.NewTextMessage(messages.RoleAssistant, " "), 2)
			return e
		}, ErrEmptyTurn, 1, 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := make([]TurnEvent, 0, 2)
			session := NewSessionTurns(SessionTurnsOptions{EventSink: func(e TurnEvent) { events = append(events, e) }})
			if tc.setup != nil {
				tc.setup(session)
			}
			err := tc.try(session)
			if err == nil || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("error = %v, want errors.Is and message %q", err, tc.want)
			}
			if len(events) != tc.events || int(session.NextTurnIndex()) != tc.next || len(session.History()) != tc.count {
				t.Fatalf("state events/next/history = %d/%d/%d, want %d/%d/%d", len(events), session.NextTurnIndex(), len(session.History()), tc.events, tc.next, tc.count)
			}
			if (session.active != nil) != tc.active {
				t.Fatalf("active = %v, want %v", session.active != nil, tc.active)
			}
		})
	}
}

func TestSessionTurns_EndAndCloseRejectIncompleteTransitions(t *testing.T) {
	events := make([]TurnEvent, 0, 2)
	session := NewSessionTurns(SessionTurnsOptions{EventSink: func(e TurnEvent) { events = append(events, e) }})
	started, err := session.StartTextTurn("input", TurnDirectionUser, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.EndTurn(started.Index+1, started.Direction, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11); !errors.Is(err, ErrTurnMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	if _, err := session.EndTurn(started.Index, started.Direction, messages.NewTextMessage(messages.RoleAssistant, "ok"), 10); !errors.Is(err, ErrInvalidTurnTick) {
		t.Fatalf("tick = %v", err)
	}
	if err := session.Close(); err == nil || !errors.Is(err, ErrSessionEndedWithActiveTurn) {
		t.Fatalf("close = %v", err)
	}
	if len(events) != 1 || len(session.History()) != 0 {
		t.Fatalf("rejected transitions changed state")
	}
	if _, err := session.CompleteTurn(messages.NewTextMessage(messages.RoleAssistant, "ok"), 11); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartTextTurn("after close", TurnDirectionUser, 12); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("start after close = %v", err)
	}
}

type turnTestInferencer struct {
	calls     int
	requests  []messages.InferenceRequest
	responses []messages.Message
}

func (i *turnTestInferencer) Infer(_ context.Context, request messages.InferenceRequest) (messages.InferenceResult, error) {
	i.calls++
	i.requests = append(i.requests, request)
	response := "acknowledged"
	current := request.Messages[len(request.Messages)-1].TextContent()
	if strings.Contains(current, "recall") {
		for _, message := range request.Messages[:len(request.Messages)-1] {
			if strings.HasPrefix(message.TextContent(), "fact:") {
				response = "I remember " + message.TextContent()
				break
			}
		}
		if response == "acknowledged" {
			return messages.InferenceResult{}, errors.New("missing fact in history")
		}
	}
	message := messages.NewTextMessage(messages.RoleAssistant, response)
	i.responses = append(i.responses, message)
	return messages.InferenceResult{Message: message}, nil
}

func (*turnTestInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	stream := make(chan messages.StreamMessage)
	close(stream)
	return stream, nil
}
