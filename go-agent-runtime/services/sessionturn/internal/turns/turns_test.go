package turns

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	turnFact      = "fact: the marigold key is hidden under stone seven"
	pcmMediaType  = "audio/pcm"
	okResponse    = "ok"
	inputText     = "input"
	recvCapacity  = 32
	fiveTurns     = 5
	allTurnEvents = 10
)

func textInput(text string) sessionturn.TurnInput { return sessionturn.TurnInput{Text: text} }

func audioInput(audio []byte) sessionturn.TurnInput {
	return sessionturn.TurnInput{Audio: audio, MediaType: pcmMediaType}
}

func TestFiveTurnsUseOnePersistentSessionAndExactLifecycle(t *testing.T) {
	inferencer := newTurnTestSession()
	var session *Session
	var events []sessionturn.TurnEvent
	session = New(sessionturn.TurnsOptions{SessionInferencer: inferencer, EventSink: func(event sessionturn.TurnEvent) {
		events = append(events, event)
		_ = session.History()
	}})
	failure := errors.New("provider response failed")
	inferencer.failNext = failure
	if _, err := session.RunTurn(context.Background(), textInput("failed attempt"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, failure) || len(events) != 1 || len(session.History()) != 0 || session.NextTurnIndex() != 1 || session.Active() {
		t.Fatalf("failed turn error = %v", err)
	}
	events = nil
	inputs := []sessionturn.TurnInput{textInput(turnFact), audioInput([]byte{1, 2}), audioInput([]byte{3, 4}), textInput("Please recall the fact."), textInput("End the scripted conversation.")}
	for i, input := range inputs {
		turn, err := session.RunTurn(context.Background(), input, sessionturn.TurnDirectionUser, uint64(2*i+1), uint64(2*i+2))
		if err != nil || turn.Index != uint64(i+1) || turn.Response.TextContent() == "" {
			t.Fatalf("turn %d = %#v, err=%v", i+1, turn, err)
		}
	}
	assertFiveTurnHistory(t, session, inferencer, events)
	if err := session.Close(); err != nil || session.NextTurnIndex() != fiveTurns+1 || len(session.History()) != fiveTurns || !inferencer.closed() {
		t.Fatalf("clean close = %v", err)
	}
}

func assertFiveTurnHistory(t *testing.T, session *Session, inferencer *turnTestSession, events []sessionturn.TurnEvent) {
	t.Helper()
	history := session.History()
	if inferencer.connects != 1 || len(history) != fiveTurns || len(events) != allTurnEvents || session.NextTurnIndex() != fiveTurns+1 {
		t.Fatalf("connection/history/events/next = %d/%d/%d/%d", inferencer.connects, len(history), len(events), session.NextTurnIndex())
	}
	if !strings.Contains(history[3].Response.TextContent(), "marigold key") || string(history[1].Input.Audio) != string([]byte{1, 2}) || string(history[2].Input.Audio) != string([]byte{3, 4}) {
		t.Fatalf("recall/audio = %q/%v/%v", history[3].Response.TextContent(), history[1].Input.Audio, history[2].Input.Audio)
	}
	assertAlternatingEvents(t, events)
}

func assertAlternatingEvents(t *testing.T, events []sessionturn.TurnEvent) {
	t.Helper()
	for i, event := range events {
		tick, start := uint64(i+1), i%2 == 0
		startValid := start && event.Type == sessionturn.TurnEventStart && event.EndTick == 0
		endValid := !start && event.Type == sessionturn.TurnEventEnd && event.EndTick == tick
		valid := event.Index == uint64(i/2+1) && event.Direction == sessionturn.TurnDirectionUser && event.Tick == tick && event.StartTick == tick-uint64(i%2)
		if !valid || (!startValid && !endValid) {
			t.Fatalf("event %d invalid: %#v", i, event)
		}
	}
}

type transitionCase struct {
	name                  string
	setup                 func(*Session) error
	try                   func(*Session) error
	want                  error
	events, history, next int
	active                bool
}

func noSetup(*Session) error { return nil }

func start(s *Session, in sessionturn.TurnInput, d sessionturn.TurnDirection, n uint64) error {
	_, err := s.StartTurn(in, d, n)
	return err
}

func end(s *Session, message messages.Message, n uint64) error {
	_, err := s.EndTurn(0, "", message, n)
	return err
}

func okMessage() messages.Message { return messages.NewTextMessage(messages.RoleAssistant, okResponse) }

func startOne(s *Session) error {
	return start(s, textInput(inputText), sessionturn.TurnDirectionUser, 1)
}

func transitionCases() []transitionCase {
	return []transitionCase{
		{"overlap", startOne, func(s *Session) error { return start(s, textInput("two"), sessionturn.TurnDirectionUser, 2) }, sessionturn.ErrTurnAlreadyActive, 1, 0, 1, true},
		{"end without start", noSetup, func(s *Session) error { return end(s, okMessage(), 1) }, sessionturn.ErrTurnEndWithoutStart, 0, 0, 1, false},
		{"empty text", noSetup, func(s *Session) error { return start(s, textInput(" "), sessionturn.TurnDirectionUser, 1) }, sessionturn.ErrEmptyTurn, 0, 0, 1, false},
		{"zero audio", noSetup, func(s *Session) error { return start(s, audioInput(nil), sessionturn.TurnDirectionUser, 1) }, sessionturn.ErrEmptyTurn, 0, 0, 1, false},
		{"bad direction", noSetup, func(s *Session) error { return start(s, textInput(inputText), "sideways", 1) }, sessionturn.ErrInvalidTurnDirection, 0, 0, 1, false},
		{"non increasing tick", func(s *Session) error {
			return errors.Join(start(s, textInput("one"), sessionturn.TurnDirectionUser, 3), end(s, okMessage(), 4))
		}, func(s *Session) error { return start(s, textInput("two"), sessionturn.TurnDirectionUser, 4) }, sessionturn.ErrInvalidTurnTick, 2, 1, 2, false},
		{"invalid end tick", func(s *Session) error { return start(s, textInput(inputText), sessionturn.TurnDirectionUser, 10) },
			func(s *Session) error { return end(s, okMessage(), 10) }, sessionturn.ErrInvalidTurnTick, 1, 0, 1, true},
		{"empty response text part", startOne, func(s *Session) error {
			return end(s, messages.Message{ContentParts: []messages.ContentPart{messages.TextPart{Text: " "}}}, 2)
		}, sessionturn.ErrEmptyTurn, 1, 0, 1, true},
		{"empty response audio part", startOne, func(s *Session) error {
			return end(s, messages.Message{ContentParts: []messages.ContentPart{messages.AudioPart{}}}, 2)
		}, sessionturn.ErrEmptyTurn, 1, 0, 1, true},
		{"mismatched end", startOne, func(s *Session) error {
			_, err := s.EndTurn(fiveTurns, "", okMessage(), 2)
			return err
		}, sessionturn.ErrTurnMismatch, 1, 0, 1, true},
		{"close active", startOne, (*Session).Close, sessionturn.ErrSessionEndedWithActiveTurn, 1, 0, 1, true},
	}
}

func TestInvalidTransitionsKeepStateAndEvents(t *testing.T) {
	for _, tc := range transitionCases() {
		t.Run(tc.name, func(t *testing.T) {
			var events []sessionturn.TurnEvent
			session := New(sessionturn.TurnsOptions{EventSink: func(event sessionturn.TurnEvent) { events = append(events, event) }})
			if err := tc.setup(session); err != nil {
				t.Fatalf("setup: %v", err)
			}
			err := tc.try(session)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if len(events) != tc.events || len(session.History()) != tc.history || int(session.NextTurnIndex()) != tc.next || session.Active() != tc.active {
				t.Fatalf("state events/history/next/active = %d/%d/%d/%v", len(events), len(session.History()), session.NextTurnIndex(), session.Active())
			}
		})
	}
}

func TestClosedSessionRejectsTransitionsAndRuns(t *testing.T) {
	session := New(sessionturn.TurnsOptions{})
	if err := session.Close(); err != nil {
		t.Fatalf("close idle: %v", err)
	}
	if err := start(session, textInput(inputText), sessionturn.TurnDirectionUser, 1); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("start after close = %v", err)
	}
	if err := end(session, okMessage(), 1); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("end after close = %v", err)
	}
}

