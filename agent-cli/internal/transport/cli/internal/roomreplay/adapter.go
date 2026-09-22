// Package roomreplay contains the CLI transport translation for the public
// room-replay service. It owns no admission, filesystem, or runtime state.
package roomreplay

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func Load(service roomreplay.Service, path string) (rooms.RoomReplayPlan, error) {
	if service == nil {
		return rooms.RoomReplayPlan{}, rooms.ErrRoomServiceUnavailable
	}
	return service.Load(path)
}

func ValidateOutput(service roomreplay.Service, plan rooms.RoomReplayPlan, destination string) error {
	if service == nil {
		return rooms.ErrRoomServiceUnavailable
	}
	return service.ValidateOutput(plan, destination)
}
