// Package roomevidence adapts CLI values to the public room evidence port.
package roomevidence

import runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

func ValidateReplayOutput(service runtimeRooms.Service, plan runtimeRooms.RoomReplayPlan, destination string) error {
	return service.ValidateReplayOutput(plan, destination)
}

func ValidateOutput(service runtimeRooms.Service, destination string) error {
	return service.ValidateEvidenceOutput(destination)
}

func FreshRunDirectory(service runtimeRooms.Service, configDir string) (string, error) {
	return service.CreateFreshRunDirectory(configDir)
}
