// Package service contains the private session-turn state machine.
package service

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
)

var _ sessionturns.Service = (*Service)(nil)

// Dependencies are the explicit provider and publication edges for one
// session-turn service. The service never discovers host state implicitly.
type Dependencies struct {
	SessionInferencer messages.SessionInferencer
	EventSink         sessionturns.TurnEventSink
}

// Service owns session reuse, turn validation, response assembly, and close
// state. Its fields are intentionally private to this package.
type Service struct {
	transitionMu sync.Mutex
	connectionMu sync.Mutex
	mu           sync.RWMutex

	inferencer messages.SessionInferencer
	connection messages.Session
	sink       sessionturns.TurnEventSink

	nextIndex uint64
	history   []sessionturns.SessionTurn
	active    *sessionturns.SessionTurn
	closed    bool
	closeErr  error
}

// New constructs an inert service. Provider connection is deferred until the
// first RunTurn call.
func New(deps Dependencies) *Service {
	return &Service{
		inferencer: deps.SessionInferencer,
		sink:       deps.EventSink,
		nextIndex:  1,
	}
}

// StartTurn admits and publishes the start edge of one turn.
func (s *Service) StartTurn(input sessionturns.TurnInput, direction sessionturns.TurnDirection, tick uint64) (turn sessionturns.SessionTurn, err error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if err == nil && s.sink != nil {
			s.sink(eventForStart(turn))
		}
	}()

	switch {
	case s.closed:
		return sessionturns.SessionTurn{}, transitionError("start", sessionturns.ErrSessionClosed)
	case s.active != nil:
		return sessionturns.SessionTurn{}, transitionError("start", sessionturns.ErrTurnAlreadyActive)
	case input.Empty():
		return sessionturns.SessionTurn{}, transitionError("start", sessionturns.ErrEmptyTurn)
	case !validDirection(direction):
		return sessionturns.SessionTurn{}, transitionError("start", sessionturns.ErrInvalidTurnDirection)
	case len(s.history) > 0 && tick <= s.history[len(s.history)-1].EndTick:
		return sessionturns.SessionTurn{}, transitionError("start", sessionturns.ErrInvalidTurnTick)
	}

	turn = sessionturns.SessionTurn{
		Index:     s.nextIndex,
		Direction: direction,
		Input:     cloneInput(input),
		StartTick: tick,
	}
	stored := cloneTurn(&turn)
	s.active = &stored
	return cloneTurn(&turn), nil
}

// EndTurn validates and publishes the end edge of the active turn.
func (s *Service) EndTurn(index uint64, direction sessionturns.TurnDirection, response messages.Message, tick uint64) (turn sessionturns.SessionTurn, err error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if err == nil && s.sink != nil {
			s.sink(eventForEnd(turn))
		}
	}()

	if s.closed {
		return sessionturns.SessionTurn{}, transitionError("end", sessionturns.ErrSessionClosed)
	}
	if s.active == nil {
		return sessionturns.SessionTurn{}, transitionError("end", sessionturns.ErrTurnEndWithoutStart)
	}
	active := *s.active
	switch {
	case (index != 0 && index != active.Index) || (direction != "" && direction != active.Direction):
		return sessionturns.SessionTurn{}, transitionError("end", sessionturns.ErrTurnMismatch)
	case !hasResponseContent(response):
		return sessionturns.SessionTurn{}, transitionError("end", sessionturns.ErrEmptyTurn)
	case tick <= active.StartTick:
		return sessionturns.SessionTurn{}, transitionError("end", sessionturns.ErrInvalidTurnTick)
	}

	active.Response = cloneMessage(response)
	active.EndTick = tick
	s.history = append(s.history, cloneTurn(&active))
	s.nextIndex++
	s.active = nil
	turn = active
	return cloneTurn(&turn), nil
}

// RunTurn admits input, reuses one provider session, waits for its terminal
// MESSAGE.END, and commits the resulting turn.
func (s *Service) RunTurn(ctx context.Context, input sessionturns.TurnInput, direction sessionturns.TurnDirection, startTick, endTick uint64) (turn sessionturns.SessionTurn, err error) {
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return sessionturns.SessionTurn{}, err
	}
	defer func() {
		if err != nil {
			s.abort(started.Index)
		}
	}()

	connection, err := s.sessionFor(ctx)
	if err != nil {
		return sessionturns.SessionTurn{}, err
	}
	if err = sendTurnInput(ctx, connection, started.Input); err != nil {
		return sessionturns.SessionTurn{}, err
	}
	response, err := readTurnResponse(ctx, connection)
	if err != nil {
		return sessionturns.SessionTurn{}, err
	}
	return s.EndTurn(started.Index, started.Direction, response, endTick)
}

