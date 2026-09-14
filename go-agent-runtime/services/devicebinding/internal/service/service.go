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
	source, sink, err := openEndpoints(request, binding)
	if err != nil {
		return nil, err
	}
	attachObservers(source, sink, request)
	if err := attachFeedback(source, sink, binding, request); err != nil {
		return nil, err
	}
	if err := attachCapture(source, binding, request); err != nil {
		return nil, err
	}
	configureOutput(sink, request)
	return binding, nil
}

func openEndpoints(request devicebinding.Request, binding *devicebinding.Binding) (*devicert.RTCDeviceSource, *devicert.RTCDeviceSink, error) {
	source, sink, err := openDuplex(request)
	if err != nil {
		return nil, nil, err
	}
	if request.InputSelected() && source == nil {
		source, err = openInput(request)
		if err != nil {
			return nil, nil, err
		}
		binding.Source = source
	}
	if request.OutputSelected() && sink == nil {
		sink, err = openOutput(request)
		if err != nil {
			return nil, nil, errors.Join(err, binding.Close())
		}
		binding.Sink = sink
	}
	if source != nil {
		binding.Source = source
	}
	if sink != nil {
		binding.Sink = sink
	}
	return source, sink, nil
}

func openDuplex(request devicebinding.Request) (*devicert.RTCDeviceSource, *devicert.RTCDeviceSink, error) {
	if !request.InputSelected() || !request.OutputSelected() {
		return nil, nil, nil
	}
	inputRate, outputRate := request.InputSampleRate, request.OutputSampleRate
	if inputRate == 0 {
		inputRate = audio.SampleRate
	}
	if outputRate == 0 {
		outputRate = audio.SampleRate
	}
	sourceOpened, sinkOpened, err := devicegw.NewDuplexDeviceSourceSinkWithFormat(
		request.Registry,
		normalizeSelector(request.InputDevice), audio.PCM16DeviceFormat(inputRate),
		normalizeSelector(request.OutputDevice), audio.PCM16DeviceFormat(outputRate),
	)
	if err != nil {
		if errors.Is(err, devicegw.ErrDuplexDeviceUnavailable) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return devicert.NewRTCDeviceSourceFromOpened(sourceOpened, inputRate, inputRate), devicert.NewRTCDeviceSinkFromOpened(sinkOpened, outputRate, outputRate, request.OutputVoice, request.PlaybackObserver), nil
}

func openInput(request devicebinding.Request) (*devicert.RTCDeviceSource, error) {
	source, err := devicert.NewRTCDeviceSourceAtRate(request.Registry, normalizeSelector(request.InputDevice), request.InputSampleRate)
	if err != nil {
		return nil, &devicebinding.BindingError{Flag: inputDeviceFlag, Direction: devicegw.DirectionInput, DeviceID: request.InputDevice, Err: err}
	}
	return source, nil
}

func openOutput(request devicebinding.Request) (*devicert.RTCDeviceSink, error) {
	sink, err := devicert.NewRTCDeviceSinkAtRateWithOptions(request.Registry, normalizeSelector(request.OutputDevice), request.OutputSampleRate, request.OutputVoice, request.PlaybackObserver)
	if err != nil {
		return nil, &devicebinding.BindingError{Flag: outputDeviceFlag, Direction: devicegw.DirectionOutput, DeviceID: request.OutputDevice, Err: err}
	}
	return sink, nil
}

func attachObservers(source *devicert.RTCDeviceSource, sink *devicert.RTCDeviceSink, request devicebinding.Request) {
	if sink != nil {
		sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
		sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
	}
	if source != nil {
		source.SetCaptureObserver(request.CaptureObserver)
	}
}

func attachFeedback(source *devicert.RTCDeviceSource, sink *devicert.RTCDeviceSink, binding *devicebinding.Binding, request devicebinding.Request) error {
	if source == nil || sink == nil || request.BypassSelfHearing {
		return nil
	}
	feedback, err := audio.NewPCM16FeedbackGate(request.SelfHearingConfig, request.FeedbackWarningWriter, sink.SampleRate(), source.SourceSampleRate())
	if err != nil {
		return errors.Join(err, binding.Close())
	}
	binding.Feedback = feedback
	source.SetCaptureFilter(feedback)
	sink.SetPlaybackObserver(feedback)
	return nil
}

func attachCapture(source *devicert.RTCDeviceSource, binding *devicebinding.Binding, request devicebinding.Request) error {
	if source == nil {
		return nil
	}
	capture, err := devicert.NewBufferedCapture(source)
	if err != nil {
		return errors.Join(err, binding.Close())
	}
	binding.Capture = capture
	source.SetPreGateSamplesObserver(request.PreGateSamplesObserver)
	source.SetUploadedSamplesObserver(request.UploadedSamplesObserver)
	return nil
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
