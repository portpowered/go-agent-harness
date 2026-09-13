package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestOpenNoSelectionDoesNotTouchRegistry(t *testing.T) {
	binding, err := New().Open(devicebinding.Request{})
	if err != nil || binding != nil {
		t.Fatalf("Open(no selection) = %#v, %v; want nil, nil", binding, err)
	}
}

func TestOpenInputOnlyUsesDefaultAndPublishesCaptureSnapshot(t *testing.T) {
	registry := virtualRegistry(t)
	var snapshots atomic.Int32
	binding, err := New().Open(devicebinding.Request{
		Registry: registry, InputDevice: " DeFaUlT ", InputPresent: true,
		CaptureObserver: func(devicegw.DeviceID, audio.CaptureQueueStats) { snapshots.Add(1) },
	})
	if err != nil || binding == nil || binding.Source == nil || binding.Sink != nil || binding.Capture == nil {
		t.Fatalf("input-only Open() = %#v, %v", binding, err)
	}
	if binding.Source.DeviceID() != "virtual:input" {
		t.Fatalf("input device = %q, want virtual:input", binding.Source.DeviceID())
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if snapshots.Load() != 1 {
		t.Fatalf("capture snapshots = %d, want one", snapshots.Load())
	}
}

func TestOpenOutputOnlyAppliesHoldToneAndSupportedRenderObserver(t *testing.T) {
	registry := virtualRegistry(t)
	config := audio.DefaultHoldToneConfig()
	var snapshots atomic.Int32
	binding, err := New().Open(devicebinding.Request{
		Registry: registry, OutputDevice: "default", OutputPresent: true,
		HoldToneConfig: &config, RenderedSamplesObserver: func(int, []int16) {},
		PlaybackObserver: func(devicegw.DeviceID, audio.PlaybackQueueStats) { snapshots.Add(1) },
	})
	if err != nil || binding == nil || binding.Source != nil || binding.Sink == nil {
		t.Fatalf("output-only Open() = %#v, %v", binding, err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if snapshots.Load() != 1 {
		t.Fatalf("playback snapshots = %d, want one", snapshots.Load())
	}
}

func TestOpenNativeDuplexWiresFeedbackAndBothSnapshots(t *testing.T) {
	registry := &duplexRegistry{VirtualRegistry: virtualRegistry(t)}
	var playbackSnapshots, captureSnapshots atomic.Int32
	binding, err := New().Open(devicebinding.Request{
		Registry: registry, InputPresent: true, OutputPresent: true,
		PlaybackObserver: func(devicegw.DeviceID, audio.PlaybackQueueStats) { playbackSnapshots.Add(1) },
		CaptureObserver:  func(devicegw.DeviceID, audio.CaptureQueueStats) { captureSnapshots.Add(1) },
	})
	if err != nil || binding == nil || binding.Source == nil || binding.Sink == nil || binding.Feedback == nil || binding.Capture == nil {
		t.Fatalf("duplex Open() = %#v, %v", binding, err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if playbackSnapshots.Load() != 1 || captureSnapshots.Load() != 1 {
		t.Fatalf("duplex snapshots = playback %d, capture %d, want one each", playbackSnapshots.Load(), captureSnapshots.Load())
	}
}

func TestOpenBypassesFeedbackWhenExplicitlyRequested(t *testing.T) {
	registry := virtualRegistry(t)
	binding, err := New().Open(devicebinding.Request{Registry: registry, InputPresent: true, OutputPresent: true, BypassSelfHearing: true})
	if err != nil || binding == nil || binding.Feedback != nil {
		t.Fatalf("bypassed feedback Open() = %#v, %v", binding, err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenPreservesNegotiatedRates(t *testing.T) {
	const rate = 16000
	capability := devicegw.VirtualCapability{SampleRate: rate, Channels: audio.Channels, BitDepth: audio.DeviceBitDepthPCM16, Format: audio.DeviceEncodingPCM16}
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: devicegw.DirectionInput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "input", devicegw.DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	binding, err := New().Open(devicebinding.Request{Registry: &duplexRegistry{VirtualRegistry: registry}, InputPresent: true, OutputPresent: true, InputSampleRate: rate, OutputSampleRate: rate})
	if err != nil {
		t.Fatalf("rate Open() error = %v", err)
	}
	if binding.Source.SourceSampleRate() != rate || binding.Source.ProviderSampleRate() != rate || binding.Sink.SampleRate() != rate || binding.Sink.ProviderSampleRate() != rate {
		t.Fatalf("negotiated rates = source %d/%d sink %d/%d, want %d", binding.Source.SourceSampleRate(), binding.Source.ProviderSampleRate(), binding.Sink.SampleRate(), binding.Sink.ProviderSampleRate(), rate)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("rate Close() error = %v", err)
	}
}

func TestOpenOutputFailureReturnsTypedErrorAndRollsBackInput(t *testing.T) {
	registry := virtualRegistry(t)
	binding, err := New().Open(devicebinding.Request{Registry: registry, InputDevice: "virtual:input", OutputDevice: "virtual:missing", InputPresent: true, OutputPresent: true})
	if binding != nil || err == nil {
		t.Fatalf("failed Open() = %#v, %v", binding, err)
	}
	var bindingErr *devicebinding.BindingError
	if !errors.As(err, &bindingErr) || !errors.Is(err, devicegw.ErrDeviceNotFound) {
		t.Fatalf("error = %v, want typed output not-found error", err)
	}
	if bindingErr.Flag != outputDeviceFlag || bindingErr.Direction != devicegw.DirectionOutput || bindingErr.DeviceID != "virtual:missing" {
		t.Fatalf("binding error = %#v, want output metadata", bindingErr)
	}
	if got := registry.Observations().ReleaseCount; got != 1 {
		t.Fatalf("rollback releases = %d, want one input release", got)
	}
}

func TestOpenFeedbackFailureRollsBackBothEndpoints(t *testing.T) {
	registry := virtualRegistry(t)
	_, err := New().Open(devicebinding.Request{
		Registry: registry, InputPresent: true, OutputPresent: true,
		SelfHearingConfig: selfhearing.PCM16SelfHearingConfig{AnalysisWindow: -time.Second},
	})
	if err == nil {
		t.Fatal("invalid feedback config returned nil error")
	}
	if got := registry.Observations().ReleaseCount; got != 2 {
		t.Fatalf("feedback rollback releases = %d, want two", got)
	}
}

func TestOpenFallsBackOnUnavailableDuplex(t *testing.T) {
	registry := &duplexOutcomeRegistry{VirtualRegistry: virtualRegistry(t), err: devicegw.ErrDuplexDeviceUnavailable}
	binding, err := New().Open(devicebinding.Request{Registry: registry, InputPresent: true, OutputPresent: true, BypassSelfHearing: true})
	if err != nil || binding == nil || binding.Source == nil || binding.Sink == nil {
		t.Fatalf("fallback Open() = %#v, %v", binding, err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("fallback Close() error = %v", err)
	}
	if got := registry.Observations().OpenCount; got != 2 {
		t.Fatalf("fallback open count = %d, want two independent opens", got)
	}
}

func TestOpenRejectsOtherDuplexErrors(t *testing.T) {
	duplexErr := errors.New("native duplex setup failed")
	registry := &duplexOutcomeRegistry{VirtualRegistry: virtualRegistry(t), err: duplexErr}
	binding, err := New().Open(devicebinding.Request{Registry: registry, InputPresent: true, OutputPresent: true})
	if binding != nil || !errors.Is(err, duplexErr) {
		t.Fatalf("non-unavailable duplex Open() = %#v, %v", binding, err)
	}
	if got := registry.Observations().OpenCount; got != 0 {
		t.Fatalf("non-unavailable duplex opened %d devices, want zero", got)
	}
}

func TestOpenReportsUnavailableRenderBoundary(t *testing.T) {
	registry := &noRenderRegistry{VirtualRegistry: virtualRegistry(t)}
	var unavailable atomic.Int32
	binding, err := New().Open(devicebinding.Request{
		Registry: registry, OutputPresent: true,
		RenderedSamplesObserver: func(int, []int16) {}, RenderedSamplesUnavailable: func() { unavailable.Add(1) },
	})
	if err != nil || binding == nil || binding.Sink == nil {
		t.Fatalf("Open(no render boundary) = %#v, %v", binding, err)
	}
	if unavailable.Load() != 1 {
		t.Fatalf("unavailable-render calls = %d, want one", unavailable.Load())
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNormalizeSelectorOnlyMapsDefault(t *testing.T) {
	if got := normalizeSelector(" DeFaUlT "); got != "" {
		t.Fatalf("normalizeSelector(default) = %q, want empty", got)
	}
	if got := normalizeSelector(" virtual:input "); got != " virtual:input " {
		t.Fatalf("normalizeSelector(opaque ID) = %q, want unchanged", got)
	}
}

type duplexRegistry struct{ *devicegw.VirtualRegistry }

func (r *duplexRegistry) OpenDuplexWithFormat(inputID devicegw.DeviceID, inputFormat audio.DeviceFormat, outputID devicegw.DeviceID, outputFormat audio.DeviceFormat) (devicegw.OpenedDevice, devicegw.OpenedDevice, error) {
	input, err := r.OpenWithFormat(inputID, inputFormat)
	if err != nil {
		return nil, nil, err
	}
	output, err := r.OpenWithFormat(outputID, outputFormat)
	if err != nil {
		return nil, nil, errors.Join(err, input.Close())
	}
	return input, output, nil
}

type duplexOutcomeRegistry struct {
	*devicegw.VirtualRegistry
	err error
}

func (r *duplexOutcomeRegistry) OpenDuplexWithFormat(devicegw.DeviceID, audio.DeviceFormat, devicegw.DeviceID, audio.DeviceFormat) (devicegw.OpenedDevice, devicegw.OpenedDevice, error) {
	return nil, nil, r.err
}

type noRenderRegistry struct{ *devicegw.VirtualRegistry }

func (r *noRenderRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	stream, err := r.VirtualRegistry.Open(id)
	if err != nil {
		return nil, err
	}
	return withoutRenderObserver(stream)
}

func (r *noRenderRegistry) OpenWithFormat(id devicegw.DeviceID, format audio.DeviceFormat) (devicegw.OpenedDevice, error) {
	stream, err := r.VirtualRegistry.OpenWithFormat(id, format)
	if err != nil {
		return nil, err
	}
	return withoutRenderObserver(stream)
}

func withoutRenderObserver(stream devicegw.OpenedDevice) (devicegw.OpenedDevice, error) {
	virtualStream, ok := stream.(*devicegw.VirtualStream)
	if !ok {
		return nil, errors.New("virtual registry returned an unexpected stream")
	}
	return &noRenderStream{stream: virtualStream}, nil
}

type noRenderStream struct{ stream *devicegw.VirtualStream }

func (s *noRenderStream) Close() error                        { return s.stream.Close() }
func (s *noRenderStream) DeviceDirection() devicegw.Direction { return s.stream.DeviceDirection() }
func (s *noRenderStream) DeviceFormat() audio.DeviceFormat    { return s.stream.DeviceFormat() }
func (s *noRenderStream) WriteFrame(ctx context.Context, frame []int16) error {
	return s.stream.WriteFrame(ctx, frame)
}
func (s *noRenderStream) ReadFrame(ctx context.Context, frame []int16) error {
	return s.stream.ReadFrame(ctx, frame)
}

func virtualRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	return registry
}
