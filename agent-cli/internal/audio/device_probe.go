package audio

import (
	"fmt"
	"sort"
)

// DeviceProbeStatus is the outcome of the device availability guard that
// runs before a device-tier probe opens any hardware.
type DeviceProbeStatus string

const (
	DeviceProbeStatusReady DeviceProbeStatus = "ready"
	DeviceProbeStatusSkip  DeviceProbeStatus = "skip"
)

// DeviceProbeSkipCode is a stable machine-readable reason for not running a
// device-tier probe. The human-readable Reason is deliberately kept beside
// it so callers never need to parse an error string.
type DeviceProbeSkipCode string

const (
	DeviceProbeSkipNoInputDevice  DeviceProbeSkipCode = "no_audio_input_device"
	DeviceProbeSkipNoOutputDevice DeviceProbeSkipCode = "no_audio_output_device"
	DeviceProbeSkipNoDevices      DeviceProbeSkipCode = "no_audio_input_or_output_devices"
)

// DeviceProbeAvailability is the side-effect-free result of enumerating the
// registry at the start of a device-tier probe. InputDevices and
// OutputDevices retain the validated snapshot for the later binding stage;
// they are not serialized because the device metadata is an implementation
// detail of the registry and may contain host-specific names.
type DeviceProbeAvailability struct {
	Status            DeviceProbeStatus   `json:"status"`
	ReasonCode        DeviceProbeSkipCode `json:"reason_code,omitempty"`
	Reason            string              `json:"reason,omitempty"`
	InputDeviceCount  int                 `json:"input_device_count"`
	OutputDeviceCount int                 `json:"output_device_count"`
	Devices           []Device            `json:"-"`
	InputDevices      []Device            `json:"-"`
	OutputDevices     []Device            `json:"-"`
}

// ProbeDeviceAvailability enumerates the shared device registry exactly once
// and decides whether a probe has both directions it needs. Enumeration
// never opens a device, so a SKIP is safe on headless or CI hosts and cannot
// consume an exclusive device merely to inspect availability.
func ProbeDeviceAvailability(registry DeviceRegistry) (DeviceProbeAvailability, error) {
	if registry == nil {
		return DeviceProbeAvailability{}, fmt.Errorf("enumerate audio devices: %w", ErrNilDeviceRegistry)
	}

	devices, err := registry.List()
	if err != nil {
		return DeviceProbeAvailability{}, fmt.Errorf("enumerate audio devices: %w", err)
	}

	result := DeviceProbeAvailability{
		Devices:       append([]Device(nil), devices...),
		InputDevices:  make([]Device, 0),
		OutputDevices: make([]Device, 0),
	}
	seen := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return DeviceProbeAvailability{}, fmt.Errorf("enumerate audio device %q: %w", device.ID, err)
		}
		key := string(device.ID) + "\x00" + device.Direction.String()
		if _, exists := seen[key]; exists {
			return DeviceProbeAvailability{}, fmt.Errorf("enumerate audio devices: duplicate device %q for %s", device.ID, device.Direction)
		}
		seen[key] = struct{}{}
		switch device.Direction {
		case DirectionInput:
			result.InputDevices = append(result.InputDevices, device)
		case DirectionOutput:
			result.OutputDevices = append(result.OutputDevices, device)
		}
	}

	sortProbeDevices(result.InputDevices)
	sortProbeDevices(result.OutputDevices)
	result.InputDeviceCount = len(result.InputDevices)
	result.OutputDeviceCount = len(result.OutputDevices)
	if result.InputDeviceCount == 0 || result.OutputDeviceCount == 0 {
		result.Status, result.ReasonCode, result.Reason = deviceProbeSkipReason(result.InputDeviceCount, result.OutputDeviceCount)
		return result, nil
	}
	result.Status = DeviceProbeStatusReady
	return result, nil
}

func sortProbeDevices(devices []Device) {
	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].ID != devices[j].ID {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].Display() < devices[j].Display()
	})
}

func deviceProbeSkipReason(inputCount, outputCount int) (DeviceProbeStatus, DeviceProbeSkipCode, string) {
	switch {
	case inputCount == 0 && outputCount == 0:
		return DeviceProbeStatusSkip, DeviceProbeSkipNoDevices, "no audio input or output device"
	case inputCount == 0:
		return DeviceProbeStatusSkip, DeviceProbeSkipNoInputDevice, "no audio input device"
	default:
		return DeviceProbeStatusSkip, DeviceProbeSkipNoOutputDevice, "no audio output device"
	}
}
