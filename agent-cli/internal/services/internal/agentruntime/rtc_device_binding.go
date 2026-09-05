package agentruntime

import devicecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const SessionAudioInDeviceFlag = devicecontract.SessionAudioInDeviceFlag
const SessionAudioOutDeviceFlag = devicecontract.SessionAudioOutDeviceFlag

var ErrSessionAudioOutputConflict = devicecontract.ErrSessionAudioOutputConflict

type SessionAudioDeviceConflictError = devicecontract.SessionAudioDeviceConflictError

// RTCDeviceBindingRequest carries the command presence bits separately from
// device IDs. This lets --audio-in-device= and --audio-out-device= select a
// directional default while still distinguishing an omitted flag. Non-empty
// IDs select exact registry IDs even when the corresponding presence bit is
// false, which keeps the service API useful to non-CLI RTC owners.
type RTCDeviceBindingRequest struct {
	Registry      devicegw.DeviceRegistry
	InputDevice   devicegw.DeviceID
	OutputDevice  devicegw.DeviceID
	InputPresent  bool
	OutputPresent bool
	// SelfHearingConfig is used only when both local directions are selected.
	// Zero fields use the documented audio default profile.
	SelfHearingConfig audio.PCM16SelfHearingConfig
	// FeedbackWarningWriter receives the one-time local acoustic-feedback
	// warning. The CLI supplies command stderr; nil disables presentation while
	// retaining the audio gate.
	FeedbackWarningWriter io.Writer
	// BypassSelfHearing keeps device I/O active while explicitly disabling the
	// local feedback controller for replay, file, or room-owned topologies.
	BypassSelfHearing bool
	// OutputSampleRate is the provider-owned PCM16 playback rate. Zero keeps
	// the legacy device rate for callers that do not carry a session contract.
	OutputSampleRate int
	// OutputVoice selects the fixed per-voice output loudness gain applied
	// to this session's own audio before it reaches the device (see
	// audio.LoudnessNormalizer / VoiceLoudnessGainDB). An empty value is the
	// documented 0 dB no-op, matching the provider-selected default voice.
	OutputVoice string
	// InputSampleRate is the provider-owned PCM16 capture rate. A device that
	// cannot open this rate may be opened at another supported rate and
	// converted once by RTCDeviceSource before provider transmission.
	InputSampleRate int
	// PlaybackObserver receives one final queue snapshot when a selected output
	// device closes. It is called outside the native device callback and may be
	// used to publish cumulative overflow diagnostics.
	PlaybackObserver devicert.RTCDevicePlaybackObserver
	// PlaybackReceiptObserver receives every queued playback control result
	// after the sink worker applies or rejects it. It is used by the session
	// runtime trace to distinguish command admission from device application.
	PlaybackReceiptObserver devicert.RTCDevicePlaybackReceiptObserver
	// PlaybackSamplesObserver receives each PCM16 chunk only after it has
	// passed playback-generation admission, provider-to-device resampling,
	// loudness correction, capacity pacing, and a successful device enqueue.
	// The sample rate is the negotiated output-device rate. The callback runs
	// outside the native audio callback and may return an error to stop the
	// session rather than silently publish an incomplete observation.
	PlaybackSamplesObserver devicert.RTCDevicePlaybackSamplesObserver
	// PreGateSamplesObserver receives microphone PCM immediately after the
	// device read and before the local self-hearing filter.
	PreGateSamplesObserver devicert.RTCDeviceCaptureSamplesObserver
	// UploadedSamplesObserver receives provider-rate PCM only after the
	// outbound media boundary accepts it.
	UploadedSamplesObserver devicert.RTCDeviceCaptureSamplesObserver
	// RenderedSamplesObserver receives complete PCM consumed at the physical
	// playback boundary, including synthesized underflow silence, where the
	// selected backend exposes that seam.
	RenderedSamplesObserver devicert.RTCDeviceRenderedSamplesObserver
	// RenderedSamplesUnavailable reports that the selected backend cannot
	// expose physical render callbacks. Trace owners use this to mark partial
	// speaker evidence instead of silently publishing an empty render tap.
	RenderedSamplesUnavailable func()
	// CaptureObserver receives one final capture queue snapshot after the input
	// device has stopped, outside its native callback.
	CaptureObserver devicert.RTCDeviceCaptureObserver
	// Observability is populated by the application composition root so local
	// device snapshots cannot lose their metric/logger path at a command seam.
	Observability observability.Dependencies
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
	Source *devicert.RTCDeviceSource
	Sink   *devicert.RTCDeviceSink
	// Capture is created with the source, before the session workers start.
	// Its control is the exact handoff observed by the loop subsystem.
	Capture  *devicert.BufferedCapture
	feedback *audio.PCM16FeedbackGate

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
		// Stop the sink first. A playback observation serializes the physical
		// write with capture classification; closing the sink releases a device
		// write before the source can be waiting on that shared boundary.
		if b.Sink != nil {
			sinkErr = b.Sink.Close()
		}
		if b.Source != nil {
			sourceErr = b.Source.Close()
		}
		feedbackErr := error(nil)
		if b.feedback != nil {
			feedbackErr = b.feedback.Close()
		}
		b.closeErr = errors.Join(sourceErr, sinkErr, feedbackErr)
	})
	return b.closeErr
}

