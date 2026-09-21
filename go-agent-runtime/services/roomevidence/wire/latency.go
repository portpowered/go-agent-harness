package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/latency"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// NewLatencyService composes the room timing implementation behind the room
// contract. The ledger and report policy remain private to roomevidence.
func NewLatencyService() rooms.LatencyService { return latency.NewService() }
