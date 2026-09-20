package agentruntime_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"fmt"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const (
	rtcRoundtripInputID    devicegw.DeviceID = "virtual:mic-in"
	rtcRoundtripMicFeedID  devicegw.DeviceID = "virtual:mic-feed"
	rtcRoundtripOutputID   devicegw.DeviceID = "virtual:speaker-out"
	rtcRoundtripSpeakerID  devicegw.DeviceID = "virtual:speaker-observe"
	rtcRoundtripFrameCount                   = 4
	rtcRoundtripTimeout                      = 2 * time.Second
)

// TestRTCBindingVirtualRegistryTrackRoundTrip proves the complete PCM
// path at the provider-neutral RTC media boundary. The loopback peer models
// the outgoing and incoming WebRTC tracks with the same frame ownership and
// cancellation contract as rtc.OutboundMedia/rtc.InboundMedia; the virtual
// registry keeps capture and playback topologies separate so both directions
// can be observed without a hardware device.
func TestRTCBindingVirtualRegistryTrackRoundTrip(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	peer := newLoopbackRTCTrackPeer(rtcRoundtripFrameCount)
	provider := newRuntimeRTCSessionInferencer(peer)
	binding := openRTCDeviceRoundtripBinding(t, registry, provider)
	feed, observe := openRTCDeviceRoundtripObservation(t, registry)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if closeErr := binding.Close(); closeErr != nil {
			t.Errorf("close binding: %v", closeErr)
		}
		if closeErr := feed.Close(); closeErr != nil {
			t.Errorf("close feed: %v", closeErr)
		}
		if closeErr := observe.Close(); closeErr != nil {
			t.Errorf("close observe: %v", closeErr)
		}
	})

	assertRTCDeviceRoundtripIDs(t, registry)
	t.Cleanup(func() {
		if closeErr := peer.Close(); closeErr != nil {
			t.Errorf("close peer: %v", closeErr)
		}
	})
	providerSession, err := binding.Inferencer().ConnectSession(ctx)
	if err != nil {
		t.Fatalf("connect provider media session: %v", err)
	}

	wantFrames := writeRTCDeviceRoundtripFrames(t, ctx, feed)

	readCtx, readCancel := context.WithTimeout(context.Background(), rtcRoundtripTimeout)
	defer readCancel()
	gotFrames := readRTCDeviceRoundtripFrames(t, readCtx, observe)

	if !reflect.DeepEqual(gotFrames, wantFrames) {
		t.Fatalf("roundtrip speaker frames differ from microphone frames")
	}
	if got := peer.Stats(); got.Writes != rtcRoundtripFrameCount || got.Reads != rtcRoundtripFrameCount {
		t.Fatalf("loopback peer stats = %+v, want %d writes and reads", got, rtcRoundtripFrameCount)
	}

	closeRTCDeviceRoundtripResources(t, providerSession, binding, feed, observe)
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("registry observations = %+v, want four opens and four releases", got)
	}
	cancel()
	if err := peer.Close(); err != nil {
		t.Fatalf("close loopback peer: %v", err)
	}
}

func openRTCDeviceRoundtripBinding(t *testing.T, registry *devicegw.VirtualRegistry, provider messages.SessionInferencer) runtimedevices.RTCBinding {
	t.Helper()
	binding, err := newTestDeviceService(registry).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{
		Inferencer: provider, InputPresent: true, OutputPresent: true,
	})
	if err != nil {
		t.Fatalf("prepare default RTC device bindings: %v", err)
	}
	if binding == nil || binding.Inferencer() == nil {
		t.Fatalf("binding = %#v, want public binding", binding)
	}
	return binding
}

func openRTCDeviceRoundtripObservation(t *testing.T, registry *devicegw.VirtualRegistry) (*devicegw.DeviceSink, *devicegw.DeviceSource) {
	t.Helper()
	feed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	observe, err := devicegw.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open virtual speaker observer: %v", err)
	}
	return feed, observe
}

