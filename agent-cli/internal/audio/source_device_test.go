package audio

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestDeviceSourceConformanceAndValidation(t *testing.T) {
	var nilRegistry *adapterTestRegistryStub
	if _, err := NewDeviceSource(nilRegistry, "input"); !errors.Is(err, ErrNilDeviceRegistry) {
		t.Fatalf("typed nil registry error = %v", err)
	}
	r := adapterTestRegistry(t)
	if _, err := NewDeviceSource(r, "virtual:missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := NewDeviceSource(r, "virtual:output"); !errors.Is(err, ErrDeviceDirectionMismatch) {
		t.Fatalf("wrong-direction source error = %v", err)
	}
	bare := &adapterBareHandle{direction: DirectionInput}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{handle: bare}, "input"); !errors.Is(err, ErrDeviceCapabilityMismatch) || bare.closed != 1 {
		t.Fatalf("missing capability error=%v close=%d", err, bare.closed)
	}

	samples := make([]int16, FrameSize*2)
	for i := range samples {
		samples[i] = int16(i - 700)
	}
	samples[0], samples[FrameSize] = -32768, 32767
	handle := &adapterFrameHandle{direction: DirectionInput, frames: [][]int16{samples[:FrameSize], samples[FrameSize:]}}
	source, err := NewDeviceSource(&adapterTestRegistryStub{handle: handle}, "input")
	if err != nil {
		t.Fatal(err)
	}
	var contract AudioSource = source
	assertSourceFrames(t, contract, samples)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.ReadFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ReadFrame = %v", err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize-1)); !errors.Is(err, ErrInvalidFrameSize) {
		t.Fatalf("invalid ReadFrame = %v", err)
	}
	if handle.reads != 3 {
		t.Fatalf("frame reads=%d, want two frames plus the conformance EOF probe", handle.reads)
	}
	if err := source.Close(); err != nil || source.Close() != nil {
		t.Fatalf("idempotent source close: %v", err)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadFrame after Close = %v", err)
	}
}

func TestDeviceSourceEdgeContracts(t *testing.T) {
	var nilSource *DeviceSource
	if nilSource.Close() != nil {
		t.Fatal("nil source Close returned an error")
	}
	var nilAdapterError *DeviceAdapterError
	if nilAdapterError.Error() != "<nil>" {
		t.Fatalf("nil adapter error = %q", nilAdapterError.Error())
	}

	openErr := errors.New("open failed")
	openHandle := &adapterBareHandle{direction: DirectionInput}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{handle: openHandle, err: openErr}, "input"); !errors.Is(err, openErr) || openHandle.closed != 1 {
		t.Fatalf("open error=%v close=%d, want preserved error and one cleanup", err, openHandle.closed)
	}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{}, "input"); !errors.Is(err, ErrNilOpenedDevice) {
		t.Fatalf("nil opened handle error=%v", err)
	}
	reflectHandle := &adapterReflectHandle{direction: DirectionOutput}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{handle: reflectHandle}, "input"); !errors.Is(err, ErrDeviceDirectionMismatch) || reflectHandle.closed != 1 {
		t.Fatalf("reflected direction error=%v close=%d", err, reflectHandle.closed)
	}
	noDirection := &adapterNoDirectionHandle{}
	if _, err := NewDeviceSource(&adapterTestRegistryStub{handle: noDirection}, "input"); !errors.Is(err, ErrDeviceCapabilityMismatch) || noDirection.closed != 1 {
		t.Fatalf("missing direction/capability error=%v close=%d", err, noDirection.closed)
	}

	badFrame := &adapterByteHandle{direction: DirectionInput, data: []byte{1}}
	source, err := NewDeviceSource(&adapterTestRegistryStub{handle: badFrame}, "input")
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	if err := source.ReadFrame(nilContext, make([]int16, FrameSize)); !errors.Is(err, ErrInvalidFrameSize) {
		t.Fatalf("malformed raw frame error=%v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name   string
		err    *DeviceAdapterError
		want   string
		target error
	}{
		{name: "registry", err: &DeviceAdapterError{ID: "input", Direction: DirectionInput, Err: ErrNilDeviceRegistry}, want: `open input device "input": audio device registry is nil`, target: ErrNilDeviceRegistry},
		{name: "direction", err: &DeviceAdapterError{ID: "output", Want: DirectionInput, Got: DirectionOutput, Kind: ErrDeviceDirectionMismatch}, want: `device "output" is output; want input`, target: ErrDeviceDirectionMismatch},
		{name: "capability", err: &DeviceAdapterError{ID: "input", Direction: DirectionInput, Operation: "read", Kind: ErrDeviceCapabilityMismatch}, want: `device "input" has no input capability for read`, target: ErrDeviceCapabilityMismatch},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.err.Error(); got != testCase.want {
				t.Fatalf("error=%q, want %q", got, testCase.want)
			}
			if !errors.Is(testCase.err, testCase.target) {
				t.Fatalf("error=%v does not expose %v", testCase.err, testCase.target)
			}
		})
	}
}

