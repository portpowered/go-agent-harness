// Package service contains the private devicebinding admission policy.
package service

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const (
	inputDeviceFlag  = "--audio-in-device"
	outputDeviceFlag = "--audio-out-device"
)

// Service is stateless; every registry access occurs during Open.
type Service struct{}

// New returns the private implementation used by the dedicated Wire edge.
func New() *Service { return &Service{} }

// Open resolves selected directions, wires observations and feedback, and
// rolls back every admitted endpoint when a later step fails.
func (*Service) Open(request devicebinding.Request) (*devicebinding.Binding, error) {
	if !request.Selected() {
		return nil, nil
	}

	binding := &devicebinding.Binding{}
	var source *devicert.RTCDeviceSource
	var sink *devicert.RTCDeviceSink
	if request.InputSelected() && request.OutputSelected() {
		inputRate, outputRate := request.InputSampleRate, request.OutputSampleRate
		if inputRate == 0 {
			inputRate = audio.SampleRate
		}
		if outputRate == 0 {
			outputRate = audio.SampleRate
		}
		sourceOpened, sinkOpened, duplexErr := devicegw.NewDuplexDeviceSourceSinkWithFormat(
			request.Registry,
			normalizeSelector(request.InputDevice), audio.PCM16DeviceFormat(inputRate),
			normalizeSelector(request.OutputDevice), audio.PCM16DeviceFormat(outputRate),
		)
		if duplexErr == nil {
			source = devicert.NewRTCDeviceSourceFromOpened(sourceOpened, inputRate, inputRate)
			sink = devicert.NewRTCDeviceSinkFromOpened(sinkOpened, outputRate, outputRate, request.OutputVoice, request.PlaybackObserver)
			binding.Source, binding.Sink = source, sink
		} else if !errors.Is(duplexErr, devicegw.ErrDuplexDeviceUnavailable) {
			return nil, duplexErr
		}
	}

	if request.InputSelected() && source == nil {
		var err error
		source, err = devicert.NewRTCDeviceSourceAtRate(request.Registry, normalizeSelector(request.InputDevice), request.InputSampleRate)
		if err != nil {
			return nil, &devicebinding.BindingError{Flag: inputDeviceFlag, Direction: devicegw.DirectionInput, DeviceID: request.InputDevice, Err: err}
		}
		binding.Source = source
	}
	if request.OutputSelected() && sink == nil {
		var err error
		sink, err = devicert.NewRTCDeviceSinkAtRateWithOptions(request.Registry, normalizeSelector(request.OutputDevice), request.OutputSampleRate, request.OutputVoice, request.PlaybackObserver)
		if err != nil {
			return nil, errors.Join(&devicebinding.BindingError{Flag: outputDeviceFlag, Direction: devicegw.DirectionOutput, DeviceID: request.OutputDevice, Err: err}, binding.Close())
		}
		binding.Sink = sink
	}

	if sink != nil {
		sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
		sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
	}
	if source != nil {
		source.SetCaptureObserver(request.CaptureObserver)
	}
	if source != nil && sink != nil && !request.BypassSelfHearing {
		feedback, err := audio.NewPCM16FeedbackGate(request.SelfHearingConfig, request.FeedbackWarningWriter, sink.SampleRate(), source.SourceSampleRate())
		if err != nil {
			return nil, errors.Join(err, binding.Close())
		}
		binding.Feedback = feedback
		source.SetCaptureFilter(feedback)
		sink.SetPlaybackObserver(feedback)
	}
	if source != nil {
		capture, err := devicert.NewBufferedCapture(source)
		if err != nil {
			return nil, errors.Join(err, binding.Close())
		}
		binding.Capture = capture
		source.SetPreGateSamplesObserver(request.PreGateSamplesObserver)
		source.SetUploadedSamplesObserver(request.UploadedSamplesObserver)
	}
	configureOutput(sink, request)
	return binding, nil
}

func normalizeSelector(id devicegw.DeviceID) devicegw.DeviceID {
	if strings.EqualFold(strings.TrimSpace(id), "default") {
		return ""
	}
	return id
}

func configureOutput(sink *devicert.RTCDeviceSink, request devicebinding.Request) {
	if sink == nil {
		return
	}
	if request.HoldToneConfig != nil {
		sink.SetHoldToneConfig(*request.HoldToneConfig)
	}
	if request.RenderedSamplesObserver != nil && !sink.SetRenderedSamplesObserver(request.RenderedSamplesObserver) && request.RenderedSamplesUnavailable != nil {
		request.RenderedSamplesUnavailable()
	}
}
