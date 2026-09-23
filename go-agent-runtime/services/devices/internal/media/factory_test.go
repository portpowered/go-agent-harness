package media

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

const (
	testInputDevice  = "virtual:input"
	testOutputDevice = "virtual:output"
)

func TestFactoryReportsSelectedDevicesAndQueuedPlayback(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := NewFactory(registry, mixer.Format{}).Open(context.Background(), devices.Request{
		InputDevice:     " DEFAULT ",
		OutputDevice:    testOutputDevice,
		CaptureEnabled:  true,
		PlaybackEnabled: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	selection, ok := handle.(devices.DeviceSelectionProvider)
	if !ok {
		t.Fatal("device handle does not expose its selected device IDs")
	}
	inputID, outputID := selection.SelectedDeviceIDs()
	if inputID != testInputDevice || outputID != testOutputDevice {
		t.Fatalf("selected devices = (%q, %q), want (%q, %q)", inputID, outputID, testInputDevice, testOutputDevice)
	}

	statsProvider, ok := handle.(devices.PlaybackStatsProvider)
	if !ok {
		t.Fatal("device handle does not expose its playback queue snapshot")
	}
	deviceID, stats := statsProvider.PlaybackStats()
	if deviceID != outputID || stats.QueuedSamples != 0 {
		t.Fatalf("initial playback snapshot = (%q, %+v), want selected output and empty queue", deviceID, stats)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := &oneFrameThenBlockedInbound{
		frame:     audio.PCMFrame{Samples: []int16{1, -1}, EndOfResponse: true},
		readAgain: make(chan struct{}),
	}
	pumpResult := make(chan error, 1)
	go func() { pumpResult <- handle.Media().Playback.Pump(ctx, inbound) }()
	select {
	case <-inbound.readAgain:
	case <-time.After(time.Second):
		t.Fatal("playback pump did not admit the frame before the deadline")
	}
	deviceID, stats = statsProvider.PlaybackStats()
	if deviceID != outputID || stats.QueuedSamples != 2 {
		t.Fatalf("queued playback snapshot = (%q, %+v), want two admitted samples", deviceID, stats)
	}
	if stats.RenderedSamples != 0 || stats.CallbackCount != 0 {
		t.Fatalf("queue admission reported device consumption: %+v", stats)
	}
	cancel()
	if err := <-pumpResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Playback.Pump() after cancellation = %v, want context.Canceled", err)
	}
}

func TestFactoryPlaybackAppliesOneDeviceOwnedHoldToneToSilentRoomFrames(t *testing.T) {
	backend := devicegw.DefaultVirtualBackendConfig()
	backend.RecordPCM = true
	registry, err := devicegw.NewVirtualRegistry(backend)
	if err != nil {
		t.Fatal(err)
	}
	config := audio.DefaultHoldToneConfig()
	config.GapThreshold = 25 * time.Millisecond
	config.PulseInterval = time.Hour
	config.PulseDuration = 25 * time.Millisecond
	deviceHandle, err := NewFactory(registry, mixer.DefaultFormat()).Open(context.Background(), devices.Request{
		OutputDevice: testOutputDevice, PlaybackEnabled: true, HoldToneConfig: &config,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := deviceHandle.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	inbound := &oneFrameThenBlockedInbound{frame: audio.PCMFrame{Samples: make([]int16, audio.FrameSize)}, readAgain: make(chan struct{})}
	pumpResult := make(chan error, 1)
	go func() { pumpResult <- deviceHandle.Media().Playback.Pump(ctx, inbound) }()
	select {
	case <-inbound.readAgain:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("playback pump did not reach its bounded silent gap")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		nonSilent := 0
		for _, observation := range registry.PCMObservations() {
			if devicegw.DirectionOutput == observation.Direction && hasNonZeroSamples(observation.Samples) {
				nonSilent++
			}
		}
		if nonSilent == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	nonSilent := 0
	for _, observation := range registry.PCMObservations() {
		if devicegw.DirectionOutput == observation.Direction && hasNonZeroSamples(observation.Samples) {
			nonSilent++
		}
	}
	if nonSilent != 1 {
		cancel()
		t.Fatalf("non-silent playback writes = %d, want one device-owned hold tone", nonSilent)
	}
	cancel()
	if err := <-pumpResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Playback.Pump() after cancellation = %v, want context.Canceled", err)
	}
}

func TestFactoryClosesCaptureWhenPlaybackAdmissionFails(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner, failID: testOutputDevice}
	factory := NewFactory(registry, mixer.DefaultFormat())
	_, err = factory.Open(context.Background(), devices.Request{
		InputDevice:     testInputDevice,
		OutputDevice:    testOutputDevice,
		CaptureEnabled:  true,
		PlaybackEnabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "open output device") {
		t.Fatalf("OpenMedia error = %v, want output admission error", err)
	}
	if got := inner.Observations().ReleaseCount; got != 1 {
		t.Fatalf("released handles = %d, want capture cleanup after output failure", got)
	}
}

func TestFactoryRejectsUnsupportedShapeBeforeOpen(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner}
	factory := NewFactory(registry, mixer.Format{SampleRate: 24000, Channels: 2})
	_, err = factory.Open(context.Background(), devices.Request{CaptureEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "requires mono format") {
		t.Fatalf("OpenMedia error = %v, want mono format validation", err)
	}
	if got := len(registry.opened); got != 0 {
		t.Fatalf("opened devices = %d, want no admission for invalid format", got)
	}
}

func TestFactoryRejectsCancelledAdmission(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner}
	factory := NewFactory(registry, mixer.DefaultFormat())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = factory.Open(ctx, devices.Request{CaptureEnabled: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenMedia error = %v, want context.Canceled", err)
	}
	if got := len(registry.opened); got != 0 {
		t.Fatalf("opened devices = %d, want no admission after cancellation", got)
	}
}

func TestFactoryHandleOwnsBothWorkersAndClosesOnce(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(inner, mixer.DefaultFormat())
	handle, err := factory.Open(context.Background(), devices.Request{
		InputDevice:     testInputDevice,
		OutputDevice:    testOutputDevice,
		CaptureEnabled:  true,
		PlaybackEnabled: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if handle == nil || handle.Media().Capture == nil || handle.Media().Playback == nil {
		t.Fatalf("Open() returned incomplete media handle: %#v", handle)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := inner.Observations().ReleaseCount; got != 2 {
		t.Fatalf("released handles = %d, want one release per worker", got)
	}
}

func TestFactoryAdmitsEachDirectionIndependently(t *testing.T) {
	tests := []struct {
		name       string
		enable     devices.Request
		wantInput  bool
		wantOutput bool
		wantOpen   int
	}{
		{name: "capture only", enable: devices.Request{CaptureEnabled: true}, wantInput: true, wantOpen: 1},
		{name: "playback only", enable: devices.Request{PlaybackEnabled: true}, wantOutput: true, wantOpen: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertFactoryDirection(t, test.enable, test.wantInput, test.wantOutput, test.wantOpen)
		})
	}
}

func assertFactoryDirection(t *testing.T, request devices.Request, wantInput, wantOutput bool, wantOpen int) {
	t.Helper()
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	request.InputDevice = testInputDevice
	request.OutputDevice = testOutputDevice
	handle, err := NewFactory(inner, mixer.DefaultFormat()).Open(context.Background(), request)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	assertMediaDirections(t, handle, wantInput, wantOutput)
	_, outputID := assertHandleSelection(t, handle, wantInput, wantOutput)
	assertHandlePlaybackStats(t, handle, outputID, wantOutput)
	if err := handle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := inner.Observations().OpenCount; got != wantOpen {
		t.Fatalf("opened handles = %d, want %d", got, wantOpen)
	}
}

func assertMediaDirections(t *testing.T, handle devices.Handle, wantInput, wantOutput bool) {
	t.Helper()
	ports := handle.Media()
	if (ports.Capture != nil) != wantInput || (ports.Playback != nil) != wantOutput {
		t.Fatalf("ports = capture %v playback %v", ports.Capture != nil, ports.Playback != nil)
	}
}

func assertHandleSelection(t *testing.T, handle devices.Handle, wantInput, wantOutput bool) (string, string) {
	t.Helper()
	selection, ok := handle.(devices.DeviceSelectionProvider)
	if !ok {
		t.Fatal("device handle does not expose selected device IDs")
	}
	inputID, outputID := selection.SelectedDeviceIDs()
	if wantInput != (inputID == testInputDevice) || wantOutput != (outputID == testOutputDevice) {
		t.Fatalf("selected devices = (%q, %q), want input=%v output=%v", inputID, outputID, wantInput, wantOutput)
	}
	return inputID, outputID
}

func assertHandlePlaybackStats(t *testing.T, handle devices.Handle, outputID string, wantOutput bool) {
	t.Helper()
	statsProvider, ok := handle.(devices.PlaybackStatsProvider)
	if !ok {
		t.Fatal("device handle does not expose playback stats")
	}
	statsDeviceID, stats := statsProvider.PlaybackStats()
	if wantOutput {
		if statsDeviceID != outputID || stats.QueuedSamples != 0 {
			t.Fatalf("playback snapshot = (%q, %+v), want selected empty output queue", statsDeviceID, stats)
		}
		return
	}
	if statsDeviceID != "" || stats != (audio.PlaybackQueueStats{}) {
		t.Fatalf("capture-only playback snapshot = (%q, %+v), want empty", statsDeviceID, stats)
	}
}

func TestFactoryUnavailableAndUnselectedRTCBindingResults(t *testing.T) {
	var missing *Factory
	if _, err := missing.Open(context.Background(), devices.Request{CaptureEnabled: true}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil factory Open() error = %v, want devices.ErrUnavailable", err)
	}
	if _, err := missing.BindRTC(context.Background(), devices.RTCBindingRequest{InputPresent: true}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil factory BindRTC() error = %v, want devices.ErrUnavailable", err)
	}

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewFactory(registry, mixer.DefaultFormat()).BindRTC(context.Background(), devices.RTCBindingRequest{})
	if err != nil || binding != nil {
		t.Fatalf("BindRTC() without selected directions = (%v, %v), want (nil, nil)", binding, err)
	}
	if got := registry.Observations().OpenCount; got != 0 {
		t.Fatalf("unselected RTC binding opened %d devices, want none", got)
	}
}

func TestFactoryBindsSelectedRTCDirections(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(registry, mixer.DefaultFormat())
	tests := []struct {
		name       string
		request    devices.RTCBindingRequest
		wantInput  string
		wantOutput string
	}{
		{name: "input", request: devices.RTCBindingRequest{InputPresent: true, BypassSelfHearing: true}, wantInput: testInputDevice},
		{name: "output", request: devices.RTCBindingRequest{OutputPresent: true, BypassSelfHearing: true}, wantOutput: testOutputDevice},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, err := factory.BindRTC(context.Background(), test.request)
			if err != nil {
				t.Fatalf("BindRTC() error = %v", err)
			}
			selection, ok := binding.(interface{ SelectedDeviceIDs() (string, string) })
			if !ok {
				t.Fatal("RTC binding does not expose selected device IDs")
			}
			inputID, outputID := selection.SelectedDeviceIDs()
			if inputID != test.wantInput || outputID != test.wantOutput {
				t.Fatalf("selected devices = (%q, %q), want (%q, %q)", inputID, outputID, test.wantInput, test.wantOutput)
			}
			if err := binding.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestFactoryRejectsDirectionlessAndNegativeRequests(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(inner, mixer.DefaultFormat())
	for _, request := range []devices.Request{
		{},
		{CaptureEnabled: true, SampleRate: -1},
		{PlaybackEnabled: true, Channels: -1},
	} {
		if _, err := factory.Open(context.Background(), request); !errors.Is(err, devices.ErrInvalidRequest) {
			t.Fatalf("Open(%+v) error = %v, want devices.ErrInvalidRequest", request, err)
		}
	}
	if got := inner.Observations().OpenCount; got != 0 {
		t.Fatalf("opened handles = %d, want no admission for invalid requests", got)
	}
}

func TestFactoryRejectsInvalidRemoteEndpoint(t *testing.T) {
	factory := NewFactory(devicegw.NewPlatformDeviceRegistry(), mixer.DefaultFormat())
	_, err := factory.Open(context.Background(), devices.Request{PlaybackEnabled: true, RemoteEndpoint: "192.0.2.10:19090"})
	if !errors.Is(err, devices.ErrInvalidRemoteEndpoint) {
		t.Fatalf("Open remote error = %v, want ErrInvalidRemoteEndpoint", err)
	}
}

func TestFactoryBindRTCRejectsInvalidRemoteEndpoint(t *testing.T) {
	factory := NewFactory(devicegw.NewPlatformDeviceRegistry(), mixer.DefaultFormat())
	_, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{
		InputPresent:   true,
		RemoteEndpoint: "192.0.2.10:19090",
	})
	if !errors.Is(err, devices.ErrInvalidRemoteEndpoint) {
		t.Fatalf("BindRTC remote error = %v, want ErrInvalidRemoteEndpoint", err)
	}
}

type registryStub struct {
	inner  devicegw.DeviceRegistry
	failID devicegw.DeviceID
	opened []devicegw.DeviceID
}

func (r *registryStub) List() ([]devicegw.Device, error) { return r.inner.List() }

func (r *registryStub) Default(direction devicegw.Direction) (devicegw.Device, error) {
	return r.inner.Default(direction)
}

func (r *registryStub) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	if id == r.failID {
		return nil, errors.New("injected output admission failure")
	}
	handle, err := r.inner.Open(id)
	if err != nil {
		return nil, err
	}
	r.opened = append(r.opened, id)
	return handle, nil
}

var _ devicegw.DeviceRegistry = (*registryStub)(nil)

type oneFrameThenBlockedInbound struct {
	frame     audio.PCMFrame
	sent      bool
	readAgain chan struct{}
}

func (i *oneFrameThenBlockedInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if !i.sent {
		i.sent = true
		return i.frame, nil
	}
	select {
	case i.readAgain <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return audio.PCMFrame{}, ctx.Err()
}

func (*oneFrameThenBlockedInbound) Close() error { return nil }

func hasNonZeroSamples(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}
