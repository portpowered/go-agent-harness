package services

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

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
	return errors.Join(e.Err, &audio.DeviceSelectionConflictError{
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

// RTCDeviceBindingRequest carries the command presence bits separately from
// device IDs. This lets --audio-in-device= and --audio-out-device= select a
// directional default while still distinguishing an omitted flag. Non-empty
// IDs select exact registry IDs even when the corresponding presence bit is
// false, which keeps the service API useful to non-CLI RTC owners.
type RTCDeviceBindingRequest struct {
	Registry      audio.DeviceRegistry
	InputDevice   audio.DeviceID
	OutputDevice  audio.DeviceID
	InputPresent  bool
	OutputPresent bool
	// OutputSampleRate is the provider-owned PCM16 playback rate. Zero keeps
	// the legacy device rate for callers that do not carry a session contract.
	OutputSampleRate int
	// InputSampleRate is the provider-owned PCM16 capture rate. A device that
	// cannot open this rate may be opened at another supported rate and
	// converted once by RTCDeviceSource before provider transmission.
	InputSampleRate int
}

func (r RTCDeviceBindingRequest) inputSelected() bool {
	return r.InputPresent || r.InputDevice != ""
}

func (r RTCDeviceBindingRequest) outputSelected() bool {
	return r.OutputPresent || r.OutputDevice != ""
}

func (r RTCDeviceBindingRequest) selected() bool {
	return r.inputSelected() || r.outputSelected()
}

// RTCDeviceBinding owns the registry-backed local endpoints used by an RTC
// session. The session runtime starts Source.Pump and Sink.Pump against the
// provider-owned media endpoints; this object owns only the selected local
// devices and releases them exactly once.
type RTCDeviceBinding struct {
	Source *RTCDeviceSource
	Sink   *RTCDeviceSink

	closeOnce sync.Once
	closeErr  error
}

// Close releases both local devices. It is safe to call more than once and
// never closes caller-owned RTC media endpoints.
func (b *RTCDeviceBinding) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var sourceErr, sinkErr error
		if b.Source != nil {
			sourceErr = b.Source.Close()
		}
		if b.Sink != nil {
			sinkErr = b.Sink.Close()
		}
		b.closeErr = errors.Join(sourceErr, sinkErr)
	})
	return b.closeErr
}

// RTCDeviceBindingError identifies which command selector failed before the
// provider/peer setup boundary while preserving the registry's typed error.
type RTCDeviceBindingError struct {
	Flag      string
	Direction audio.Direction
	DeviceID  audio.DeviceID
	Err       error
}

func (e *RTCDeviceBindingError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s could not select %s audio device %q", e.Flag, e.Direction, e.DeviceID)
	}
	return fmt.Sprintf("%s could not select %s audio device %q: %v", e.Flag, e.Direction, e.DeviceID, e.Err)
}

func (e *RTCDeviceBindingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func normalizeRTCDeviceSelector(id audio.DeviceID) audio.DeviceID {
	if strings.EqualFold(strings.TrimSpace(id), "default") {
		return ""
	}
	return id
}

// PrepareRTCDeviceBindings resolves and opens all selected devices through
// the shared audio.DeviceRegistry. No device is opened when neither selector
// is present. Input is opened before output; if output fails, the input is
// released before the typed output error is returned.
func PrepareRTCDeviceBindings(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	if !request.selected() {
		return nil, nil
	}

	binding := &RTCDeviceBinding{}
	if request.inputSelected() {
		source, err := NewRTCDeviceSourceAtRate(request.Registry, normalizeRTCDeviceSelector(request.InputDevice), request.InputSampleRate)
		if err != nil {
			return nil, &RTCDeviceBindingError{
				Flag:      "--" + SessionAudioInDeviceFlag,
				Direction: audio.DirectionInput,
				DeviceID:  request.InputDevice,
				Err:       err,
			}
		}
		binding.Source = source
	}

	if request.outputSelected() {
		sink, err := NewRTCDeviceSinkAtRate(request.Registry, normalizeRTCDeviceSelector(request.OutputDevice), request.OutputSampleRate)
		if err != nil {
			closeErr := binding.Close()
			return nil, errors.Join(&RTCDeviceBindingError{
				Flag:      "--" + SessionAudioOutDeviceFlag,
				Direction: audio.DirectionOutput,
				DeviceID:  request.OutputDevice,
				Err:       err,
			}, closeErr)
		}
		binding.Sink = sink
	}

	return binding, nil
}

// OpenRTCDeviceBindings is a descriptive alias for callers that model the
// preflight operation as an open step.
func OpenRTCDeviceBindings(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	return PrepareRTCDeviceBindings(request)
}

// NewRTCDeviceBinding is a constructor-shaped alias for embedding callers.
func NewRTCDeviceBinding(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	return PrepareRTCDeviceBindings(request)
}
