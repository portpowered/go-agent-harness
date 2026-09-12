package agentruntime

import (
	sessionturns "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
	sessionturnswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns/wire"
)

// Deprecated: use services/sessionturns.SessionTurns.
type TurnDirection = sessionturns.TurnDirection
type TurnEventType = sessionturns.TurnEventType
type TurnEvent = sessionturns.TurnEvent
type TurnEventSink = sessionturns.TurnEventSink
type TurnInput = sessionturns.TurnInput
type SessionTurn = sessionturns.SessionTurn
type SessionTurnsOptions = sessionturns.Options
type SessionTurns = sessionturns.Service

const (
	TurnDirectionUser           = sessionturns.TurnDirectionUser
	TurnDirectionAssistant      = sessionturns.TurnDirectionAssistant
	TurnDirectionClientToServer = sessionturns.TurnDirectionClientToServer
	TurnDirectionServerToClient = sessionturns.TurnDirectionServerToClient
	TurnEventStart              = sessionturns.TurnEventStart
	TurnEventEnd                = sessionturns.TurnEventEnd
)

var ErrTurnAlreadyActive, ErrTurnEndWithoutStart, ErrEmptyTurn, ErrInvalidTurnDirection, ErrInvalidTurnTick, ErrSessionEndedWithActiveTurn, ErrSessionClosed, ErrTurnMismatch, ErrMissingTurnInferencer = sessionturns.ErrTurnAlreadyActive, sessionturns.ErrTurnEndWithoutStart, sessionturns.ErrEmptyTurn, sessionturns.ErrInvalidTurnDirection, sessionturns.ErrInvalidTurnTick, sessionturns.ErrSessionEndedWithActiveTurn, sessionturns.ErrSessionClosed, sessionturns.ErrTurnMismatch, sessionturns.ErrMissingTurnInferencer

// Deprecated: use sessionturns.NewTextTurnInput.
func NewTextTurnInput(text string) TurnInput { return sessionturns.NewTextTurnInput(text) }

// Deprecated: use sessionturns.NewAudioTurnInput.
func NewAudioTurnInput(audio []byte, mediaType string) TurnInput {
	return sessionturns.NewAudioTurnInput(audio, mediaType)
}

// Deprecated: use sessionturns/wire.NewService.
func NewSessionTurns(options SessionTurnsOptions) SessionTurns {
	return sessionturnswire.NewService(sessionturnswire.Dependencies{SessionInferencer: options.SessionInferencer, EventSink: options.EventSink})
}
