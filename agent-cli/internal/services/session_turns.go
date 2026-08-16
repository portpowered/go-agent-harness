package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type TurnDirection string

const (
	TurnDirectionUser           TurnDirection = "user"
	TurnDirectionAssistant      TurnDirection = "assistant"
	TurnDirectionClientToServer TurnDirection = "client_to_server"
	TurnDirectionServerToClient TurnDirection = "server_to_client"
)

type TurnEventType string

const (
	TurnEventStart TurnEventType = "turn-start"
	TurnEventEnd   TurnEventType = "turn-end"
)

type TurnEvent struct {
	Type                     TurnEventType
	Index                    uint64
	Direction                TurnDirection
	Tick, StartTick, EndTick uint64
}
type TurnEventSink func(TurnEvent)
type TurnInput struct {
	Text      string
	Audio     []byte
	MediaType string
}

func NewTextTurnInput(text string) TurnInput { return TurnInput{Text: text} }
func NewAudioTurnInput(audio []byte, mediaType string) TurnInput {
	return TurnInput{Audio: append([]byte(nil), audio...), MediaType: mediaType}
}
func (in TurnInput) Empty() bool { return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0 }

type SessionTurn struct {
	Index              uint64
	Direction          TurnDirection
	Input              TurnInput
	Response           messages.Message
	StartTick, EndTick uint64
}
type SessionTurnsOptions struct {
	SessionInferencer messages.SessionInferencer
	EventSink         TurnEventSink
}
type SessionTurns struct {
	mu                sync.Mutex
	sessionInferencer messages.SessionInferencer
	session           messages.Session
	sink              TurnEventSink
	nextIndex         uint64
	history           []SessionTurn
	active            *SessionTurn
	closed            bool
}

var (
	ErrTurnAlreadyActive          = errors.New("turn start while another turn is active")
	ErrTurnEndWithoutStart        = errors.New("turn end without start: no active turn")
	ErrEmptyTurn                  = errors.New("turn content must not be empty")
	ErrInvalidTurnDirection       = errors.New("turn direction is invalid")
	ErrInvalidTurnTick            = errors.New("turn tick must be strictly increasing")
	ErrSessionEndedWithActiveTurn = errors.New("session ended with active turn")
	ErrSessionClosed              = errors.New("session is closed")
	ErrTurnMismatch               = errors.New("turn does not match the active turn")
	ErrMissingTurnInferencer      = errors.New("session turn inferencer is not configured")
)

func transitionError(op string, cause error) error {
	return fmt.Errorf("%s turn: %w", op, cause)
}
func invalidTransition(op string, cause error) (SessionTurn, TurnEvent, error) {
	return SessionTurn{}, TurnEvent{}, transitionError(op, cause)
}
func NewSessionTurns(opts SessionTurnsOptions) *SessionTurns {
	return &SessionTurns{sessionInferencer: opts.SessionInferencer, sink: opts.EventSink, nextIndex: 1}
}

func (s *SessionTurns) StartTurn(input TurnInput, direction TurnDirection, tick uint64) (SessionTurn, error) {
	s.mu.Lock()
	turn, event, err := s.startLocked(input, direction, tick)
	if err != nil {
		return SessionTurn{}, err
	}
	if s.sink != nil {
		s.sink(event)
	}
	return cloneTurn(turn), nil
}

