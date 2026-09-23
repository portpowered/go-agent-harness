// Package wire contains the session service's explicit host composition.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
	durationrun "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/durationrun"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// LiveDependencies describes the provider, tool, and evidence edges for a
// continuous session. Hosts own provider selection and replay construction;
// this package only assembles the private live implementation.
type LiveDependencies struct {
	InferencerFactory session.LiveInferencerFactory
	CapabilityFactory session.LiveCapabilityFactory
	ToolExecutor      messages.ToolExecutor
	ToolDefinitions   []messages.ToolDefinition
	EventCapacity     int
	Clock             session.LiveClock
	Scheduler         platformclock.Scheduler
	DurationService   sessionduration.Service
	RuntimeObserver   sessiontrace.RuntimeObserver
	Tick              func() uint64
}

// DurationDependencies contains the service-owned dependencies for one
// bounded agent-loop invocation.
type DurationDependencies struct {
	DurationService sessionduration.Service
	LoopFactory     sessionduration.DuplexLoopFactory
}

// NewDurationRunner assembles the bounded invocation owner behind the public
// session contract.
func NewDurationRunner(deps DurationDependencies) session.DurationRunner {
	return durationrun.NewDurationRunner(deps.DurationService, deps.LoopFactory)
}

// NewLiveService assembles the continuous session role. It does not connect a
// provider until the returned handle's Start method is called.
func NewLiveService(deps LiveDependencies) session.LiveService {
	return live.New(live.Dependencies{
		InferencerFactory: deps.InferencerFactory,
		CapabilityFactory: deps.CapabilityFactory,
		ToolExecutor:      deps.ToolExecutor,
		ToolDefinitions:   append([]messages.ToolDefinition(nil), deps.ToolDefinitions...),
		EventCapacity:     deps.EventCapacity,
		Clock:             deps.Clock,
		Scheduler:         deps.Scheduler,
		DurationService:   deps.DurationService,
		RuntimeObserver:   deps.RuntimeObserver,
		Tick:              deps.Tick,
	})
}
