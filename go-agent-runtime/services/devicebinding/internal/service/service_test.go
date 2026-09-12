package service

import (
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestOpenDirectionalAndDuplexPaths(t *testing.T) {
	t.Run("input only", func(t *testing.T) {
		registry := virtualRegistry(t)
		binding, err := New().Open(devicebinding.Request{Registry: registry, InputDevice: " DEFAULT ", InputPresent: true})
		if err != nil || binding == nil || binding.Source == nil || binding.Sink != nil {
			t.Fatalf("input-only Open() = %#v, %v", binding, err)
		}
		if err := binding.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("output only with output options", func(t *testing.T) {
		registry := virtualRegistry(t)
		config := audio.DefaultHoldToneConfig()
		binding, err := New().Open(devicebinding.Request{
			Registry: registry, OutputDevice: "default", OutputPresent: true,
			HoldToneConfig: &config, RenderedSamplesObserver: func(int, []int16) {},
		})
		if err != nil || binding == nil || binding.Source != nil || binding.Sink == nil {
			t.Fatalf("output-only Open() = %#v, %v", binding, err)
		}
		if err := binding.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("native duplex", func(t *testing.T) {
		registry := &duplexRegistry{VirtualRegistry: virtualRegistry(t)}
		binding, err := New().Open(devicebinding.Request{Registry: registry, InputPresent: true, OutputPresent: true})
		if err != nil || binding == nil || binding.Source == nil || binding.Sink == nil || binding.Feedback == nil {
			t.Fatalf("duplex Open() = %#v, %v", binding, err)
		}
		if err := binding.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("negotiated rate is preserved", func(t *testing.T) {
		const rate = 16000
		capability := devicegw.VirtualCapability{SampleRate: rate, Channels: audio.Channels, BitDepth: audio.DeviceBitDepthPCM16, Format: audio.DeviceEncodingPCM16}
		virtual, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
			Devices: []devicegw.VirtualDeviceConfig{
				{ID: "input", Name: "Input", Direction: devicegw.DirectionInput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "output"},
				{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "input"},
			},
			Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "input", devicegw.DirectionOutput: "output"},
		})
		if err != nil {
			t.Fatalf("NewVirtualRegistry() error = %v", err)
		}
		binding, err := New().Open(devicebinding.Request{Registry: &duplexRegistry{VirtualRegistry: virtual}, InputPresent: true, OutputPresent: true, InputSampleRate: rate, OutputSampleRate: rate})
		if err != nil {
			t.Fatalf("rate Open() error = %v", err)
		}
		if binding.Source.SourceSampleRate() != rate || binding.Source.ProviderSampleRate() != rate || binding.Sink.SampleRate() != rate || binding.Sink.ProviderSampleRate() != rate {
			t.Fatalf("negotiated rates = source %d/%d sink %d/%d, want %d", binding.Source.SourceSampleRate(), binding.Source.ProviderSampleRate(), binding.Sink.SampleRate(), binding.Sink.ProviderSampleRate(), rate)
		}
		if err := binding.Close(); err != nil {
			t.Fatalf("rate Close() error = %v", err)
		}
	})
}

func TestOpenFailuresAreTypedAndFailClosed(t *testing.T) {
	t.Run("input selector", func(t *testing.T) {
		registry := virtualRegistry(t)
		_, err := New().Open(devicebinding.Request{Registry: registry, InputDevice: "virtual:missing", InputPresent: true})
		var bindingErr *devicebinding.BindingError
		if !errors.As(err, &bindingErr) || !errors.Is(err, devicegw.ErrDeviceNotFound) {
			t.Fatalf("input error = %v, want typed not-found binding error", err)
		}
	})

	t.Run("feedback construction", func(t *testing.T) {
		registry := virtualRegistry(t)
		_, err := New().Open(devicebinding.Request{
			Registry: registry, InputPresent: true, OutputPresent: true,
			SelfHearingConfig: selfhearing.PCM16SelfHearingConfig{AnalysisWindow: -time.Second},
		})
		if err == nil {
			t.Fatal("invalid feedback config returned nil error")
		}
		if got := registry.Observations().ReleaseCount; got != 2 {
			t.Fatalf("feedback rollback releases = %d, want 2", got)
		}
	})
}

func TestOpenFallsBackOnlyForUnavailableDuplex(t *testing.T) {
	t.Run("unavailable falls back", func(t *testing.T) {
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
	})

	t.Run("other errors stop admission", func(t *testing.T) {
		duplexErr := errors.New("native duplex setup failed")
		registry := &duplexOutcomeRegistry{VirtualRegistry: virtualRegistry(t), err: duplexErr}
		binding, err := New().Open(devicebinding.Request{Registry: registry, InputPresent: true, OutputPresent: true})
		if binding != nil || !errors.Is(err, duplexErr) {
			t.Fatalf("non-unavailable duplex Open() = %#v, %v", binding, err)
		}
		if got := registry.Observations().OpenCount; got != 0 {
			t.Fatalf("non-unavailable duplex opened %d devices, want zero", got)
		}
	})
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
		_ = input.Close()
		return nil, nil, err
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

func virtualRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	return registry
}
