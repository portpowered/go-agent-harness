package services

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestRTCDeviceSinkDefaultPumpsInboundFramesToOutput(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}

	source, err := audio.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatalf("open input default: %v", err)
	}
	defer func() { _ = source.Close() }()

	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatalf("open output default: %v", err)
	}
	defer func() { _ = sink.Close() }()
	if got := sink.DeviceID(); got != "virtual:output" {
		t.Fatalf("sink DeviceID = %q, want virtual:output", got)
	}

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(index%113 - 56)
	}
	inbound := &recordingRTCInboundMedia{frames: []rtc.PCMFrame{{Samples: want}}}
	if err := sink.Pump(context.Background(), inbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if inbound.closeCount() != 0 {
		t.Fatal("Pump closed the caller-owned inbound endpoint")
	}

	got := make([]int16, audio.FrameSize)
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := source.ReadFrame(readCtx, got); err != nil {
		t.Fatalf("read looped-back output: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("output device received samples different from inbound RTC frame")
	}
	if !hasNonZeroSamples(got) {
		t.Fatal("output device received no emitted audio energy")
	}
}

func TestRTCDeviceSinkPreservesRegistryErrorIdentity(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewRTCDeviceSink(registry, "virtual:missing")
	var notFound *audio.DeviceNotFoundError
	if !errors.As(err, &notFound) || notFound.ID != "virtual:missing" || !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatalf("missing device error = %v, want typed ID-preserving not-found", err)
	}

	_, err = NewRTCDeviceSink(registry, "virtual:input")
	var direction *audio.DeviceDirectionError
	if !errors.As(err, &direction) || direction.ID != "virtual:input" || direction.Want != audio.DirectionOutput || direction.Got != audio.DirectionInput || !errors.Is(err, audio.ErrDeviceDirectionMismatch) {
		t.Fatalf("wrong-direction error = %v, want typed output-direction mismatch", err)
	}

	opened, err := registry.Open("virtual:exclusive")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	_, err = NewRTCDeviceSink(registry, "virtual:exclusive")
	var inUse *audio.DeviceInUseError
	if !errors.As(err, &inUse) || inUse.ID != "virtual:exclusive" || !errors.Is(err, audio.ErrDeviceInUse) {
		t.Fatalf("in-use error = %v, want typed exclusive-open error", err)
	}

	var nilRegistry *audio.VirtualRegistry
	_, err = NewRTCDeviceSink(nilRegistry, "virtual:output")
	var registryError *audio.DeviceAdapterError
	if !errors.As(err, &registryError) || registryError.ID != "virtual:output" || registryError.Direction != audio.DirectionOutput || !errors.Is(err, audio.ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v, want typed output nil-registry error", err)
	}

	noDefaultConfig := audio.DefaultVirtualBackendConfig()
	delete(noDefaultConfig.Defaults, audio.DirectionOutput)
	noDefaultRegistry, err := audio.NewVirtualRegistry(noDefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDefaultRTCDeviceSink(noDefaultRegistry)
	var noDefault *audio.NoDefaultDeviceError
	if !errors.As(err, &noDefault) || noDefault.Direction != audio.DirectionOutput || !errors.Is(err, audio.ErrNoDefaultDevice) {
		t.Fatalf("missing output default error = %v, want typed no-default error", err)
	}
}

func TestRTCDeviceSinkCloseReleasesOutputExactlyOnce(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := sink.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	observations := registry.Observations()
	if observations.OpenCount != 1 || observations.ReleaseCount != 1 {
		t.Fatalf("device lifecycle observations = %+v, want one open and one release", observations)
	}
	if err := sink.Pump(context.Background(), &recordingRTCInboundMedia{}); !errors.Is(err, ErrRTCDeviceSinkClosed) {
		t.Fatalf("Pump after Close = %v, want ErrRTCDeviceSinkClosed", err)
	}
}

func TestRTCDeviceSinkContextCancellationStopsPlaybackAndReleasesOnce(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}

	source, err := audio.NewDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}

	want := make([]int16, audio.FrameSize)
	for index := range want {
		want[index] = int16(index%29 + 1)
	}
	inbound := &blockingRTCInboundMedia{first: rtc.PCMFrame{Samples: want}}
	ctx, cancel := context.WithCancel(context.Background())
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(ctx, inbound) }()

	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	if err := source.ReadFrame(readCtx, got); err != nil {
		readCancel()
		cancel()
		_ = sink.Close()
		t.Fatalf("read looped-back playback: %v", err)
	}
	readCancel()
	if !reflect.DeepEqual(got, want) {
		cancel()
		_ = sink.Close()
		t.Fatal("playback frame differs from inbound RTC frame")
	}

	cancel()
	select {
	case err := <-pumpDone:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrRTCDeviceSinkClosed) {
			t.Fatalf("cancelled Pump error = %v, want a cancellation identity", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop after context cancellation")
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close after cancellation: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("repeated Close after cancellation: %v", err)
	}
	if got := registry.Observations().ReleaseCount; got != 1 {
		t.Fatalf("sink release count = %d, want 1", got)
	}
}

