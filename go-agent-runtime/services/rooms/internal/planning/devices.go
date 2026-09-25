package planning

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

const deviceUnavailableProblem = "device is unavailable; run agent devices list"

// validateHumanDevices checks every human selector against one host device
// snapshot before any participant runs. Agent-only rooms never consult the
// host device port.
func validateHumanDevices(value rooms.Manifest, devices rooms.LaunchDevices) error {
	if !hasHuman(value) {
		return nil
	}
	if devices == nil {
		return fmt.Errorf("audio device registry is unavailable; run agent devices list: %w", rooms.ErrLaunchDeviceInventoryUnavailable)
	}
	listed, err := devices.List()
	if err != nil {
		return fmt.Errorf("could not inspect available devices: %w", err)
	}
	byID := make(map[string]rooms.LaunchDevice, len(listed))
	for _, device := range listed {
		byID[device.ID] = device
	}
	for index, participant := range value.Participants {
		if manifest.NormalizeParticipantKind(participant.Kind) != rooms.ParticipantKindHuman {
			continue
		}
		if err := selectDevice(byID, participant.InputDevice, rooms.LaunchDeviceInput, index, "input_device"); err != nil {
			return err
		}
		if err := selectDevice(byID, participant.OutputDevice, rooms.LaunchDeviceOutput, index, "output_device"); err != nil {
			return err
		}
	}
	return nil
}

func hasHuman(value rooms.Manifest) bool {
	for _, participant := range value.Participants {
		if manifest.NormalizeParticipantKind(participant.Kind) == rooms.ParticipantKindHuman {
			return true
		}
	}
	return false
}

func selectDevice(byID map[string]rooms.LaunchDevice, selector string, want rooms.LaunchDeviceDirection, index int, field string) error {
	name := fmt.Sprintf("participants[%d].%s", index, field)
	device, ok := byID[selector]
	if !ok {
		return &rooms.ValidationError{Field: name, Value: selector, Problem: deviceUnavailableProblem, Cause: rooms.ErrLaunchDeviceUnavailable}
	}
	if device.Direction != want {
		cause := &rooms.LaunchDeviceDirectionError{ID: device.ID, Want: want, Got: device.Direction}
		return &rooms.ValidationError{Field: name, Value: selector, Problem: cause.Error(), Cause: cause}
	}
	return nil
}

// defaultDevice resolves one bare-room host default and rejects a host that
// reports a device of the opposite direction.
func defaultDevice(devices rooms.LaunchDevices, direction rooms.LaunchDeviceDirection) (rooms.LaunchDevice, error) {
	device, err := devices.Default(direction)
	if err != nil {
		return rooms.LaunchDevice{}, fmt.Errorf("bare room customer %s device is unavailable: %w; run agent devices list", direction, err)
	}
	if device.Direction != direction {
		return rooms.LaunchDevice{}, &rooms.LaunchDeviceDirectionError{ID: device.ID, Want: direction, Got: device.Direction}
	}
	return device, nil
}
