// Package wire contains the session service's explicit host composition.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
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
	})
}
