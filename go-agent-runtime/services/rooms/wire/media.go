package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommedia "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/media"
)

// NewMediaFactory adapts the whole host device service to the room media
// contract. Device admission policy remains in the devices service and room
// orchestration only receives the narrow participant port.
func NewMediaFactory(service devices.Service) rooms.MediaFactory {
	return roommedia.NewFactory(service)
}