func (s *SessionTurns) startLocked(input TurnInput, direction TurnDirection, tick uint64) (SessionTurn, TurnEvent, error) {
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return invalidTransition("start", ErrSessionClosed)
	case s.active != nil:
		return invalidTransition("start", ErrTurnAlreadyActive)
	case input.Empty():
		return invalidTransition("start", ErrEmptyTurn)
	case direction != TurnDirectionUser && direction != TurnDirectionAssistant && direction != TurnDirectionClientToServer && direction != TurnDirectionServerToClient:
		return invalidTransition("start", ErrInvalidTurnDirection)
	case len(s.history) != 0 && tick <= s.history[len(s.history)-1].EndTick:
		return invalidTransition("start", ErrInvalidTurnTick)
	}
	turn := SessionTurn{Index: s.nextIndex, Direction: direction, Input: cloneInput(input), StartTick: tick}
	s.active = &turn
	return turn, TurnEvent{Type: TurnEventStart, Index: turn.Index, Direction: direction, Tick: tick, StartTick: tick}, nil
}
func (s *SessionTurns) StartTextTurn(text string, direction TurnDirection, tick uint64) (SessionTurn, error) {
	return s.StartTurn(NewTextTurnInput(text), direction, tick)
}
func (s *SessionTurns) StartAudioTurn(audio []byte, mediaType string, direction TurnDirection, tick uint64) (SessionTurn, error) {
	return s.StartTurn(NewAudioTurnInput(audio, mediaType), direction, tick)
}

func (s *SessionTurns) EndTurn(index uint64, direction TurnDirection, response messages.Message, tick uint64) (SessionTurn, error) {
	s.mu.Lock()
	active, event, err := s.endLocked(index, direction, response, tick)
	if err != nil {
		return SessionTurn{}, err
	}
	if s.sink != nil {
		s.sink(event)
	}
	return cloneTurn(active), nil
}

func (s *SessionTurns) endLocked(index uint64, direction TurnDirection, response messages.Message, tick uint64) (SessionTurn, TurnEvent, error) {
	defer s.mu.Unlock()
	if s.closed {
		return invalidTransition("end", ErrSessionClosed)
	}
	if s.active == nil {
		return invalidTransition("end", ErrTurnEndWithoutStart)
	}
	active := *s.active
	switch {
	case index != 0 && index != active.Index:
		return invalidTransition("end", ErrTurnMismatch)
	case direction != "" && direction != active.Direction:
		return invalidTransition("end", ErrTurnMismatch)
	case !messageHasContent(response):
		return invalidTransition("end", ErrEmptyTurn)
	case tick <= active.StartTick:
		return invalidTransition("end", ErrInvalidTurnTick)
	}
	active.Response, active.EndTick = response, tick
	if active.Response.Role == "" {
		active.Response.Role = messages.RoleAssistant
	}
	s.history, s.nextIndex = append(s.history, cloneTurn(active)), s.nextIndex+1
	s.active = nil
	return active, TurnEvent{Type: TurnEventEnd, Index: active.Index, Direction: active.Direction, Tick: tick, StartTick: active.StartTick, EndTick: tick}, nil
}
func (s *SessionTurns) CompleteTurn(response messages.Message, tick uint64) (SessionTurn, error) {
	return s.EndTurn(0, "", response, tick)
}

func (s *SessionTurns) RunTurn(ctx context.Context, input TurnInput, direction TurnDirection, startTick, endTick uint64) (SessionTurn, error) {
	s.mu.Lock()
	inferencer := s.sessionInferencer
	s.mu.Unlock()
	if inferencer == nil {
		return SessionTurn{}, transitionError("run", ErrMissingTurnInferencer)
	}
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return SessionTurn{}, err
	}
	session, err := s.sessionFor(ctx, inferencer)
	if err != nil {
		s.abort(started.Index)
		return SessionTurn{}, err
	}
	if err = sendTurnInput(ctx, session, input); err != nil {
		s.abort(started.Index)
		return SessionTurn{}, err
	}
	response, err := readTurnResponse(ctx, session)
	if err != nil {
		s.abort(started.Index)
		return SessionTurn{}, err
	}
	completed, err := s.EndTurn(started.Index, started.Direction, response, endTick)
	if err != nil {
		s.abort(started.Index)
		return SessionTurn{}, err
	}
	return completed, nil
}

func (s *SessionTurns) History() []SessionTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionTurn, len(s.history))
	for i, turn := range s.history {
		out[i] = cloneTurn(turn)
	}
	return out
}
func (s *SessionTurns) NextTurnIndex() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.nextIndex }
func (s *SessionTurns) Close() error {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return transitionError("close", ErrSessionEndedWithActiveTurn)
	}
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	session := s.session
	s.session = nil
	s.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

