package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"errors"
	"fmt"
)

// DeviceFormatOpener is the optional registry capability for opening a
// device at an explicit PCM format. Registries that do not implement it can
// still be used with NewDeviceSource/NewDeviceSink, which retain the legacy
// backend-selected format.
type DeviceFormatOpener interface {
	OpenWithFormat(DeviceID, audio.DeviceFormat) (OpenedDevice, error)
}

// DuplexDeviceFormatOpener is an optional registry capability that acquires
// capture and playback as one atomic hardware graph. It is intentionally
// separate from DeviceRegistry.Open: ordinary devices remain independently
// openable, while native AEC backends can guarantee a shared render reference.
type DuplexDeviceFormatOpener interface {
	OpenDuplexWithFormat(inputID DeviceID, inputFormat audio.DeviceFormat, outputID DeviceID, outputFormat audio.DeviceFormat) (OpenedDevice, OpenedDevice, error)
}

// VoiceProcessingProvider reports whether a device endpoint is backed by a
// native duplex voice-processing graph rather than the portable fallback.
type VoiceProcessingProvider interface {
	VoiceProcessingActive() bool
}

// DeviceFormatProvider is implemented by opened devices that can report the
// format selected by their registry/backend.
type DeviceFormatProvider interface {
	DeviceFormat() audio.DeviceFormat
}

// DeviceFormatError preserves the requested and, when known, available
// formats so callers can explain a provider/device rate mismatch without
// parsing a backend-specific error string.
type DeviceFormatError struct {
	ID        DeviceID
	Direction Direction
	Requested audio.DeviceFormat
	Available []audio.DeviceFormat
	Err       error
}

func (e *DeviceFormatError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("device %q cannot open %s format %s", e.ID, e.Direction, e.Requested)
	if len(e.Available) > 0 {
		message += "; available: "
		for index, format := range e.Available {
			if index > 0 {
				message += ", "
			}
			message += format.String()
		}
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *DeviceFormatError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return errors.Join(audio.ErrUnsupportedDeviceFormat, e.Err)
	}
	return audio.ErrUnsupportedDeviceFormat
}