func TestDeviceSourceVirtualLossAndConcurrentClose(t *testing.T) {
	r := adapterTestRegistry(t)
	source, err := NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	ready, result := make(chan struct{}), make(chan error, 1)
	go func() {
		result <- source.ReadFrame(&adapterReadyContext{Context: context.Background(), ready: ready}, make([]int16, FrameSize))
	}()
	waitDeviceSignal(t, ready)
	if !r.RemoveDevice("virtual:input") {
		t.Fatal("RemoveDevice(input) = false")
	}
	lost := waitDeviceError(t, result)
	var lostErr *DeviceLostError
	if !errors.As(lost, &lostErr) || lostErr.ID != "virtual:input" || lostErr.Direction != DirectionInput || !errors.Is(lost, ErrDeviceLost) || errors.Is(lost, io.EOF) {
		t.Fatalf("pending read error = %v, want typed input loss", lost)
	}
	_ = source.Close()

	r = adapterTestRegistry(t)
	source, err = NewDeviceSource(r, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	ready, result = make(chan struct{}), make(chan error, 1)
	go func() {
		result <- source.ReadFrame(&adapterReadyContext{Context: context.Background(), ready: ready}, make([]int16, FrameSize))
	}()
	waitDeviceSignal(t, ready)
	closeErr := source.Close()
	readErr := waitDeviceError(t, result)
	if closeErr != nil || !errors.Is(readErr, ErrClosed) {
		t.Fatalf("concurrent close: close=%v read=%v", closeErr, readErr)
	}
	if source.Close() != nil {
		t.Fatal("second source close returned an error")
	}
}

func adapterTestRegistry(t *testing.T) *VirtualRegistry {
	t.Helper()
	r, err := NewVirtualRegistry(DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type adapterTestRegistryStub struct {
	handle        OpenedDevice
	err           error
	defaultDevice Device
	defaultErr    error
}

func (r *adapterTestRegistryStub) List() ([]Device, error) { return nil, nil }
func (r *adapterTestRegistryStub) Default(Direction) (Device, error) {
	return r.defaultDevice, r.defaultErr
}
func (r *adapterTestRegistryStub) Open(DeviceID) (OpenedDevice, error) {
	return r.handle, r.err
}

type adapterBareHandle struct {
	direction Direction
	closed    int
}

func (h *adapterBareHandle) DeviceDirection() Direction { return h.direction }
func (h *adapterBareHandle) Close() error               { h.closed++; return nil }

type adapterReflectHandle struct {
	direction Direction
	closed    int
}

func (h *adapterReflectHandle) Close() error { h.closed++; return nil }

type adapterNoDirectionHandle struct{ closed int }

func (h *adapterNoDirectionHandle) Close() error { h.closed++; return nil }

type adapterByteHandle struct {
	direction Direction
	data      []byte
}

func (h *adapterByteHandle) DeviceDirection() Direction { return h.direction }
func (h *adapterByteHandle) Read(context.Context) ([]byte, error) {
	return h.data, nil
}
func (h *adapterByteHandle) Close() error { return nil }

type adapterReadyContext struct {
	context.Context
	ready chan<- struct{}
	once  sync.Once
}

func (c *adapterReadyContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}

type adapterFrameHandle struct {
	mu        sync.Mutex
	direction Direction
	frames    [][]int16
	writes    [][]int16
	reads     int
	closed    bool
}

func (h *adapterFrameHandle) DeviceDirection() Direction { return h.direction }
func (h *adapterFrameHandle) ReadFrame(_ context.Context, frame []int16) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.reads++
	if len(h.frames) == 0 {
		return io.EOF
	}
	copy(frame, h.frames[0])
	h.frames = h.frames[1:]
	return nil
}
func (h *adapterFrameHandle) WriteFrame(_ context.Context, frame []int16) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.writes = append(h.writes, append([]int16(nil), frame...))
	return nil
}
func (h *adapterFrameHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

func waitDeviceSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("operation did not reach its synchronization point")
	}
}

func waitDeviceError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
		return nil
	}
}
