// Package turns implements the persistent session turn state machine.
package turns

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	opStart = "start"
	opEnd   = "end"
	opRun   = "run"
	opClose = "close"
	opRead  = "read"
)

// Session is one persistent turn session. Transitions are serialized by
// transitionMu so event publication preserves commit order; mu guards state.
type Session struct {
	transitionMu sync.Mutex
	mu           sync.RWMutex
	inferencer   messages.SessionInferencer
	session      messages.Session
	sink         sessionturn.TurnEventSink
	nextIndex    uint64
	history      []sessionturn.SessionTurn
	active       *sessionturn.SessionTurn
	closed       bool
}

var _ sessionturn.Turns = (*Session)(nil)

// New creates an idle turn session whose first turn index is one.
func New(opts sessionturn.TurnsOptions) *Session {
	return &Session{inferencer: opts.SessionInferencer, sink: opts.EventSink, nextIndex: 1}
}

func transitionError(op string, cause error) error { return fmt.Errorf("%s turn: %w", op, cause) }

// StartTurn opens one turn after validating its input, direction, and tick.
func (s *Session) StartTurn(input sessionturn.TurnInput, direction sessionturn.TurnDirection, tick uint64) (turn sessionturn.SessionTurn, err error) {
	s.transitionMu.Lock()
	s.mu.Lock()
	var event sessionturn.TurnEvent
	defer func() { s.finishTransition(event, err) }()
	if cause := s.startRejection(input, direction, tick); cause != nil {
		return sessionturn.SessionTurn{}, transitionError(opStart, cause)
	}
	turn = sessionturn.SessionTurn{Index: s.nextIndex, Direction: direction, Input: copyInput(input), StartTick: tick}
	s.active = &turn
	event = sessionturn.TurnEvent{Type: sessionturn.TurnEventStart, Index: turn.Index, Direction: direction, Tick: tick, StartTick: tick}
	return cloneTurn(turn), nil
}

func (s *Session) startRejection(input sessionturn.TurnInput, direction sessionturn.TurnDirection, tick uint64) error {
	switch {
	case s.closed:
		return sessionturn.ErrSessionClosed
	case s.active != nil:
		return sessionturn.ErrTurnAlreadyActive
	case input.Empty():
		return sessionturn.ErrEmptyTurn
	case !validDirection(direction):
		return sessionturn.ErrInvalidTurnDirection
	case len(s.history) != 0 && tick <= s.history[len(s.history)-1].EndTick:
		return sessionturn.ErrInvalidTurnTick
	}
	return nil
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

// EndTurn closes the active turn with a non-empty response.
func (s *Session) EndTurn(index uint64, direction sessionturn.TurnDirection, response messages.Message, tick uint64) (turn sessionturn.SessionTurn, err error) {
	s.transitionMu.Lock()
	s.mu.Lock()
	var event sessionturn.TurnEvent
	defer func() { s.finishTransition(event, err) }()
	if s.closed {
		return sessionturn.SessionTurn{}, transitionError(opEnd, sessionturn.ErrSessionClosed)
	}
	if s.active == nil {
		return sessionturn.SessionTurn{}, transitionError(opEnd, sessionturn.ErrTurnEndWithoutStart)
	}
	active := *s.active
	if cause := endRejection(active, index, direction, response, tick); cause != nil {
		return sessionturn.SessionTurn{}, transitionError(opEnd, cause)
	}
	active.Response, active.EndTick = response, tick
	s.history, s.nextIndex = append(s.history, cloneTurn(active)), s.nextIndex+1
	s.active = nil
	event = sessionturn.TurnEvent{Type: sessionturn.TurnEventEnd, Index: active.Index, Direction: active.Direction, Tick: tick, StartTick: active.StartTick, EndTick: tick}
	return cloneTurn(active), nil
}

func endRejection(active sessionturn.SessionTurn, index uint64, direction sessionturn.TurnDirection, response messages.Message, tick uint64) error {
	switch {
	case (index != 0 && index != active.Index) || (direction != "" && direction != active.Direction):
		return sessionturn.ErrTurnMismatch
	case emptyResponse(response):
		return sessionturn.ErrEmptyTurn
	case tick <= active.StartTick:
		return sessionturn.ErrInvalidTurnTick
	}
	return nil
}

func emptyResponse(response messages.Message) bool {
	return strings.TrimSpace(response.TextContent()) == "" && strings.TrimSpace(response.Refusal) == "" &&
		len(response.ToolCalls) == 0 && !slices.ContainsFunc(response.ContentParts, audioContent)
}

// RunTurn starts a turn, sends its input on the persistent provider session,
// reads one complete response, and ends the turn. A failed run discards the
// active turn without publishing an end event.
func (s *Session) RunTurn(ctx context.Context, input sessionturn.TurnInput, direction sessionturn.TurnDirection, startTick, endTick uint64) (turn sessionturn.SessionTurn, err error) {
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	defer func() {
		if err != nil {
			s.abort()
		}
	}()
	session, err := s.sessionFor(ctx)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	if err = sendInput(ctx, session, input); err != nil {
		return sessionturn.SessionTurn{}, err
	}
	response, err := readResponse(ctx, session)
	if err != nil {
		return sessionturn.SessionTurn{}, err
	}
	return s.EndTurn(started.Index, started.Direction, response, endTick)
}

// History returns independent copies of the completed turns.
func (s *Session) History() []sessionturn.SessionTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.history)
	for i := range out {
		out[i].Input = copyInput(out[i].Input)
	}
	return out
}

// NextTurnIndex returns the index the next started turn will receive.
func (s *Session) NextTurnIndex() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextIndex
}

// Active reports whether a turn is open.
func (s *Session) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active != nil
}

// Close rejects an open turn, then closes the provider session once.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return transitionError(opClose, sessionturn.ErrSessionEndedWithActiveTurn)
	}
	s.closed = true
	session := s.session
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func (s *Session) sessionFor(ctx context.Context) (messages.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	if s.inferencer == nil {
		return nil, transitionError(opRun, sessionturn.ErrMissingTurnInferencer)
	}
	session, err := s.inferencer.ConnectSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect turn session: %w", err)
	}
	s.session = session
	return session, nil
}

func (s *Session) finishTransition(event sessionturn.TurnEvent, err error) {
	s.mu.Unlock()
	defer s.transitionMu.Unlock()
	if err == nil && s.sink != nil {
		s.sink(event)
	}
}

func (s *Session) abort() {
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

func copyInput(in sessionturn.TurnInput) sessionturn.TurnInput {
	in.Audio = append([]byte(nil), in.Audio...)
	return in
}

func cloneTurn(turn sessionturn.SessionTurn) sessionturn.SessionTurn {
	turn.Input = copyInput(turn.Input)
	return turn
}

func audioContent(part messages.ContentPart) bool {
	audio, ok := part.(messages.AudioPart)
	return ok && (strings.TrimSpace(audio.URL) != "" || len(audio.Bytes) != 0)
}
