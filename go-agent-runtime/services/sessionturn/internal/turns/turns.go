// Package service contains the private session-turn state machine.
package turns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// Dependencies are the explicit provider and publication edges for one
// session-turn service. The service never discovers host state implicitly.
type Dependencies struct {
	SessionInferencer messages.SessionInferencer
	EventSink         sessionturn.TurnEventSink
}

// Service owns session reuse, turn validation, response assembly, and close
// state. Its fields are intentionally private to this package.
type Service struct {
	transitionMu sync.Mutex
	connectionMu sync.Mutex
	mu           sync.RWMutex

	inferencer messages.SessionInferencer
	connection messages.Session
	sink       sessionturn.TurnEventSink

	nextIndex uint64
	history   []sessionturn.SessionTurn
	active    *sessionturn.SessionTurn
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
func (s *Service) StartTurn(input sessionturn.TurnInput, direction sessionturn.TurnDirection, tick uint64) (turn sessionturn.SessionTurn, err error) {
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
		return sessionturn.SessionTurn{}, transitionError("start", sessionturn.ErrSessionClosed)
	case s.active != nil:
		return sessionturn.SessionTurn{}, transitionError("start", sessionturn.ErrTurnAlreadyActive)
	case input.Empty():
		return sessionturn.SessionTurn{}, transitionError("start", sessionturn.ErrEmptyTurn)
	case !validDirection(direction):
		return sessionturn.SessionTurn{}, transitionError("start", sessionturn.ErrInvalidTurnDirection)
	case len(s.history) > 0 && tick <= s.history[len(s.history)-1].EndTick:
		return sessionturn.SessionTurn{}, transitionError("start", sessionturn.ErrInvalidTurnTick)
	}

	turn = sessionturn.SessionTurn{
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
func (s *Service) EndTurn(index uint64, direction sessionturn.TurnDirection, response messages.Message, tick uint64) (turn sessionturn.SessionTurn, err error) {
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
		return sessionturn.SessionTurn{}, transitionError("end", sessionturn.ErrSessionClosed)
	}
	if s.active == nil {
		return sessionturn.SessionTurn{}, transitionError("end", sessionturn.ErrTurnEndWithoutStart)
	}
	active := *s.active
	switch {
	case (index != 0 && index != active.Index) || (direction != "" && direction != active.Direction):
		return sessionturn.SessionTurn{}, transitionError("end", sessionturn.ErrTurnMismatch)
	case !hasResponseContent(response):
		return sessionturn.SessionTurn{}, transitionError("end", sessionturn.ErrEmptyTurn)
	case tick <= active.StartTick:
		return sessionturn.SessionTurn{}, transitionError("end", sessionturn.ErrInvalidTurnTick)
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
func (s *Service) RunTurn(ctx context.Context, input sessionturn.TurnInput, direction sessionturn.TurnDirection, startTick, endTick uint64) (turn sessionturn.SessionTurn, err error) {
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	defer func() {
		if err != nil {
			s.abort(started.Index)
		}
	}()

	connection, err := s.sessionFor(ctx)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	if err = sendTurnInput(ctx, connection, started.Input); err != nil {
		return sessionturn.SessionTurn{}, err
	}
	response, err := readTurnResponse(ctx, connection)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	return s.EndTurn(started.Index, started.Direction, response, endTick)
}

// History returns a deep-copy snapshot of completed turns.
func (s *Service) History() []sessionturn.SessionTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]sessionturn.SessionTurn, len(s.history))
	for i := range s.history {
		out[i] = cloneTurn(&s.history[i])
	}
	return out
}

// ActiveTurn returns a deep-copy snapshot of the active turn, when present.
func (s *Service) ActiveTurn() (sessionturn.SessionTurn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return sessionturn.SessionTurn{}, false
	}
	return cloneTurn(s.active), true
}

// NextTurnIndex returns the next index that a successful turn will receive.
func (s *Service) NextTurnIndex() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextIndex
}

// Close marks the service closed, retires any active turn, and closes its
// reused provider session once. The bounded provider close lets an in-flight
// owner observe terminal shutdown without leaving the service reopenable.
func (s *Service) Close() error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	s.connectionMu.Lock()
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		s.connectionMu.Unlock()
		return err
	}
	s.closed = true
	// Closing is terminal ownership of the active turn. The provider session
	// is closed below, which wakes any reader and lets RunTurn observe the
	// bounded shutdown error instead of leaving the service permanently open.
	s.active = nil
	connection := s.connection
	s.mu.Unlock()
	s.connectionMu.Unlock()

	if connection == nil {
		return nil
	}
	err := closeConnectionBounded(connection)
	s.mu.Lock()
	s.closeErr = err
	s.mu.Unlock()
	return err
}

const providerCloseTimeout = 500 * time.Millisecond

func closeConnectionBounded(connection messages.Session) error {
	if connection == nil {
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- connection.Close() }()
	select {
	case err := <-result:
		return err
	case <-time.After(providerCloseTimeout):
		return fmt.Errorf("close turn session: %w", context.DeadlineExceeded)
	}
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
		return nil, transitionError("run", sessionturn.ErrSessionClosed)
	}
	if s.connection != nil {
		connection := s.connection
		s.mu.RUnlock()
		return connection, nil
	}
	inferencer := s.inferencer
	s.mu.RUnlock()
	if inferencer == nil {
		return nil, transitionError("run", sessionturn.ErrMissingTurnInferencer)
	}
	connection, err := inferencer.ConnectSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect turn session: %w", err)
	}
	if connection == nil {
		return nil, fmt.Errorf("connect turn session: %w", sessionturn.ErrMissingTurnSession)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closedErr := transitionError("run", sessionturn.ErrSessionClosed)
		if closeErr := connection.Close(); closeErr != nil {
			return nil, errors.Join(closedErr, closeErr)
		}
		return nil, closedErr
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

func validDirection(direction sessionturn.TurnDirection) bool {
	switch direction {
	case sessionturn.TurnDirectionUser, sessionturn.TurnDirectionAssistant,
		sessionturn.TurnDirectionClientToServer, sessionturn.TurnDirectionServerToClient:
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

func eventForStart(turn sessionturn.SessionTurn) sessionturn.TurnEvent {
	return sessionturn.TurnEvent{Type: sessionturn.TurnEventStart, Index: turn.Index, Direction: turn.Direction, Tick: turn.StartTick, StartTick: turn.StartTick}
}

func eventForEnd(turn sessionturn.SessionTurn) sessionturn.TurnEvent {
	return sessionturn.TurnEvent{Type: sessionturn.TurnEventEnd, Index: turn.Index, Direction: turn.Direction, Tick: turn.EndTick, StartTick: turn.StartTick, EndTick: turn.EndTick}
}
