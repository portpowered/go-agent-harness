package sessionturn

import (
	"context"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TurnDirection identifies who authored one turn.
type TurnDirection string

// TurnEventType identifies one turn lifecycle transition.
type TurnEventType string

const (
	TurnDirectionUser           TurnDirection = "user"
	TurnDirectionAssistant      TurnDirection = "assistant"
	TurnDirectionClientToServer TurnDirection = "client_to_server"
	TurnDirectionServerToClient TurnDirection = "server_to_client"

	TurnEventStart TurnEventType = "turn-start"
	TurnEventEnd   TurnEventType = "turn-end"
)

// TurnEvent is published after one committed start or end transition.
type TurnEvent struct {
	Type                     TurnEventType
	Index                    uint64
	Direction                TurnDirection
	Tick, StartTick, EndTick uint64
}

// TurnEventSink receives committed transitions in order. Transitions are
// serialized, so a blocked sink delays the next transition.
type TurnEventSink func(TurnEvent)

// TurnInput is one text or audio turn input.
type TurnInput struct {
	Text      string
	Audio     []byte
	MediaType string
}

// Empty reports whether the input carries neither text nor audio.
func (in TurnInput) Empty() bool {
	return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0
}

// SessionTurn is one completed or active turn snapshot. Audio input is copied
// on the way in and out.
type SessionTurn struct {
	Index, StartTick, EndTick uint64
	Direction                 TurnDirection
	Input                     TurnInput
	Response                  messages.Message
}

// TurnsOptions configures one persistent turn session.
type TurnsOptions struct {
	SessionInferencer messages.SessionInferencer
	EventSink         TurnEventSink
}

// Turns is one persistent provider session driven as ordered turns.
type Turns interface {
	StartTurn(input TurnInput, direction TurnDirection, tick uint64) (SessionTurn, error)
	EndTurn(index uint64, direction TurnDirection, response messages.Message, tick uint64) (SessionTurn, error)
	RunTurn(ctx context.Context, input TurnInput, direction TurnDirection, startTick, endTick uint64) (SessionTurn, error)
	History() []SessionTurn
	NextTurnIndex() uint64
	Active() bool
	Close() error
}

// TurnService creates persistent turn sessions.
type TurnService interface {
	NewTurns(TurnsOptions) Turns
}
