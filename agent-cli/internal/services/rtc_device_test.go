package services

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestRTCDeviceSourceDefaultPumpsFixedFramesToOutboundMedia(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := audio.NewDeviceSink(registry, "")
	if err != nil {
		t.Fatalf("open output default: %v", err)
	}
	defer func() { _ = sink.Close() }()
	if got := sink.DeviceID(); got != "virtual:output" {
		t.Fatalf("sink DeviceID = %q, want virtual:output", got)
	}

	source, err := NewDefaultRTCDeviceSource(registry)
	if err != nil {
		t.Fatalf("open input default: %v", err)
	}
	defer func() { _ = source.Close() }()
	if got := source.DeviceID(); got != "virtual:input" {
		t.Fatalf("source DeviceID = %q, want virtual:input", got)
	}

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(index%97 - 48)
	}
	if err := sink.WriteFrame(context.Background(), want); err != nil {
		t.Fatalf("script virtual input: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbound := &recordingRTCOutboundMedia{cancelAfterFirst: cancel}
	err = source.Pump(ctx, outbound)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Pump error = %v, want context.Canceled after one frame", err)
	}
	if len(outbound.frames) != 1 {
		t.Fatalf("outbound frame count = %d, want 1", len(outbound.frames))
	}
	if len(outbound.frames[0].Samples) != audio.FrameSize {
		t.Fatalf("outbound sample count = %d, want %d", len(outbound.frames[0].Samples), audio.FrameSize)
	}
	if !reflect.DeepEqual(outbound.frames[0].Samples, want) {
		t.Fatal("outbound frame differs from the registry-backed input frame")
	}
}

func TestRTCDeviceSourcePreservesRegistryErrorIdentity(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRTCDeviceSource(registry, "virtual:missing")
	var notFound *audio.DeviceNotFoundError
	if !errors.As(err, &notFound) || notFound.ID != "virtual:missing" || !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatalf("missing device error = %v, want typed ID-preserving not-found", err)
	}

	var nilRegistry *audio.VirtualRegistry
	_, err = NewRTCDeviceSource(nilRegistry, "virtual:input")
	var registryError *audio.DeviceAdapterError
	if !errors.As(err, &registryError) || !errors.Is(err, audio.ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v, want typed nil-registry error", err)
	}
	if registryError.ID != "virtual:input" || registryError.Direction != audio.DirectionInput {
		t.Fatalf("nil registry identity = %#v, want input virtual:input", registryError)
	}

	noDefaultConfig := audio.DefaultVirtualBackendConfig()
	delete(noDefaultConfig.Defaults, audio.DirectionInput)
	noDefaultRegistry, err := audio.NewVirtualRegistry(noDefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDefaultRTCDeviceSource(noDefaultRegistry)
	var noDefault *audio.NoDefaultDeviceError
	if !errors.As(err, &noDefault) || noDefault.Direction != audio.DirectionInput || !errors.Is(err, audio.ErrNoDefaultDevice) {
		t.Fatalf("missing input default error = %v, want typed no-default error", err)
	}
}

func TestRTCDeviceSourceCloseReleasesInputExactlyOnce(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewRTCDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := source.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	observations := registry.Observations()
	if observations.OpenCount != 1 || observations.ReleaseCount != 1 {
		t.Fatalf("device lifecycle observations = %+v, want one open and one release", observations)
	}
	if err := source.Pump(context.Background(), &recordingRTCOutboundMedia{}); !errors.Is(err, ErrRTCDeviceSourceClosed) {
		t.Fatalf("Pump after Close = %v, want ErrRTCDeviceSourceClosed", err)
	}
}

func TestRTCDeviceSourceCloseStopsBlockedPump(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewRTCDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	outbound := &recordingRTCOutboundMedia{}
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- source.Pump(context.Background(), outbound) }()

	if err := source.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-pumpDone:
		if !errors.Is(err, audio.ErrClosed) && !errors.Is(err, ErrRTCDeviceSourceClosed) {
			t.Fatalf("blocked Pump error = %v, want a close identity", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Pump did not stop after Close")
	}
	if got := registry.Observations().ReleaseCount; got != 1 {
		t.Fatalf("release count = %d, want 1", got)
	}
}

func TestRTCDeviceSourcePreservesOutboundWriteError(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := audio.NewDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	source, err := NewRTCDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err := sink.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("track write failed")
	err = source.Pump(context.Background(), &recordingRTCOutboundMedia{writeErr: wantErr})
	var sourceErr *RTCDeviceSourceError
	if !errors.As(err, &sourceErr) || sourceErr.Operation != "write" || !errors.Is(err, wantErr) {
		t.Fatalf("outbound write error = %v, want wrapped operation and sentinel", err)
	}
}

type recordingRTCOutboundMedia struct {
	mu               sync.Mutex
	frames           []rtc.PCMFrame
	writeErr         error
	cancelAfterFirst context.CancelFunc
}

func (m *recordingRTCOutboundMedia) WriteFrame(_ context.Context, frame rtc.PCMFrame) error {
	m.mu.Lock()
	copyFrame := rtc.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}
	m.frames = append(m.frames, copyFrame)
	cancel := m.cancelAfterFirst
	if len(m.frames) == 1 {
		m.cancelAfterFirst = nil
	} else {
		cancel = nil
	}
	err := m.writeErr
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}

func (m *recordingRTCOutboundMedia) Close() error { return nil }

var _ rtc.OutboundMedia = (*recordingRTCOutboundMedia)(nil)
