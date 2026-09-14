package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/errorpolicy"
)

// NewFailureService exposes only the public failure contract to focused hosts
// and tests; construction remains in the rooms service-owned graph.
func NewFailureService() rooms.FailureService { return errorpolicy.New() }