func writeRTCDeviceRoundtripFrames(t *testing.T, ctx context.Context, feed *devicegw.DeviceSink) [][]int16 {
	t.Helper()
	wantFrames := make([][]int16, rtcRoundtripFrameCount)
	for frameIndex := range wantFrames {
		wantFrames[frameIndex] = rtcRoundtripPCMFrame(frameIndex)
		if err := feed.WriteFrame(ctx, wantFrames[frameIndex]); err != nil {
			t.Fatalf("feed virtual microphone frame %d: %v", frameIndex, err)
		}
	}
	return wantFrames
}

func readRTCDeviceRoundtripFrames(t *testing.T, ctx context.Context, observe *devicegw.DeviceSource) [][]int16 {
	t.Helper()
	gotFrames := make([][]int16, rtcRoundtripFrameCount)
	for frameIndex := range gotFrames {
		frame := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(ctx, frame); err != nil {
			t.Fatalf("observe virtual speaker frame %d: %v", frameIndex, err)
		}
		gotFrames[frameIndex] = frame
		if pcmAbsoluteEnergy(frame) == 0 {
			t.Fatalf("observed virtual speaker frame %d has no emitted audio energy", frameIndex)
		}
	}
	return gotFrames
}

func closeRTCDeviceRoundtripResources(t *testing.T, providerSession messages.Session, binding runtimedevices.RTCBinding, feed *devicegw.DeviceSink, observe *devicegw.DeviceSource) {
	t.Helper()
	if err := providerSession.Close(); err != nil {
		t.Fatalf("close RTC device binding: %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("repeat close RTC device binding: %v", err)
	}
	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close virtual speaker observer: %v", err)
	}
}

func startRTCDeviceSession(ctx context.Context, inferencer messages.SessionInferencer, registry *devicegw.VirtualRegistry) <-chan error {
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- agentruntime.RunSession(ctx, io.Discard, agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(),
			ReplayPath: "synthetic.json", SessionInferencer: inferencer,
			DeviceService: newTestDeviceService(registry),
			RTCBinding: runtimedevices.RTCBindingRequest{
				InputDevice: rtcRoundtripInputID, OutputDevice: rtcRoundtripOutputID,
				InputPresent: true, OutputPresent: true,
			},
		})
	}()
	return runErrCh
}

func waitForRTCDeviceSession(t *testing.T, ctx context.Context, inferencer *runtimeRTCSessionInferencer) *runtimeRTCSession {
	t.Helper()
	select {
	case session := <-inferencer.connected:
		return session
	case <-ctx.Done():
		t.Fatalf("provider session did not connect: %v", ctx.Err())
		return nil
	}
}

func assertRTCDeviceRuntimeFrames(t *testing.T, ctx context.Context, observe *devicegw.DeviceSource, wantFrames [][]int16) {
	t.Helper()
	for frameIndex, want := range wantFrames {
		got := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(ctx, got); err != nil {
			t.Fatalf("observe virtual speaker frame %d: %v", frameIndex, err)
		}
		if pcmAbsoluteEnergy(got) == 0 {
			t.Fatalf("observed virtual speaker frame %d has no emitted audio energy", frameIndex)
		}
		for sampleIndex := range want {
			if got[sampleIndex] != want[sampleIndex] {
				t.Fatalf("speaker frame %d sample %d = %d, want %d", frameIndex, sampleIndex, got[sampleIndex], want[sampleIndex])
			}
		}
	}
}

func awaitRTCDeviceSession(t *testing.T, ctx context.Context, runErrCh <-chan error) {
	t.Helper()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("RunSession with runtime RTC pumps: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not finish after provider close: %v", ctx.Err())
	}
}

func newRTCDeviceRoundtripRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "mic-in", Name: "Virtual Microphone", Direction: devicegw.DirectionInput, LoopbackID: "mic-feed"},
			{ID: "mic-feed", Name: "Virtual Microphone Feeder", Direction: devicegw.DirectionOutput, LoopbackID: "mic-in"},
			{ID: "speaker-out", Name: "Virtual Speaker", Direction: devicegw.DirectionOutput, LoopbackID: "speaker-observe"},
			{ID: "speaker-observe", Name: "Virtual Speaker Observer", Direction: devicegw.DirectionInput, LoopbackID: "speaker-out"},
		},
		Defaults: map[devicegw.Direction]string{
			devicegw.DirectionInput:  "mic-in",
			devicegw.DirectionOutput: "speaker-out",
		},
	})
	if err != nil {
		t.Fatalf("new RTC roundtrip virtual registry: %v", err)
	}
	return registry
}

func assertRTCDeviceRoundtripIDs(t *testing.T, registry *devicegw.VirtualRegistry) {
	t.Helper()
	devices, err := registry.List()
	if err != nil {
		t.Fatalf("list roundtrip virtual devices: %v", err)
	}
	if len(devices) != 4 {
		t.Fatalf("roundtrip virtual devices = %d, want 4", len(devices))
	}
	for _, device := range devices {
		backend, nativeID, err := devicegw.ParseDeviceID(device.ID)
		if err != nil {
			t.Fatalf("parse device ID %q: %v", device.ID, err)
		}
		if backend != devicegw.VirtualBackendName || nativeID == "" {
			t.Fatalf("device ID %q = backend %q/native %q, want virtual:<native-id>", device.ID, backend, nativeID)
		}
	}
}

func rtcRoundtripPCMFrame(index int) []int16 {
	frame := make([]int16, audio.FrameSize)
	for sampleIndex := range frame {
		frame[sampleIndex] = int16(((index+1)*701+sampleIndex*37)%24000 - 12000) //nolint:gosec // bounded test tone
	}
	return frame
}

func pcmAbsoluteEnergy(samples []int16) int64 {
	var energy int64
	for _, sample := range samples {
		if sample < 0 {
			energy -= int64(sample)
		} else {
			energy += int64(sample)
		}
	}
	return energy
}

type loopbackRTCTrackPeer struct {
	frames chan audio.PCMFrame
	closed chan struct{}
	once   sync.Once

	mu     sync.Mutex
	writes int
	reads  int
	energy int64
}

func newLoopbackRTCTrackPeer(frameCount int) *loopbackRTCTrackPeer {
	return &loopbackRTCTrackPeer{
		frames: make(chan audio.PCMFrame, frameCount),
		closed: make(chan struct{}),
	}
}

func (p *loopbackRTCTrackPeer) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(frame.Samples) != audio.FrameSize {
		return fmt.Errorf("loopback outbound frame has %d samples, want %d", len(frame.Samples), audio.FrameSize)
	}
	owned := audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}
	select {
	case p.frames <- owned:
		p.mu.Lock()
		p.writes++
		p.energy += pcmAbsoluteEnergy(owned.Samples)
		p.mu.Unlock()
		return nil
	case <-p.closed:
		return rtc.ErrPeerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *loopbackRTCTrackPeer) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame := <-p.frames:
		if len(frame.Samples) != audio.FrameSize {
			return audio.PCMFrame{}, fmt.Errorf("loopback inbound frame has %d samples, want %d", len(frame.Samples), audio.FrameSize)
		}
		p.mu.Lock()
		p.reads++
		p.mu.Unlock()
		return audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}, nil
	case <-p.closed:
		return audio.PCMFrame{}, rtc.ErrPeerClosed
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (p *loopbackRTCTrackPeer) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

type loopbackRTCTrackPeerStats struct {
	Writes int
	Reads  int
	Energy int64
}

func (p *loopbackRTCTrackPeer) Stats() loopbackRTCTrackPeerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return loopbackRTCTrackPeerStats{Writes: p.writes, Reads: p.reads, Energy: p.energy}
}

var _ audio.InboundMedia = (*loopbackRTCTrackPeer)(nil)
var _ audio.OutboundMedia = (*loopbackRTCTrackPeer)(nil)