// RTCDeviceBindingError identifies which command selector failed before the
// provider/peer setup boundary while preserving the registry's typed error.
type RTCDeviceBindingError struct {
	Flag      string
	Direction devicegw.Direction
	DeviceID  devicegw.DeviceID
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

func normalizeRTCDeviceSelector(id devicegw.DeviceID) devicegw.DeviceID {
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
	// Prefer an atomic native duplex graph when both directions are selected.
	// On macOS this is AUVoiceIO: the speaker render callback becomes the AEC
	// reference for the processed microphone uplink. Registries without this
	// optional capability, explicit routes it cannot host, and native setup
	// failures retain the portable independent-device + correlation-gate path.
	if request.inputSelected() && request.outputSelected() {
		inputRate, outputRate := request.InputSampleRate, request.OutputSampleRate
		if inputRate == 0 {
			inputRate = audio.SampleRate
		}
		if outputRate == 0 {
			outputRate = audio.SampleRate
		}
		source, sink, duplexErr := devicegw.NewDuplexDeviceSourceSinkWithFormat(
			request.Registry,
			normalizeRTCDeviceSelector(request.InputDevice), audio.PCM16DeviceFormat(inputRate),
			normalizeRTCDeviceSelector(request.OutputDevice), audio.PCM16DeviceFormat(outputRate),
		)
		if duplexErr == nil {
			binding.Source = devicert.NewRTCDeviceSourceFromOpened(source, inputRate, inputRate)
			binding.Sink = devicert.NewRTCDeviceSinkFromOpened(sink, outputRate, outputRate, request.OutputVoice, request.PlaybackObserver)
			binding.Sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
			binding.Sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
		} else if !errors.Is(duplexErr, devicegw.ErrDuplexDeviceUnavailable) {
			return nil, duplexErr
		}
	}
	if request.inputSelected() && binding.Source == nil {
		source, err := devicert.NewRTCDeviceSourceAtRate(request.Registry, normalizeRTCDeviceSelector(request.InputDevice), request.InputSampleRate)
		if err != nil {
			return nil, &RTCDeviceBindingError{
				Flag:      "--" + SessionAudioInDeviceFlag,
				Direction: devicegw.DirectionInput,
				DeviceID:  request.InputDevice,
				Err:       err,
			}
		}
		binding.Source = source
		binding.Source.SetCaptureObserver(request.CaptureObserver)
	}

	if request.outputSelected() && binding.Sink == nil {
		sink, err := devicert.NewRTCDeviceSinkAtRateWithOptions(request.Registry, normalizeRTCDeviceSelector(request.OutputDevice), request.OutputSampleRate, request.OutputVoice, request.PlaybackObserver)
		if err != nil {
			closeErr := binding.Close()
			return nil, errors.Join(&RTCDeviceBindingError{
				Flag:      "--" + SessionAudioOutDeviceFlag,
				Direction: devicegw.DirectionOutput,
				DeviceID:  request.OutputDevice,
				Err:       err,
			}, closeErr)
		}
		binding.Sink = sink
		binding.Sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
		binding.Sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
	}
	if binding.Source != nil && binding.Sink != nil && !request.BypassSelfHearing {
		// Declare the gate's timing to the true negotiated device rates, not the
		// caller's requested rates: a capture device that cannot honor the
		// requested rate falls back to another supported one (see
		// openRTCDeviceSourceAtRate) and hands the gate raw, pre-resample PCM.
		feedback, feedbackErr := audio.NewPCM16FeedbackGate(request.SelfHearingConfig, request.FeedbackWarningWriter, binding.Sink.SampleRate(), binding.Source.SourceSampleRate())
		if feedbackErr != nil {
			closeErr := binding.Close()
			return nil, errors.Join(feedbackErr, closeErr)
		}
		binding.feedback = feedback
		binding.Source.SetCaptureFilter(feedback)
		binding.Sink.SetPlaybackObserver(feedback)
	}
	if binding.Source != nil {
		capture, captureErr := devicert.NewBufferedCapture(binding.Source)
		if captureErr != nil {
			closeErr := binding.Close()
			return nil, errors.Join(captureErr, closeErr)
		}
		binding.Capture = capture
		binding.Source.SetPreGateSamplesObserver(request.PreGateSamplesObserver)
		binding.Source.SetUploadedSamplesObserver(request.UploadedSamplesObserver)
	}
	if binding.Sink != nil {
		if request.RenderedSamplesObserver != nil && !binding.Sink.SetRenderedSamplesObserver(request.RenderedSamplesObserver) && request.RenderedSamplesUnavailable != nil {
			request.RenderedSamplesUnavailable()
		}
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