func TestRTCDeviceSinkCloseStopsBlockedPump(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	inbound := newBlockingRTCInboundMedia()
	inbound.first = rtc.PCMFrame{Samples: make([]int16, audio.FrameSize)}
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(context.Background(), inbound) }()

	select {
	case <-inbound.started:
	case <-time.After(time.Second):
		t.Fatal("Pump did not start reading inbound media")
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-pumpDone:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, audio.ErrClosed) && !errors.Is(err, ErrRTCDeviceSinkClosed) {
			t.Fatalf("blocked Pump error = %v, want a close identity", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Pump did not stop after Close")
	}
	if got := registry.Observations().ReleaseCount; got != 1 {
		t.Fatalf("sink release count = %d, want 1", got)
	}
}

func TestRTCDeviceSinkPreservesInboundAndDeviceErrors(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}

	wantInboundErr := errors.New("track read failed")
	err = sink.Pump(context.Background(), &recordingRTCInboundMedia{err: wantInboundErr})
	var sinkErr *RTCDeviceSinkError
	if !errors.As(err, &sinkErr) || sinkErr.Operation != "read" || !errors.Is(err, wantInboundErr) {
		t.Fatalf("inbound read error = %v, want wrapped operation and sentinel", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	registry, err = audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err = NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	if !registry.RemoveDevice("virtual:output") {
		t.Fatal("RemoveDevice(output) = false")
	}
	err = sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: []rtc.PCMFrame{{Samples: make([]int16, audio.FrameSize)}}})
	if !errors.As(err, &sinkErr) || sinkErr.Operation != "write" || !errors.Is(err, audio.ErrDeviceLost) {
		t.Fatalf("device write error = %v, want wrapped device-loss error", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRTCDeviceSinkRejectsNilInboundMedia(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()

	var inbound *recordingRTCInboundMedia
	if err := sink.Pump(context.Background(), inbound); !errors.Is(err, ErrNilRTCInboundMedia) {
		t.Fatalf("typed nil inbound = %v, want ErrNilRTCInboundMedia", err)
	}
}

type recordingRTCInboundMedia struct {
	mu              sync.Mutex
	frames          []rtc.PCMFrame
	index           int
	err             error
	closeCountValue int
}

func (m *recordingRTCInboundMedia) ReadFrame(context.Context) (rtc.PCMFrame, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index < len(m.frames) {
		frame := m.frames[m.index]
		m.index++
		return frame, nil
	}
	if m.err != nil {
		return rtc.PCMFrame{}, m.err
	}
	return rtc.PCMFrame{}, io.EOF
}

func (m *recordingRTCInboundMedia) Close() error {
	m.mu.Lock()
	m.closeCountValue++
	m.mu.Unlock()
	return nil
}

func (m *recordingRTCInboundMedia) closeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeCountValue
}

type blockingRTCInboundMedia struct {
	mu        sync.Mutex
	first     rtc.PCMFrame
	returned  bool
	started   chan struct{}
	startOnce sync.Once
}

func (m *blockingRTCInboundMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	if m.started != nil {
		m.startOnce.Do(func() { close(m.started) })
	}
	m.mu.Lock()
	if !m.returned {
		m.returned = true
		frame := m.first
		m.mu.Unlock()
		return frame, nil
	}
	m.mu.Unlock()
	<-ctx.Done()
	return rtc.PCMFrame{}, ctx.Err()
}

func (m *blockingRTCInboundMedia) Close() error { return nil }

func newBlockingRTCInboundMedia() *blockingRTCInboundMedia {
	return &blockingRTCInboundMedia{started: make(chan struct{})}
}

func hasNonZeroSamples(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

var _ rtc.InboundMedia = (*recordingRTCInboundMedia)(nil)
var _ rtc.InboundMedia = (*blockingRTCInboundMedia)(nil)
