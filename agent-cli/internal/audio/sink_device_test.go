package audio

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeviceSinkVirtualFramesAndLoss(t *testing.T) {
	r := adapterTestRegistry(t)
	sink, err := NewDeviceSink(r, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close(); _ = source.Close() }()
	var sinkContract AudioSink = sink
	var sourceContract AudioSource = source
	wants := make([][]int16, 3)
	for i := range wants {
		wants[i] = make([]int16, FrameSize)
		wants[i][0], wants[i][1], wants[i][FrameSize-1] = int16(i+1), -32768, 32767-int16(i)
		if err := sinkContract.WriteFrame(context.Background(), wants[i]); err != nil {
			t.Fatal(err)
		}
		wants[i][0] = 99
	}
	for i := range wants {
		got := make([]int16, FrameSize)
		if err := sourceContract.ReadFrame(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		want := append([]int16(nil), wants[i]...)
		want[0] = int16(i + 1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frame %d changed, reordered, or crossed a boundary", i)
		}
	}
	if !r.RemoveDevice("virtual:output") {
		t.Fatal("RemoveDevice(output) = false")
	}
	err = sinkContract.WriteFrame(context.Background(), make([]int16, FrameSize))
	var lost *DeviceLostError
	if !errors.As(err, &lost) || lost.ID != "virtual:output" || lost.Direction != DirectionOutput || !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("lost WriteFrame = %v, want typed output loss", err)
	}
}