func (s *SessionTurns) sessionFor(ctx context.Context, inferencer messages.SessionInferencer) (messages.Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, transitionError("connect", ErrSessionClosed)
	}
	if s.session != nil {
		session := s.session
		s.mu.Unlock()
		return session, nil
	}
	s.mu.Unlock()
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect turn session: %w", err)
	}
	if session == nil {
		return nil, errors.New("connect turn session: inferencer returned a nil session")
	}
	s.mu.Lock()
	s.session = session
	s.mu.Unlock()
	return session, nil
}
func (s *SessionTurns) abort(index uint64) {
	s.mu.Lock()
	if s.active != nil && (index == 0 || index == s.active.Index) {
		s.active = nil
	}
	s.mu.Unlock()
}

func sendSessionMessage(ctx context.Context, session messages.Session, msg messages.StreamMessage, action string) error {
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() {
		return nil
	}
	if outcome.Err != nil {
		return fmt.Errorf("%s turn input: %w", action, outcome.Err)
	}
	return fmt.Errorf("%s turn input: session send %s", action, outcome.Status)
}

func sendTurnInput(ctx context.Context, session messages.Session, input TurnInput) error {
	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(input.Text)}
	if len(input.Audio) != 0 {
		msg = messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValueWithMediaType(input.Audio, input.MediaType)}
	}
	if err := sendSessionMessage(ctx, session, msg, "send"); err != nil {
		return err
	}
	if len(input.Audio) == 0 {
		return nil
	}
	commit := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	return sendSessionMessage(ctx, session, commit, "commit")
}

func readTurnResponse(ctx context.Context, session messages.Session) (messages.Message, error) {
	buffer := session.Receive()
	if buffer == nil {
		return messages.Message{}, errors.New("read turn response: session receive buffer is nil")
	}
	var deltas []messages.StreamMessage
	for {
		var msg messages.StreamMessage
		select {
		case msg = <-buffer.Chan():
		case <-ctx.Done():
			return messages.Message{}, ctx.Err()
		case <-session.Done():
			return messages.Message{}, transitionError("read", ErrSessionClosed)
		}
		switch msg.Type {
		case messages.StreamTypeSessionOpen, messages.StreamTypeSessionCreated, messages.StreamTypeSessionUpdated, messages.StreamTypeUsageInfo, messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped:
			continue
		case messages.StreamTypeError:
			value, _ := msg.Value.(*messages.ErrorValue)
			if value != nil && value.Err != nil {
				return messages.Message{}, value.Err
			}
			if value == nil || strings.TrimSpace(value.Message) == "" {
				return messages.Message{}, errors.New("session returned an error")
			}
			return messages.Message{}, errors.New(value.Message)
		case messages.StreamTypeSessionClose:
			return messages.Message{}, transitionError("read", ErrSessionClosed)
		case messages.StreamTypeMessageEnd:
			deltas = append(deltas, msg)
			response := messages.ReconstructModelMessageFromDeltas(deltas)
			if !messageHasContent(response) {
				return messages.Message{}, transitionError("end", ErrEmptyTurn)
			}
			return response, nil
		default:
			deltas = append(deltas, msg)
		}
	}
}

func messageHasContent(message messages.Message) bool {
	if strings.TrimSpace(message.TextContent()) != "" || message.Refusal != "" || len(message.ToolCalls) != 0 {
		return true
	}
	for _, part := range message.ContentParts {
		switch value := part.(type) {
		case messages.TextPart:
			if strings.TrimSpace(value.Text) != "" {
				return true
			}
		case messages.AudioPart:
			if len(value.Bytes) != 0 || value.URL != "" {
				return true
			}
		default:
			if part != nil {
				return true
			}
		}
	}
	return false
}
func cloneInput(input TurnInput) TurnInput {
	input.Audio = append([]byte(nil), input.Audio...)
	return input
}
func cloneTurn(turn SessionTurn) SessionTurn { turn.Input = cloneInput(turn.Input); return turn }
