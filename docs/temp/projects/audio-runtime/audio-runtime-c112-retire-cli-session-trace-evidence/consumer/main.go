package consumer

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	tracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

// NewService is the only composition entry point needed by an external host.
func NewService() sessiontrace.Service { return tracewire.NewService() }
