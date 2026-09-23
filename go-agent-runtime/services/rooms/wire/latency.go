package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// NewLatencyService composes the room latency implementation from the
// service-owned evidence package. The rooms graph retains only the public
// latency contract; policy and state stay behind roomevidence Wire.
func NewLatencyService() rooms.LatencyService { return wire.NewLatencyService() }
