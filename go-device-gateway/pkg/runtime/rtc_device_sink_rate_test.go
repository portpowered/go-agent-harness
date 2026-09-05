package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestRTCDeviceSinkConvertsProviderRateToSupportedPlaybackRate(t *testing.T) {
	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	observe, err := devicegw.NewDeviceSourceAtRate(registry, "virtual:input", audio.SampleRate)
	if err != nil {
		t.Fatalf("open playback observer: %v", err)
	}
	defer func() { _ = observe.Close() }()

	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open 16 kHz fallback sink for 24 kHz provider: %v", err)
	}
	defer func() { _ = sink.Close() }()
	if sink.ProviderSampleRate() != wavio.Rate24kHz || sink.DeviceSampleRate() != audio.SampleRate {
		t.Fatalf("sink rates = %d -> %d, want %d -> %d", sink.ProviderSampleRate(), sink.DeviceSampleRate(), wavio.Rate24kHz, audio.SampleRate)
	}

	providerSamples := make([]int16, audio.FrameSize*3/2)
	for index := range providerSamples {
		providerSamples[index] = int16(index%401 - 200)
	}
	inbound := &recordingRTCInboundMedia{frames: []audio.PCMFrame{{Samples: providerSamples}}}
	if err := sink.Pump(context.Background(), inbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}

	got := make([]int16, audio.FrameSize)
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observe.ReadFrame(readCtx, got); err != nil {
		t.Fatalf("read converted playback: %v", err)
	}
	reference, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Process(providerSamples, true)
	if err != nil {
		t.Fatalf("reference resample: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("device playback differs from one boundary conversion: got %d samples, want %d", len(got), len(want))
	}
	if gotDuration, wantDuration := len(got)*wavio.Rate24kHz, len(providerSamples)*audio.SampleRate; gotDuration != wantDuration {
		t.Fatalf("playback duration ratio = %d, want %d", gotDuration, wantDuration)
	}
}

func TestRTCDeviceSinkPreservesShort24kSessionMediaResponseAt16kBoundary(t *testing.T) {
	assertShort24kSessionMediaPlayback(t, true)
}

func TestRTCDeviceSinkPreservesShort24kSessionMediaTerminalAt16kBoundary(t *testing.T) {
	assertShort24kSessionMediaPlayback(t, false)
}

func assertShort24kSessionMediaPlayback(t *testing.T, responseDone bool) {
	t.Helper()
	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open 16 kHz fallback sink for 24 kHz provider: %v", err)
	}
	defer func() { _ = sink.Close() }()

	providerSamples := make([]int16, 480)
	for index := range providerSamples {
		providerSamples[index] = int16(index%401 - 200)
	}
	media := audio.NewSessionMediaAtRate(nil, wavio.Rate24kHz)
	defer func() { _ = media.Close() }()
	if err := media.PushInbound(providerSamples); err != nil {
		t.Fatalf("push short provider response: %v", err)
	}
	if responseDone {
		if err := media.FlushInbound(); err != nil {
			t.Fatalf("flush short provider response: %v", err)
		}
	}
	media.FailInbound(io.EOF)
	if err := sink.Pump(context.Background(), media.Endpoints().Inbound); err != nil {
		t.Fatalf("pump actual SessionMedia response: %v", err)
	}

	reference, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Process(providerSamples, true)
	if err != nil {
		t.Fatalf("reference resample: %v", err)
	}
	if got := sink.PlaybackStats().QueuedSamples; got != len(want) {
		t.Fatalf("queued native playback samples = %d, want duration-equivalent %d", got, len(want))
	}
	if got := sink.DiscardPlayback(); got != len(want) {
		t.Fatalf("discarded native playback samples = %d, want exact queued count %d", got, len(want))
	}
	if gotDuration, wantDuration := len(want)*wavio.Rate24kHz, len(providerSamples)*audio.SampleRate; gotDuration != wantDuration {
		t.Fatalf("short playback duration ratio = %d, want %d", gotDuration, wantDuration)
	}
}

func TestRTCDeviceSinkKeepsMatchedPlaybackRateIdentity(t *testing.T) {
	registry := newRTCDeviceSinkRateRegistry(t, wavio.Rate24kHz)
	observe, err := devicegw.NewDeviceSourceAtRate(registry, "virtual:input", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open playback observer: %v", err)
	}
	defer func() { _ = observe.Close() }()
	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open matched-rate sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(190 - index%379)
	}
	if err := sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: []audio.PCMFrame{{Samples: want}}}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	got := make([]int16, audio.FrameSize)
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := observe.ReadFrame(readCtx, got); err != nil {
		t.Fatalf("read matched playback: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("matched-rate playback changed provider samples")
	}
}

func TestRTCDeviceSinkRejectsUnsupportedPlaybackConversionBeforeOpen(t *testing.T) {
	const deviceRate = 44100
	registry := newRTCDeviceSinkRateRegistry(t, deviceRate)

	_, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	var rateErr *RTCDeviceSinkRateError
	if !errors.As(err, &rateErr) || rateErr.ProviderRate != wavio.Rate24kHz || rateErr.DeviceRate != deviceRate {
		t.Fatalf("unsupported conversion error = %T %v, want provider %d and device %d", err, err, wavio.Rate24kHz, deviceRate)
	}
	if !errors.Is(err, wavio.ErrUnsupportedResampleRate) || !errors.Is(err, audio.ErrUnsupportedDeviceFormat) {
		t.Fatalf("unsupported conversion error = %v, want resampler and device-format identities", err)
	}
	for _, fragment := range []string{"24000", "44100"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("unsupported conversion error %q missing %q", err, fragment)
		}
	}
	if got := registry.Observations().OpenCount; got != 0 {
		t.Fatalf("unsupported conversion opened device %d times, want zero", got)
	}
}

func newRTCDeviceSinkRateRegistry(t *testing.T, rate int) *devicegw.VirtualRegistry {
	t.Helper()
	capability := devicegw.VirtualCapability{
		SampleRate: rate,
		Channels:   audio.Channels,
		BitDepth:   audio.DeviceBitDepthPCM16,
		Format:     audio.DeviceEncodingPCM16,
	}
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: devicegw.DirectionInput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput, Capabilities: []devicegw.VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionInput: "input", devicegw.DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	return registry
}
