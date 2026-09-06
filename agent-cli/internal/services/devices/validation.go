package devices

import (
	"errors"
	"fmt"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

var ErrSessionAudioInputConflict = errors.New("--audio-in and --audio-in-device (audio device input) cannot be used together")

const (
	// SessionAudioInDeviceFlag is the session flag that selects the RTC input
	// device. Its value is an opaque audio.DeviceID; an empty value selects the
	// directional default.
	SessionAudioInDeviceFlag = "audio-in-device"
	// SessionAudioOutDeviceFlag is the session flag that selects the RTC output
	// device. Its value is an opaque audio.DeviceID; an empty value selects the
	// directional default.
	SessionAudioOutDeviceFlag = "audio-out-device"
)

// ErrSessionAudioOutputConflict is retained for source compatibility with
// callers that classified the old output-selection conflict. File capture
// and RTC device playback are now independent observations and are allowed
// together, so new validation does not return this error.
var ErrSessionAudioOutputConflict = errors.New("--audio-out and --audio-out-device (audio device output) cannot be used together")

// SessionAudioDeviceConflictError describes a file/device selection conflict
// while preserving both the direction-specific session error and the shared
// audio selection-conflict identity.
type SessionAudioDeviceConflictError struct {
	FileFlag   string
	DeviceFlag string
	Err        error
}

func (e *SessionAudioDeviceConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s and %s cannot be used together", e.FileFlag, e.DeviceFlag)
	}
	return e.Err.Error()
}

func (e *SessionAudioDeviceConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(e.Err, &devicegw.DeviceSelectionConflictError{
		FileOption:   e.FileFlag,
		DeviceOption: e.DeviceFlag,
	})
}

// ValidateSessionAudioDeviceConflicts rejects a file and registry-backed input
// device selector being supplied for the same direction. File assistant
// capture and RTC output-device playback intentionally remain independent so
// both can observe one provider response in the same session. This validation
// is intentionally independent of transport selection so the RTC transport
// can consume it before dialing or constructing a peer.
func ValidateSessionAudioDeviceConflicts(audioInFile, audioOutFile, audioInDevice, audioOutDevice bool) error {
	if audioInFile && audioInDevice {
		return &SessionAudioDeviceConflictError{
			FileFlag:   "--audio-in",
			DeviceFlag: "--" + SessionAudioInDeviceFlag,
			Err:        ErrSessionAudioInputConflict,
		}
	}
	return nil
}
