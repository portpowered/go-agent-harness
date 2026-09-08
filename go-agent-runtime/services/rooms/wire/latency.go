package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomlatency "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence/latency"
)

// NewLatencyService composes the private room latency implementation behind
// its public service contract. Observation and report logic stays in the
// evidence owner; Wire only chooses the implementation.
func NewLatencyService() rooms.LatencyService { return roomlatency.NewService() }
