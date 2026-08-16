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
	Type      TurnEventType
	Index     uint64
	Direction TurnDirection
	Tick      uint64
	StartTick uint64
	EndTick   uint64
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

func (in TurnInput) Empty() bool {
	return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0
}

func (in TurnInput) message(role messages.Role) messages.Message {
	parts := make([]messages.ContentPart, 0, 2)
	if strings.TrimSpace(in.Text) != "" {
		parts = append(parts, messages.TextPart{Text: in.Text})
	}
	if len(in.Audio) != 0 {
		parts = append(parts, messages.AudioPart{Bytes: append([]byte(nil), in.Audio...), MediaType: in.MediaType})
	}
	return messages.Message{Role: role, ContentParts: parts}
}

type SessionTurn struct {
	Index     uint64
	Direction TurnDirection
	Input     TurnInput
	Response  messages.Message
	StartTick uint64
	EndTick   uint64
}

type SessionTurnsOptions struct {
	Inferencer messages.Inferencer
	EventSink  TurnEventSink
}

type SessionTurns struct {
	mu         sync.Mutex
	inferencer messages.Inferencer
	sink       TurnEventSink
	nextIndex  uint64
	history    []SessionTurn
	active     *SessionTurn
	lastTick   uint64
	hasTick    bool
	closed     bool
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

func transitionError(op string, cause error, detail string) error {
	return fmt.Errorf("%s turn: %w (%s)", op, cause, detail)
}

func NewSessionTurns(opts SessionTurnsOptions) *SessionTurns {
	return &SessionTurns{inferencer: opts.Inferencer, sink: opts.EventSink, nextIndex: 1}
}

func (s *SessionTurns) StartTurn(input TurnInput, direction TurnDirection, startTick uint64) (SessionTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SessionTurn{}, transitionError("start", ErrSessionClosed, "session has already ended")
	}
	if s.active != nil {
		return SessionTurn{}, transitionError("start", ErrTurnAlreadyActive, "complete the active turn first")
	}
	if input.Empty() {
		return SessionTurn{}, transitionError("start", ErrEmptyTurn, "input must contain text or audio")
	}
	if !validTurnDirection(direction) {
		return SessionTurn{}, transitionError("start", ErrInvalidTurnDirection, fmt.Sprintf("direction=%q", direction))
	}
	if s.hasTick && startTick <= s.lastTick {
		return SessionTurn{}, transitionError("start", ErrInvalidTurnTick, fmt.Sprintf("previous=%d", s.lastTick))
	}
	turn := SessionTurn{Index: s.nextIndex, Direction: direction, Input: cloneInput(input), StartTick: startTick}
	s.active, s.lastTick, s.hasTick = &turn, startTick, true
	s.emit(TurnEvent{Type: TurnEventStart, Index: turn.Index, Direction: direction, Tick: startTick, StartTick: startTick})
	return cloneTurn(turn), nil
}

func (s *SessionTurns) StartTextTurn(text string, direction TurnDirection, tick uint64) (SessionTurn, error) {
	return s.StartTurn(NewTextTurnInput(text), direction, tick)
}

func (s *SessionTurns) StartAudioTurn(audio []byte, mediaType string, direction TurnDirection, tick uint64) (SessionTurn, error) {
	return s.StartTurn(NewAudioTurnInput(audio, mediaType), direction, tick)
}

func (s *SessionTurns) EndTurn(index uint64, direction TurnDirection, response messages.Message, endTick uint64) (SessionTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SessionTurn{}, transitionError("end", ErrSessionClosed, "session has already ended")
	}
	if s.active == nil {
		return SessionTurn{}, transitionError("end", ErrTurnEndWithoutStart, "start a turn before ending it")
	}
	active := *s.active
	if index != 0 && index != active.Index {
		return SessionTurn{}, transitionError("end", ErrTurnMismatch, fmt.Sprintf("active index=%d", active.Index))
	}
	if direction != "" && direction != active.Direction {
		return SessionTurn{}, transitionError("end", ErrTurnMismatch, fmt.Sprintf("active direction=%q", active.Direction))
	}
	if !messageHasContent(response) {
		return SessionTurn{}, transitionError("end", ErrEmptyTurn, "response must contain content")
	}
	if endTick <= s.lastTick {
		return SessionTurn{}, transitionError("end", ErrInvalidTurnTick, fmt.Sprintf("start=%d", active.StartTick))
	}
	active.Response = cloneMessage(response)
	if active.Response.Role == "" {
		active.Response.Role = messages.RoleAssistant
	}
	active.EndTick = endTick
	s.history = append(s.history, cloneTurn(active))
	s.nextIndex++
	s.active, s.lastTick = nil, endTick
	s.emit(TurnEvent{Type: TurnEventEnd, Index: active.Index, Direction: active.Direction, Tick: endTick, StartTick: active.StartTick, EndTick: endTick})
	return cloneTurn(active), nil
}

func (s *SessionTurns) CompleteTurn(response messages.Message, endTick uint64) (SessionTurn, error) {
	return s.EndTurn(0, "", response, endTick)
}

func (s *SessionTurns) RunTurn(ctx context.Context, input TurnInput, direction TurnDirection, startTick, endTick uint64) (SessionTurn, error) {
	s.mu.Lock()
	inferencer := s.inferencer
	s.mu.Unlock()
	if inferencer == nil {
		return SessionTurn{}, transitionError("run", ErrMissingTurnInferencer, "configure SessionTurnsOptions.Inferencer")
	}
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return SessionTurn{}, err
	}
	s.mu.Lock()
	requestMessages := append(s.historyMessagesLocked(), started.Input.message(messages.RoleUser))
	s.mu.Unlock()
	result, err := inferencer.Infer(ctx, messages.InferenceRequest{Messages: requestMessages})
	if err != nil {
		return SessionTurn{}, err
	}
	return s.EndTurn(started.Index, started.Direction, result.Message, endTick)
}

func (s *SessionTurns) History() []SessionTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := make([]SessionTurn, len(s.history))
	for i, turn := range s.history {
		history[i] = cloneTurn(turn)
	}
	return history
}

func (s *SessionTurns) NextTurnIndex() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextIndex
}

func (s *SessionTurns) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return transitionError("close", ErrSessionEndedWithActiveTurn, "complete the active turn first")
	}
	s.closed = true
	return nil
}

func (s *SessionTurns) emit(event TurnEvent) {
	if s.sink != nil {
		s.sink(event)
	}
}

func (s *SessionTurns) historyMessagesLocked() []messages.Message {
	result := make([]messages.Message, 0, len(s.history)*2)
	for _, turn := range s.history {
		result = append(result, turn.Input.message(messages.RoleUser), cloneMessage(turn.Response))
	}
	return result
}

func validTurnDirection(direction TurnDirection) bool {
	switch direction {
	case TurnDirectionUser, TurnDirectionAssistant, TurnDirectionClientToServer, TurnDirectionServerToClient:
		return true
	default:
		return false
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

func cloneTurn(turn SessionTurn) SessionTurn {
	turn.Input = cloneInput(turn.Input)
	turn.Response = cloneMessage(turn.Response)
	return turn
}

func cloneMessage(message messages.Message) messages.Message {
	message.ContentParts = append([]messages.ContentPart(nil), message.ContentParts...)
	message.ToolCalls = append([]messages.ToolCall(nil), message.ToolCalls...)
	for i, part := range message.ContentParts {
		if audio, ok := part.(messages.AudioPart); ok {
			audio.Bytes = append([]byte(nil), audio.Bytes...)
			message.ContentParts[i] = audio
		}
	}
	return message
}
