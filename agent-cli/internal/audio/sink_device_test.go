package audio

import (
	"context"
	"errors"
	"reflect"
	"runtime"
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
	if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteFrame after Close = %v", err)
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
