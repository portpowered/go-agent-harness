package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication/internal/publisher"
)

// Dependencies are explicit host-level inputs to the toolpublication graph.
// A session may still override the timer factory in its own Options.
type Dependencies struct {
	TimerFactory toolpublication.TimerFactory
}

func newService(dependencies Dependencies) toolpublication.Service {
	return publisher.NewService(dependencies.TimerFactory)
}
