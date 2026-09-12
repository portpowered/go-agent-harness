// Package sessionturns owns the transport-neutral state machine for ordered
// turns on one reusable inference session.
//
// The package contains value contracts only. Session state, provider protocol
// handling, and lifecycle policy live in the private implementation assembled
// by services/sessionturns/wire.
package sessionturns

import (
	"context"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TurnDirection identifies the direction of one admitted turn.
type TurnDirection string

// TurnEventType identifies the lifecycle edge represented by an event.
type TurnEventType string

// TurnEvent is the immutable event published for a successful turn boundary.
type TurnEvent struct {
	Type                     TurnEventType
	Index                    uint64
	Direction                TurnDirection
	Tick, StartTick, EndTick uint64
}

// TurnEventSink observes successful turn transitions. Implementations should
// keep callbacks bounded and must not mutate the service through a transition
// method while a callback is running.
type TurnEventSink func(TurnEvent)

// TurnInput is the copied text or audio input admitted for a turn.
type TurnInput struct {
	Text      string
	Audio     []byte
	MediaType string
}

// SessionTurn is the immutable value returned for a completed turn or a
// read-only history snapshot. Audio and message byte slices are copied by the
// service before publication and when returned to callers.
type SessionTurn struct {
	Index, StartTick, EndTick uint64
	Direction                 TurnDirection
	Input                     TurnInput
	Response                  messages.Message
}

// Options supplies the explicit provider and event dependencies for one
// service instance. Construction is inert and does not connect a provider.
type Options struct {
	SessionInferencer messages.SessionInferencer
	EventSink         TurnEventSink
}

// SessionTurnsOptions is the descriptive compatibility name for Options.
type SessionTurnsOptions = Options

// Service owns one ordered, reusable session-turn state machine.
type Service interface {
	StartTurn(TurnInput, TurnDirection, uint64) (SessionTurn, error)
	EndTurn(uint64, TurnDirection, messages.Message, uint64) (SessionTurn, error)
	RunTurn(context.Context, TurnInput, TurnDirection, uint64, uint64) (SessionTurn, error)
	History() []SessionTurn
	ActiveTurn() (SessionTurn, bool)
	NextTurnIndex() uint64
	Close() error
}

// SessionTurns is retained as a readable alias for the public service
// contract. Implementations remain private.
type SessionTurns = Service

const (
	TurnDirectionUser           TurnDirection = "user"
	TurnDirectionAssistant      TurnDirection = "assistant"
	TurnDirectionClientToServer TurnDirection = "client_to_server"
	TurnDirectionServerToClient TurnDirection = "server_to_client"

	TurnEventStart TurnEventType = "turn-start"
	TurnEventEnd   TurnEventType = "turn-end"
)

// ErrorCode is a comparable sentinel cause suitable for errors.Is and
// errors.As. Constants keep the public contract immutable while preserving
// exact identity through wrapped transition errors.
type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	// ErrTurnAlreadyActive identifies an overlapping start transition.
	ErrTurnAlreadyActive ErrorCode = "turn start while another turn is active"
	// ErrTurnEndWithoutStart identifies an end without an active turn.
	ErrTurnEndWithoutStart ErrorCode = "turn end without start: no active turn"
	// ErrEmptyTurn identifies empty text, audio, or response content.
	ErrEmptyTurn ErrorCode = "turn content must not be empty"
	// ErrInvalidTurnDirection identifies a direction outside the contract.
	ErrInvalidTurnDirection ErrorCode = "turn direction is invalid"
	// ErrInvalidTurnTick identifies a non-increasing transition tick.
	ErrInvalidTurnTick ErrorCode = "turn tick must be strictly increasing"
	// ErrSessionEndedWithActiveTurn prevents Close from discarding a turn.
	ErrSessionEndedWithActiveTurn ErrorCode = "session ended with active turn"
	// ErrSessionClosed identifies a closed service or provider session.
	ErrSessionClosed ErrorCode = "session is closed"
	// ErrTurnMismatch identifies an end for a different active turn.
	ErrTurnMismatch ErrorCode = "turn does not match the active turn"
	// ErrMissingTurnInferencer identifies construction without a provider.
	ErrMissingTurnInferencer ErrorCode = "session turn inferencer is not configured"
	// ErrMissingTurnSession identifies a provider that returned no session.
	ErrMissingTurnSession ErrorCode = "session turn provider returned no session"
	// ErrSessionResponse is the fail-closed fallback for a blank provider error.
	ErrSessionResponse ErrorCode = "session returned an error"
	// ErrTurnInputRejected identifies a provider that rejected input admission.
	ErrTurnInputRejected ErrorCode = "session rejected turn input"
	// ErrTurnInputCommitRejected identifies a provider that rejected audio commit.
	ErrTurnInputCommitRejected ErrorCode = "session rejected turn input commit"
)

// Empty reports whether the input has neither non-whitespace text nor audio.
func (in TurnInput) Empty() bool {
	return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0
}