func TestDeviceSinkFrameHandleConformance(t *testing.T) {
	handle := &adapterFrameHandle{direction: DirectionOutput}
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	for i := 1; i <= 3; i++ {
		frame := make([]int16, FrameSize)
		frame[0], frame[FrameSize-1] = int16(i), int16(32767-i)
		if err := sink.WriteFrame(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		frame[0] = 99
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if len(handle.writes) != 3 {
		t.Fatalf("native frame writes=%d, want 3", len(handle.writes))
	}
	for i, frame := range handle.writes {
		if frame[0] != int16(i+1) || frame[FrameSize-1] != int16(32766-i) {
			t.Fatalf("native frame %d = (%d,%d), want (%d,%d)", i, frame[0], frame[FrameSize-1], i+1, 32766-i)
		}
	}
}

func TestDeviceSinkWriteSamplesQueuesExactPartialChunk(t *testing.T) {
	r := adapterTestRegistry(t)
	sink, err := NewDeviceSink(r, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.WriteSamples(cancelled, []int16{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled WriteSamples = %v, want context.Canceled", err)
	}
	if err := sink.WriteSamples(context.Background(), nil); err != nil {
		t.Fatalf("empty WriteSamples: %v", err)
	}
	samples := make([]int16, 320)
	sample := int16(1)
	for index := range samples {
		samples[index] = sample
		sample++
	}
	if err := sink.WriteSamples(context.Background(), samples); err != nil {
		t.Fatalf("partial WriteSamples: %v", err)
	}
	samples[0] = -1
	if got := sink.PlaybackStats().QueuedSamples; got != 320 {
		t.Fatalf("queued partial samples = %d, want 320", got)
	}
	if got := sink.DiscardPlayback(); got != 320 {
		t.Fatalf("discarded partial samples = %d, want 320", got)
	}

	frameOnly := &adapterFrameHandle{direction: DirectionOutput}
	frameSink, err := NewDeviceSink(&adapterTestRegistryStub{handle: frameOnly}, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = frameSink.Close() }()
	if err := frameSink.WriteSamples(context.Background(), make([]int16, 320)); !errors.Is(err, ErrInvalidFrameSize) {
		t.Fatalf("partial write to frame-only device = %v, want ErrInvalidFrameSize", err)
	}
	if err := frameSink.WriteSamples(context.Background(), make([]int16, FrameSize)); err != nil {
		t.Fatalf("full WriteSamples to frame-only device: %v", err)
	}
	if len(frameOnly.writes) != 1 {
		t.Fatalf("full WriteSamples frame writes = %d, want 1", len(frameOnly.writes))
	}

	byteOnly := &adapterSinkByteHandle{direction: DirectionOutput}
	byteSink, err := NewDeviceSink(&adapterTestRegistryStub{handle: byteOnly}, "output")
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	if err := byteSink.WriteSamples(nilContext, []int16{1, -2, 32767}); err != nil {
		t.Fatalf("partial byte-device WriteSamples: %v", err)
	}
	if want := []byte{1, 0, 254, 255, 255, 127}; !reflect.DeepEqual(byteOnly.data, want) {
		t.Fatalf("partial byte-device PCM = %v, want %v", byteOnly.data, want)
	}
	if err := byteSink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := byteSink.WriteSamples(context.Background(), []int16{1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteSamples after Close = %v, want ErrClosed", err)
	}
}

func TestDeviceSinkFormatCapabilitiesAndExplicitFormatFailures(t *testing.T) {
	legacyHandle := &adapterFrameHandle{direction: DirectionOutput}
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: legacyHandle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	stats := sink.PlaybackStats()
	if stats.Format != DefaultDeviceFormat() || stats.CapacitySamples != 4000 {
		t.Fatalf("legacy sink playback stats = %+v, want default format and 4000 samples", stats)
	}
	if got := sink.DiscardPlayback(); got != 0 {
		t.Fatalf("legacy sink DiscardPlayback = %d, want 0", got)
	}
	if got := sink.SampleRate(); got != SampleRate {
		t.Fatalf("legacy sink SampleRate = %d, want %d", got, SampleRate)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	unsupportedHandle := &adapterFrameHandle{direction: DirectionOutput}
	_, err = NewDeviceSinkAtRate(&adapterTestRegistryStub{handle: unsupportedHandle}, "output", 24000)
	var formatErr *DeviceFormatError
	if !errors.As(err, &formatErr) || !errors.Is(err, ErrUnsupportedDeviceFormat) || !strings.Contains(err.Error(), "24000 Hz") {
		t.Fatalf("explicit format without opener error = %v, want typed 24000 Hz mismatch", err)
	}
	if unsupportedHandle.closed {
		t.Fatalf("unsupported format path opened legacy handle, want no open")
	}

	var nilSink *DeviceSink
	if nilSink.DeviceFormat() != (DeviceFormat{}) || nilSink.SampleRate() != 0 || nilSink.PlaybackStats() != (PlaybackQueueStats{}) || nilSink.DiscardPlayback() != 0 {
		t.Fatal("nil sink format/playback methods returned non-zero state")
	}
}

func TestDeviceSinkExplicitOpenerValidatesOpenedFormat(t *testing.T) {
	want := PCM16DeviceFormat(24000)
	goodHandle := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: want}
	goodRegistry := &adapterFormatRegistryStub{handle: goodHandle}
	sink, err := NewDeviceSinkAtRate(goodRegistry, "output", want.SampleRate)
	if err != nil {
		t.Fatalf("matching explicit format open: %v", err)
	}
	if goodRegistry.requested != want || sink.DeviceFormat() != want {
		t.Fatalf("explicit opener request/sink format = %v/%v, want %v", goodRegistry.requested, sink.DeviceFormat(), want)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	actual := DefaultDeviceFormat()
	mismatchHandle := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: actual}
	_, err = NewDeviceSinkAtRate(&adapterFormatRegistryStub{handle: mismatchHandle}, "output", want.SampleRate)
	var formatErr *DeviceFormatError
	if !errors.As(err, &formatErr) || formatErr.Requested != want || len(formatErr.Available) != 1 || formatErr.Available[0] != actual {
		t.Fatalf("opened format mismatch error = %v, want requested/actual formats", err)
	}
	if mismatchHandle.closeCount != 1 {
		t.Fatalf("mismatched opened handle close count = %d, want 1", mismatchHandle.closeCount)
	}
}

func TestDeviceSinkContractsAndConcurrentClose(t *testing.T) {
	if _, err := NewDeviceSink(nil, "output"); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v", err)
	}
	r := adapterTestRegistry(t)
	for _, id := range []DeviceID{"virtual:missing", "virtual:input"} {
		if _, err := NewDeviceSink(r, id); err == nil {
			t.Fatalf("NewDeviceSink(%q) succeeded", id)
		}
	}
	bare := &adapterBareHandle{direction: DirectionOutput}
	if _, err := NewDeviceSink(&adapterTestRegistryStub{handle: bare}, "output"); !errors.Is(err, ErrDeviceCapabilityMismatch) || bare.closed != 1 {
		t.Fatalf("missing capability error=%v close=%d", err, bare.closed)
	}
	handle := &adapterFrameHandle{direction: DirectionOutput}
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.WriteFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled WriteFrame = %v", err)
	}
	if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize-1)); !errors.Is(err, ErrInvalidFrameSize) {
		t.Fatalf("invalid WriteFrame = %v", err)
	}
	if len(handle.writes) != 0 {
		t.Fatalf("validation wrote %d frames", len(handle.writes))
	}
	if err := sink.Close(); err != nil || sink.Close() != nil {
		t.Fatalf("idempotent sink close: %v", err)
	}
	var nilContext context.Context
	if err := sink.WriteFrame(nilContext, make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteFrame after Close = %v", err)
	}
	var nilSink *DeviceSink
	if nilSink.Close() != nil {
		t.Fatal("nil sink Close returned an error")
	}

	blocking := newBlockingDeviceHandle(DirectionOutput)
	sink, err = NewDeviceSink(&adapterTestRegistryStub{handle: blocking}, "output")
	if err != nil {
		t.Fatal(err)
	}
	writes := make(chan error, 1)
	go func() { writes <- sink.WriteFrame(context.Background(), make([]int16, FrameSize)) }()
	waitDeviceSignal(t, blocking.started)
	closeDone := make(chan error, 1)
	go func() { closeDone <- sink.Close() }()
	if err := waitDeviceError(t, writes); !errors.Is(err, ErrClosed) {
		t.Fatalf("concurrent WriteFrame = %v", err)
	}
	if err := waitDeviceError(t, closeDone); err != nil {
		t.Fatalf("concurrent Close = %v", err)
	}
}

func TestDeviceAdaptersS9LifecycleBaseline(t *testing.T) {
	beforeHandles := processOpenHandleCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	r := adapterTestRegistry(t)
	const iterations = 16
	for range iterations {
		source, err := NewDeviceSource(r, "virtual:input")
		if err != nil {
			t.Fatal(err)
		}
		sink, err := NewDeviceSink(r, "virtual:output")
		if err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Observations()
	if got.OpenCount != iterations*2 || got.ReleaseCount != iterations*2 {
		t.Fatalf("lifecycle observations=%+v, want %d opens and releases", got, iterations*2)
	}
	assertHandleCountWithinTolerance(t, settledProcessOpenHandleCount(t, beforeHandles), beforeHandles, "device source/sink lifecycle")
	deadline := time.Now().Add(500 * time.Millisecond)
	for current := runtime.NumGoroutine(); current > beforeGoroutines+2 && time.Now().Before(deadline); current = runtime.NumGoroutine() {
		runtime.Gosched()
	}
	if current := runtime.NumGoroutine(); current > beforeGoroutines+2 {
		t.Fatalf("goroutines after lifecycle=%d, want <= %d", current, beforeGoroutines+2)
	}
}

type blockingDeviceHandle struct {
	direction Direction
	started   chan struct{}
	released  chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

type adapterFormatHandle struct {
	*adapterFrameHandle
	format     DeviceFormat
	closeCount int
}

type adapterSinkByteHandle struct {
	direction Direction
	data      []byte
}

func (h *adapterSinkByteHandle) DeviceDirection() Direction { return h.direction }
func (h *adapterSinkByteHandle) Write(_ context.Context, data []byte) error {
	h.data = append([]byte(nil), data...)
	return nil
}
func (h *adapterSinkByteHandle) Close() error { return nil }

func (h *adapterFormatHandle) DeviceFormat() DeviceFormat { return h.format }
func (h *adapterFormatHandle) Close() error {
	h.closeCount++
	return h.adapterFrameHandle.Close()
}

type adapterFormatRegistryStub struct {
	handle    OpenedDevice
	requested DeviceFormat
}

func (r *adapterFormatRegistryStub) List() ([]Device, error) { return nil, nil }
func (r *adapterFormatRegistryStub) Default(Direction) (Device, error) {
	return Device{ID: "output", Direction: DirectionOutput}, nil
}
func (r *adapterFormatRegistryStub) Open(DeviceID) (OpenedDevice, error) { return r.handle, nil }
func (r *adapterFormatRegistryStub) OpenWithFormat(_ DeviceID, format DeviceFormat) (OpenedDevice, error) {
	r.requested = format
	return r.handle, nil
}

func newBlockingDeviceHandle(direction Direction) *blockingDeviceHandle {
	return &blockingDeviceHandle{direction: direction, started: make(chan struct{}), released: make(chan struct{})}
}
func (h *blockingDeviceHandle) DeviceDirection() Direction { return h.direction }
func (h *blockingDeviceHandle) WriteFrame(ctx context.Context, _ []int16) error {
	h.startOnce.Do(func() { close(h.started) })
	select {
	case <-h.released:
		return ErrClosed
	case <-ctx.Done():
		return contextError(ctx)
	}
}
func (h *blockingDeviceHandle) Close() error {
	h.closeOnce.Do(func() { close(h.released) })
	return nil
}
