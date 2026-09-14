package rtc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

func (f *Factory) BindRTC(ctx context.Context, request devices.RTCBindingRequest) (devices.RTCBinding, error) {
	inputSelected, outputSelected, err := admitBindRequest(ctx, f, request)
	if err != nil {
		return nil, err
	}
	if !inputSelected && !outputSelected {
		return nil, nil
	}
	registry, err := f.resolveRegistry(request)
	if err != nil {
		return nil, err
	}
	b := &binding{}
	if err := openSelectedDevices(b, registry, request, inputSelected, outputSelected); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	if err := configureBinding(b, request); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	return b, nil
}

func admitBindRequest(ctx context.Context, f *Factory, request devices.RTCBindingRequest) (bool, bool, error) {
	if ctx == nil {
		return false, false, fmt.Errorf("RTC binding context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	if f == nil {
		return false, false, errors.Join(devices.ErrUnavailable, devicegw.ErrNilDeviceRegistry)
	}
	return request.InputPresent || strings.TrimSpace(request.InputDevice) != "", request.OutputPresent || strings.TrimSpace(request.OutputDevice) != "", nil
}

func (f *Factory) resolveRegistry(request devices.RTCBindingRequest) (devicegw.DeviceRegistry, error) {
	registry := f.registry
	if endpoint := strings.TrimSpace(request.RemoteEndpoint); endpoint != "" {
		var err error
		registry, err = devicegw.NewRemoteDeviceRegistry(endpoint)
		if err != nil {
			return nil, fmt.Errorf("connect remote audio device server: %w", err)
		}
	}
	if registry == nil {
		return nil, errors.Join(devices.ErrUnavailable, devicegw.ErrNilDeviceRegistry)
	}
	return registry, nil
}

func openSelectedDevices(b *binding, registry devicegw.DeviceRegistry, request devices.RTCBindingRequest, inputSelected, outputSelected bool) error {
	if inputSelected && outputSelected {
		if err := openDuplex(b, registry, request); err != nil {
			return err
		}
	}
	if inputSelected && b.source == nil {
		if err := openInput(b, registry, request); err != nil {
			return err
		}
	}
	if outputSelected && b.sink == nil {
		if err := openOutput(b, registry, request); err != nil {
			return err
		}
	}
	return nil
}

func openDuplex(b *binding, registry devicegw.DeviceRegistry, request devices.RTCBindingRequest) error {
	inputRate, outputRate := request.InputSampleRate, request.OutputSampleRate
	if inputRate == 0 {
		inputRate = audio.SampleRate
	}
	if outputRate == 0 {
		outputRate = audio.SampleRate
	}
	source, sink, err := devicegw.NewDuplexDeviceSourceSinkWithFormat(registry, normalizeSelector(request.InputDevice), audio.PCM16DeviceFormat(inputRate), normalizeSelector(request.OutputDevice), audio.PCM16DeviceFormat(outputRate))
	if errors.Is(err, devicegw.ErrDuplexDeviceUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	b.source = devicert.NewRTCDeviceSourceFromOpened(source, inputRate, inputRate)
	b.sink = devicert.NewRTCDeviceSinkFromOpened(sink, outputRate, outputRate, request.OutputVoice, request.PlaybackObserver)
	b.sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
	b.sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
	return nil
}

func openInput(b *binding, registry devicegw.DeviceRegistry, request devices.RTCBindingRequest) error {
	source, err := devicert.NewRTCDeviceSourceAtRate(registry, normalizeSelector(request.InputDevice), request.InputSampleRate)
	if err != nil {
		return &bindingError{flag: "--" + inputFlag, direction: devicegw.DirectionInput, id: request.InputDevice, err: err}
	}
	b.source = source
	return nil
}

func openOutput(b *binding, registry devicegw.DeviceRegistry, request devices.RTCBindingRequest) error {
	sink, err := devicert.NewRTCDeviceSinkAtRateWithOptions(registry, normalizeSelector(request.OutputDevice), request.OutputSampleRate, request.OutputVoice, request.PlaybackObserver)
	if err != nil {
		return &bindingError{flag: "--" + outputFlag, direction: devicegw.DirectionOutput, id: request.OutputDevice, err: err}
	}
	b.sink = sink
	b.sink.SetPlaybackReceiptObserver(request.PlaybackReceiptObserver)
	b.sink.SetPlaybackSamplesObserver(request.PlaybackSamplesObserver)
	return nil
}

func configureBinding(b *binding, request devices.RTCBindingRequest) error {
	if err := configureFeedback(b, request); err != nil {
		return err
	}
	if err := configureCapture(b, request); err != nil {
		return err
	}
	configurePlayback(b, request)
	b.inferencer = &inferencer{inner: request.Inferencer, binding: b, errors: make(chan error, 8)}
	return nil
}

func configureFeedback(b *binding, request devices.RTCBindingRequest) error {
	if b.source == nil || b.sink == nil || request.BypassSelfHearing {
		return nil
	}
	feedback, err := audio.NewPCM16FeedbackGate(request.SelfHearingConfig, request.FeedbackWarningWriter, b.sink.SampleRate(), b.source.SourceSampleRate())
	if err != nil {
		return err
	}
	b.feedback = feedback
	b.source.SetCaptureFilter(feedback)
	b.sink.SetPlaybackObserver(feedback)
	return nil
}

func configureCapture(b *binding, request devices.RTCBindingRequest) error {
	if b.source == nil {
		return nil
	}
	b.source.SetCaptureObserver(request.CaptureObserver)
	capture, err := devicert.NewBufferedCapture(b.source)
	if err != nil {
		return err
	}
	b.capture = capture
	b.source.SetPreGateSamplesObserver(request.PreGateSamplesObserver)
	b.source.SetUploadedSamplesObserver(request.UploadedSamplesObserver)
	return nil
}

func configurePlayback(b *binding, request devices.RTCBindingRequest) {
	if b.sink == nil {
		return
	}
	if request.HoldToneConfig != nil {
		b.sink.SetHoldToneConfig(*request.HoldToneConfig)
	}
	if request.RenderedSamplesObserver != nil && !b.sink.SetRenderedSamplesObserver(request.RenderedSamplesObserver) && request.RenderedSamplesUnavailable != nil {
		request.RenderedSamplesUnavailable()
	}
}
