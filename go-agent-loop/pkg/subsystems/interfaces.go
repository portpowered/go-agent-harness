package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/state"
)

// Subsystem is a subsystem that runs during each tick of the engine.
type Subsystem interface {
	TickGroup() TickGroup
	Execute(ctx context.Context, curr *state.LoopState) error
}

// TickGroup determines the execution order of helpers within a single tick.
// Lower values run first.
type TickGroup int

const (
	// TickGroupInterruptHandler runs before all other subsystems so it can cancel
	// in-flight executions and reset pass state before the Coordinator reacts.
	TickGroupInterruptHandler TickGroup = -1

	// TickGroupPingPong runs after InterruptHandler and Coordinator so pong
	// responses are emitted early in the tick cycle but after coordination.
	TickGroupPingPong TickGroup = 1

	TickGroupCoordinator             TickGroup = 0
	TickGroupInteractionEvents       TickGroup = 4
	TickGroupCoordinatorDelta        TickGroup = 5
	TickGroupConversationLoop        TickGroup = 10
	TickGroupRecorder                TickGroup = 30
	TickGroupTokenCounter            TickGroup = 40
	TickGroupContextPressureNotifier TickGroup = 45
)
