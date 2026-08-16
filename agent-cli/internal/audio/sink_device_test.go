package audio

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
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
	defer func() { _ = sink.Close(); _ = source.Close() }()

	wants := make([][]int16, 3)
	for frameIndex := range wants {
		wants[frameIndex] = make([]int16, FrameSize)
		wants[frameIndex][0] = int16(frameIndex + 1)
		wants[frameIndex][1] = -32768
		wants[frameIndex][FrameSize-1] = 32767 - int16(frameIndex)
		if err := sink.WriteFrame(context.Background(), wants[frameIndex]); err != nil {
			t.Fatal(err)
		}
		wants[frameIndex][0] = 99
	}
	for frameIndex := range wants {
		got := make([]int16, FrameSize)
		if err := source.ReadFrame(context.Background(), got); err != nil {
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
	err = sink.WriteFrame(context.Background(), make([]int16, FrameSize))
	var lost *DeviceLostError
	if !errors.As(err, &lost) || lost.ID != "virtual:output" || lost.Direction != DirectionOutput || !errors.Is(err, ErrDeviceLost) || errors.Is(err, io.EOF) {
		t.Fatalf("lost WriteFrame = %v, want typed output loss", err)
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