// History returns a deep-copy snapshot of completed turns.
func (s *Service) History() []sessionturns.SessionTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]sessionturns.SessionTurn, len(s.history))
	for i := range s.history {
		out[i] = cloneTurn(&s.history[i])
	}
	return out
}

// ActiveTurn returns a deep-copy snapshot of the active turn, when present.
func (s *Service) ActiveTurn() (sessionturns.SessionTurn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return sessionturns.SessionTurn{}, false
	}
	return cloneTurn(s.active), true
}

// NextTurnIndex returns the next index that a successful turn will receive.
func (s *Service) NextTurnIndex() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextIndex
}

// Close marks the service closed and closes its reused provider session once.
// An active turn is left intact so its owner can observe and repair it.
func (s *Service) Close() error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	s.connectionMu.Lock()
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		s.connectionMu.Unlock()
		return transitionError("close", sessionturns.ErrSessionEndedWithActiveTurn)
	}
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		s.connectionMu.Unlock()
		return err
	}
	s.closed = true
	connection := s.connection
	s.mu.Unlock()
	s.connectionMu.Unlock()

	if connection == nil {
		return nil
	}
	err := connection.Close()
	s.mu.Lock()
	s.closeErr = err
	s.mu.Unlock()
	return err
}

func (s *Service) sessionFor(ctx context.Context) (messages.Session, error) {
	if ctx == nil {
		return nil, transitionError("run", context.Canceled)
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, transitionError("run", sessionturns.ErrSessionClosed)
	}
	if s.connection != nil {
		connection := s.connection
		s.mu.RUnlock()
		return connection, nil
	}
	inferencer := s.inferencer
	s.mu.RUnlock()
	if inferencer == nil {
		return nil, transitionError("run", sessionturns.ErrMissingTurnInferencer)
	}
	connection, err := inferencer.ConnectSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect turn session: %w", err)
	}
	if connection == nil {
		return nil, fmt.Errorf("connect turn session: %w", sessionturns.ErrMissingTurnSession)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = connection.Close()
		return nil, transitionError("run", sessionturns.ErrSessionClosed)
	}
	s.connection = connection
	s.mu.Unlock()
	return connection, nil
}

func (s *Service) abort(index uint64) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.Index == index {
		s.active = nil
	}
}

func transitionError(operation string, cause error) error {
	return fmt.Errorf("%s turn: %w", operation, cause)
}

func validDirection(direction sessionturns.TurnDirection) bool {
	switch direction {
	case sessionturns.TurnDirectionUser, sessionturns.TurnDirectionAssistant,
		sessionturns.TurnDirectionClientToServer, sessionturns.TurnDirectionServerToClient:
		return true
	default:
		return false
	}
}

func hasResponseContent(response messages.Message) bool {
	return strings.TrimSpace(response.TextContent()) != "" ||
		strings.TrimSpace(response.Refusal) != "" ||
		len(response.ToolCalls) > 0 ||
		containsAudio(response.ContentParts)
}

func containsAudio(parts []messages.ContentPart) bool {
	for _, part := range parts {
		audio, ok := part.(messages.AudioPart)
		if ok && (strings.TrimSpace(audio.URL) != "" || len(audio.Bytes) != 0) {
			return true
		}
		if audioPtr, ok := part.(*messages.AudioPart); ok && audioPtr != nil && (strings.TrimSpace(audioPtr.URL) != "" || len(audioPtr.Bytes) != 0) {
			return true
		}
	}
	return false
}

func eventForStart(turn sessionturns.SessionTurn) sessionturns.TurnEvent {
	return sessionturns.TurnEvent{Type: sessionturns.TurnEventStart, Index: turn.Index, Direction: turn.Direction, Tick: turn.StartTick, StartTick: turn.StartTick}
}

func eventForEnd(turn sessionturns.SessionTurn) sessionturns.TurnEvent {
	return sessionturns.TurnEvent{Type: sessionturns.TurnEventEnd, Index: turn.Index, Direction: turn.Direction, Tick: turn.EndTick, StartTick: turn.StartTick, EndTick: turn.EndTick}
}
