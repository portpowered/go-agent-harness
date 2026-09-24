package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/latency"
)

// NewLatencyService composes the private room timing implementation behind the
// roomevidence contract.
func NewLatencyService() roomevidence.LatencyService { return latency.NewService() }