func TestRunTurnWithoutInferencerDiscardsTurn(t *testing.T) {
	session := New(sessionturn.TurnsOptions{})
	if _, err := session.RunTurn(context.Background(), textInput(inputText), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrMissingTurnInferencer) || session.Active() {
		t.Fatalf("run without inferencer = %v", err)
	}
	if _, err := session.RunTurn(context.Background(), textInput(" "), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrEmptyTurn) {
		t.Fatalf("empty run = %v", err)
	}
}

func TestSerializesBlockedEventPublication(t *testing.T) {
	var events []sessionturn.TurnEvent
	startEntered, releaseStart := make(chan struct{}), make(chan struct{})
	s := New(sessionturn.TurnsOptions{EventSink: func(event sessionturn.TurnEvent) {
		if event.Type == sessionturn.TurnEventStart {
			close(startEntered)
			<-releaseStart
		}
		events = append(events, event)
	}})
	done := make(chan error, 2)
	go func() { done <- start(s, textInput(inputText), sessionturn.TurnDirectionUser, 1) }()
	<-startEntered
	go func() { done <- end(s, messages.NewTextMessage(messages.RoleAssistant, "response"), 2) }()
	close(releaseStart)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	if len(events) != 2 || events[0].Type != sessionturn.TurnEventStart || events[1].Type != sessionturn.TurnEventEnd {
		t.Fatalf("events = %#v, want start then end", events)
	}
}

type turnTestSession struct {
	recv     *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	failNext error
	fact     string
	connects int
}

func newTurnTestSession() *turnTestSession {
	return &turnTestSession{recv: messages.NewTypedBuffer[messages.StreamMessage](recvCapacity), done: make(chan struct{})}
}

func (s *turnTestSession) ConnectSession(context.Context) (messages.Session, error) {
	s.connects++
	return s, nil
}

func (s *turnTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		s.respond(ctx, value.Content)
	case *messages.AudioDeltaValue:
		s.respond(ctx, "")
	}
	return true
}

func (s *turnTestSession) respond(ctx context.Context, input string) {
	if s.failNext != nil {
		s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(s.failNext)})
		s.failNext = nil
		return
	}
	response := "acknowledged"
	if strings.HasPrefix(input, "fact:") {
		s.fact = input
	} else if s.fact != "" && strings.Contains(input, "recall") {
		response = "I remember " + s.fact
	}
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(response)})
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func (s *turnTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *turnTestSession) Done() <-chan struct{}                                  { return s.done }
func (s *turnTestSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *turnTestSession) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}
