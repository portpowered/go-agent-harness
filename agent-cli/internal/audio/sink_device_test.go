package audio

import (
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestDeviceSinkVirtualExactFramesAndLoss(t *testing.T) {
	r := adapterTestRegistry(t)
	sink, err := NewDeviceSink(r, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	var sinkContract AudioSink = sink
	var sourceContract AudioSource = source
	defer func() { _ = sinkContract.Close(); _ = sourceContract.Close() }()

	wants := make([][]int16, 3)
	for frameIndex := range wants {
		wants[frameIndex] = make([]int16, FrameSize)
		wants[frameIndex][0] = int16(frameIndex + 1)
		wants[frameIndex][1] = -32768
		wants[frameIndex][FrameSize-1] = 32767 - int16(frameIndex)
		if err := sinkContract.WriteFrame(context.Background(), wants[frameIndex]); err != nil {
			t.Fatal(err)
		}
		wants[frameIndex][0] = 99
	}
	for frameIndex := range wants {
		got := make([]int16, FrameSize)
		if err := sourceContract.ReadFrame(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		want := append([]int16(nil), wants[frameIndex]...)
		want[0] = int16(frameIndex + 1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frame %d mismatch at boundaries or samples", frameIndex)
		}
	}

	if !r.RemoveDevice("virtual:output") {
		t.Fatal("RemoveDevice(output) = false")
	}
	err = sinkContract.WriteFrame(context.Background(), make([]int16, FrameSize))
	var lost *DeviceLostError
	if !errors.As(err, &lost) || lost.ID != "virtual:output" || lost.Direction != DirectionOutput || !errors.Is(err, ErrDeviceLost) || errors.Is(err, io.EOF) {
		t.Fatalf("lost WriteFrame = %v, want typed output loss", err)
	}
}

func TestDeviceSinkS11Conformance(t *testing.T) {
	handle := &adapterFrameHandle{direction: DirectionOutput}
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	var contract AudioSink = sink
	wants := make([][]int16, 3)
	for i := range wants {
		wants[i] = make([]int16, FrameSize)
		wants[i][0], wants[i][FrameSize-1] = int16(i+1), int16(32767-i)
		if err := contract.WriteFrame(context.Background(), wants[i]); err != nil {
			t.Fatal(err)
		}
		wants[i][0] = 99
	}
	handle.mu.Lock()
	got := append([][]int16(nil), handle.writes...)
	handle.mu.Unlock()
	if len(got) != len(wants) {
		t.Fatalf("device sink writes=%d, want %d", len(got), len(wants))
	}
	for i := range got {
		want := append([]int16(nil), wants[i]...)
		want[0] = int16(i + 1)
		if !reflect.DeepEqual(got[i], want) {
			t.Fatalf("device sink frame %d changed or reordered", i)
		}
	}
}

func TestDeviceSinkS8ConcurrentWriteClose(t *testing.T) {
	handle := newBlockingWriteHandle()
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- sink.WriteFrame(context.Background(), make([]int16, FrameSize)) }()
	waitDeviceSignal(t, handle.started)
	closeDone := make(chan error, 1)
	go func() { closeDone <- sink.Close() }()
	select {
	case err := <-writeDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("concurrent write after close = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent write did not unblock")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("concurrent Close() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close() did not finish")
	}
}

func TestDeviceAdaptersS9LifecycleBaseline(t *testing.T) {
	beforeHandles := processOpenHandleCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	r := adapterTestRegistry(t)
	const iterations = 32
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
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	}
	observations := r.Observations()
	if observations.OpenCount != iterations*2 || observations.ReleaseCount != iterations*2 {
		t.Fatalf("lifecycle observations=%+v, want %d opens and releases", observations, iterations*2)
	}
	assertHandleCountWithinTolerance(t, settledProcessOpenHandleCount(t, beforeHandles), beforeHandles, "device source/sink lifecycle")
	if got := settledDeviceGoroutineCount(beforeGoroutines); got > beforeGoroutines+goroutineCountSettleTolerance {
		t.Fatalf("goroutines after device source/sink lifecycle=%d, want <= %d", got, beforeGoroutines+goroutineCountSettleTolerance)
	}
}

func TestDeviceSinkLifecycleAndConstructorContracts(t *testing.T) {
	if _, err := NewDeviceSink(nil, "virtual:output"); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v", err)
	}
	r := adapterTestRegistry(t)
	if _, err := NewDeviceSink(r, "virtual:missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("missing sink error = %v", err)
	}
	if _, err := NewDeviceSink(r, "virtual:input"); !errors.Is(err, ErrDeviceDirectionMismatch) {
		t.Fatalf("wrong-direction sink error = %v", err)
	}
	if got := r.Observations(); got.OpenCount != 1 || got.ReleaseCount != 1 {
		t.Fatalf("constructor observations = %+v, want one rejected open released once", got)
	}
	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()

	for range 20 {
		sink, err := NewDeviceSink(r, "virtual:output")
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize)); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
			t.Fatalf("WriteFrame after Close = %v", err)
		}
	}
	got := r.Observations()
	if got.OpenCount != 22 || got.ReleaseCount != 21 {
		t.Fatalf("repeated lifecycle observations = %+v, want 22 opens and 21 releases before source close", got)
	}
}

func TestDeviceSinkValidationAndCapability(t *testing.T) {
	bare := &adapterBareHandle{direction: DirectionOutput}
	if _, err := NewDeviceSink(&adapterTestRegistryStub{handle: bare}, "output"); !errors.Is(err, ErrDeviceCapabilityMismatch) || bare.closed != 1 {
		t.Fatalf("missing sink capability error=%v close=%d", err, bare.closed)
	}
	handle := &adapterRawHandle{direction: DirectionOutput}
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.WriteFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled WriteFrame = %v", err)
	}
	var sizeErr *FrameSizeError
	if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize-1)); !errors.As(err, &sizeErr) {
		t.Fatalf("invalid WriteFrame = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	var nilSink *DeviceSink
	if nilSink.Close() != nil {
		t.Fatal("nil sink close returned an error")
	}
}

type adapterBareHandle struct {
	direction Direction
	closed    int
}

func (h *adapterBareHandle) DeviceDirection() Direction { return h.direction }
func (h *adapterBareHandle) Close() error               { h.closed++; return nil }

type blockingWriteHandle struct {
	direction   Direction
	started     chan struct{}
	startOnce   sync.Once
	released    chan struct{}
	releaseOnce sync.Once
}

func newBlockingWriteHandle() *blockingWriteHandle {
	return &blockingWriteHandle{direction: DirectionOutput, started: make(chan struct{}), released: make(chan struct{})}
}

func (h *blockingWriteHandle) DeviceDirection() Direction { return h.direction }
func (h *blockingWriteHandle) Write(ctx context.Context, _ []byte) error {
	h.startOnce.Do(func() { close(h.started) })
	select {
	case <-h.released:
		return ErrClosed
	case <-ctx.Done():
		return contextError(ctx)
	}
}
func (h *blockingWriteHandle) Close() error {
	h.releaseOnce.Do(func() { close(h.released) })
	return nil
}
