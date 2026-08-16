package audio

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
)

func TestDeviceSourceVirtualFramesLossAndClose(t *testing.T) {
	r := adapterTestRegistry(t)
	handle, err := r.Open("virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	raw := handle.(*VirtualStream)
	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close(); _ = source.Close() }()

	want := make([]int16, FrameSize)
	want[0], want[1], want[FrameSize-1] = -32768, 32767, -1
	if err := raw.Write(context.Background(), pcmBytes(want)); err != nil {
		t.Fatal(err)
	}
	want[0] = 12
	got := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	want[0] = -32768
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadFrame changed after caller mutation: got=%d,%d,%d", got[0], got[1], got[len(got)-1])
	}

	ready, result := make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyContext := &readyContextForDevice{Context: ctx, ready: ready}
	go func() {
		frame := make([]int16, FrameSize)
		err := source.ReadFrame(readyContext, frame)
		result <- err
	}()
	<-ready
	if !r.RemoveDevice("virtual:input") {
		t.Fatal("RemoveDevice(input) = false")
	}
	lost := <-result
	var lostErr *DeviceLostError
	if !errors.As(lost, &lostErr) || lostErr.ID != "virtual:input" || lostErr.Direction != DirectionInput || !errors.Is(lost, ErrDeviceLost) {
		t.Fatalf("pending read error = %v, want typed input loss", lost)
	}
	if errors.Is(lost, context.Canceled) || errors.Is(lost, io.EOF) {
		t.Fatalf("pending read error = %v was normalized incorrectly", lost)
	}

	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadFrame after Close = %v, want ErrClosed", err)
	}
	r = adapterTestRegistry(t)
	source, err = NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	ready, result = make(chan struct{}), make(chan error, 1)
	go func() {
		result <- source.ReadFrame(&readyContextForDevice{Context: context.Background(), ready: ready}, make([]int16, FrameSize))
	}()
	<-ready
	closeErr, readErr := source.Close(), <-result
	if closeErr != nil || !errors.Is(readErr, ErrClosed) {
		t.Fatalf("close pending read: close=%v read=%v", closeErr, readErr)
	}
}

func TestDeviceSourceConstructorContracts(t *testing.T) {
	if _, err := NewDeviceSource(nil, "virtual:input"); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("nil registry error = %v", err)
	}
	r := adapterTestRegistry(t)
	if _, err := NewDeviceSource(r, "virtual:missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := NewDeviceSource(r, "virtual:output"); !errors.Is(err, ErrDeviceDirectionMismatch) {
		t.Fatalf("wrong-direction source error = %v", err)
	}
	if got := r.Observations(); got.OpenCount != 1 || got.ReleaseCount != 1 {
		t.Fatalf("constructor observations = %+v, want one rejected open released once", got)
	}

	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
}

func adapterTestRegistry(t *testing.T) *VirtualRegistry {
	t.Helper()
	r, err := NewVirtualRegistry(DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type readyContextForDevice struct {
	context.Context
	ready chan<- struct{}
	once  sync.Once
}

func (c *readyContextForDevice) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}

func TestDeviceAdapterErrorAndStreamShapes(t *testing.T) {
	var nilError *DeviceAdapterError
	if nilError.Error() != "<nil>" {
		t.Fatal("nil adapter error should be printable")
	}
	directionErr := &DeviceDirectionError{ID: "x", Direction: DirectionInput, Want: DirectionInput, Got: DirectionOutput, Kind: ErrDeviceDirectionMismatch}
	capabilityErr := &DeviceCapabilityError{ID: "x", Direction: DirectionInput, Operation: "read", Kind: ErrDeviceCapabilityMismatch}
	if directionErr.Error() == "" || capabilityErr.Error() == "" || !errors.Is(directionErr, ErrDeviceDirectionMismatch) || !errors.Is(capabilityErr, ErrDeviceCapabilityMismatch) {
		t.Fatal("adapter error shapes are not inspectable")
	}
	var nilRegistry *adapterTestRegistryStub
	if _, err := NewDeviceSource(nilRegistry, "x"); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("typed nil registry error = %v", err)
	}
	missingHandle := &adapterNoDirectionHandle{}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{handle: missingHandle}, "x"); !errors.Is(err, ErrDeviceCapabilityMismatch) || missingHandle.closed != 1 {
		t.Fatalf("missing source capability error=%v close=%d", err, missingHandle.closed)
	}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{}, "x"); !errors.Is(err, ErrNilOpenedDevice) {
		t.Fatalf("nil opened handle error = %v", err)
	}

	wantErr := errors.New("raw read failed")
	for _, raw := range [][]byte{{1}, nil} {
		handle := &adapterRawHandle{direction: DirectionInput, read: func(context.Context) ([]byte, error) { return raw, nil }}
		if raw == nil {
			handle.read = func(context.Context) ([]byte, error) { return nil, wantErr }
		}
		registry := &adapterTestRegistryStub{handle: handle}
		source, err := NewDeviceSource(registry, "input")
		if err != nil {
			t.Fatal(err)
		}
		err = source.ReadFrame(context.Background(), make([]int16, FrameSize))
		if raw == nil && !errors.Is(err, wantErr) {
			t.Fatalf("raw read error = %v", err)
		}
		if raw != nil {
			var sizeErr *FrameSizeError
			if !errors.As(err, &sizeErr) {
				t.Fatalf("short raw frame error = %v", err)
			}
		}
		_ = source.Close()
	}

	lostHandle := &adapterRawHandle{direction: DirectionInput, read: func(context.Context) ([]byte, error) { return nil, ErrDeviceLost }}
	source, err := NewDeviceSource(&adapterTestRegistryStub{handle: lostHandle}, "lost")
	if err != nil {
		t.Fatal(err)
	}
	err = source.ReadFrame(context.Background(), make([]int16, FrameSize))
	var lost *DeviceLostError
	if !errors.As(err, &lost) || lost.ID != "lost" || lost.Direction != DirectionInput {
		t.Fatalf("normalized loss = %v", err)
	}
	_ = source.Close()

	var nilSource *DeviceSource
	if nilSource.Close() != nil {
		t.Fatal("nil source close returned an error")
	}
	if got, ok := openedDeviceDirection(&adapterNoDirectionHandle{}); ok || got != "" {
		t.Fatalf("direction-less handle = %q,%v", got, ok)
	}
	var nilStream *VirtualStream
	if nilStream.DeviceDirection() != "" || (&VirtualStream{}).DeviceDirection() != "" {
		t.Fatal("nil virtual direction should be empty")
	}
}

type adapterTestRegistryStub struct {
	handle OpenedDevice
	err    error
}

func (r *adapterTestRegistryStub) List() ([]Device, error)             { return nil, nil }
func (r *adapterTestRegistryStub) Default(Direction) (Device, error)   { return Device{}, nil }
func (r *adapterTestRegistryStub) Open(DeviceID) (OpenedDevice, error) { return r.handle, r.err }

type adapterRawHandle struct {
	direction Direction
	read      func(context.Context) ([]byte, error)
	write     func(context.Context, []byte) error
}

func (h *adapterRawHandle) DeviceDirection() Direction               { return h.direction }
func (h *adapterRawHandle) Close() error                             { return nil }
func (h *adapterRawHandle) Read(ctx context.Context) ([]byte, error) { return h.read(ctx) }
func (h *adapterRawHandle) Write(ctx context.Context, frame []byte) error {
	if h.write == nil {
		return nil
	}
	return h.write(ctx, frame)
}

type adapterNoDirectionHandle struct{ closed int }

func (h *adapterNoDirectionHandle) Close() error { h.closed++; return nil }
