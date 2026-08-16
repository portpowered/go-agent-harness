package audio

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	ErrNilDeviceRegistry        = errors.New("audio device registry is nil")
	ErrNilOpenedDevice          = errors.New("audio device registry returned a nil handle")
	ErrDeviceDirectionMismatch  = errors.New("audio device direction mismatch")
	ErrDeviceCapabilityMismatch = errors.New("audio device capability mismatch")
)

// DeviceAdapterError is the inspectable constructor error used for nil
// registries, direction mismatches, and missing stream capabilities.
type DeviceAdapterError struct {
	ID        DeviceID
	Direction Direction
	Want      Direction
	Got       Direction
	Operation string
	Err       error
	Kind      error
}

type DeviceRegistryError = DeviceAdapterError
type DeviceDirectionError = DeviceAdapterError
type DeviceCapabilityError = DeviceAdapterError

func (e *DeviceAdapterError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("open %s device %q: %v", e.Direction, e.ID, e.Err)
	}
	if e.Kind == ErrDeviceDirectionMismatch {
		return fmt.Sprintf("device %q is %s; want %s", e.ID, e.Got, e.Want)
	}
	return fmt.Sprintf("device %q has no %s capability for %s", e.ID, e.Direction, e.Operation)
}

func (e *DeviceAdapterError) Unwrap() error        { return e.Err }
func (e *DeviceAdapterError) Is(target error) bool { return target == e.Kind }

type deviceFrameReader interface {
	ReadFrame(context.Context, []int16) error
}

type deviceFrameWriter interface {
	WriteFrame(context.Context, []int16) error
}

type deviceByteReader interface {
	Read(context.Context) ([]byte, error)
}

type deviceByteWriter interface {
	Write(context.Context, []byte) error
}

type deviceAdapter struct {
	mu        sync.Mutex
	handle    OpenedDevice
	id        DeviceID
	direction Direction
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func newDeviceAdapter(handle OpenedDevice, id DeviceID, direction Direction) *deviceAdapter {
	return &deviceAdapter{handle: handle, id: id, direction: direction}
}

func (a *deviceAdapter) begin(operation string) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return &ClosedError{Operation: operation, Path: string(a.id)}
	}
	a.mu.Unlock()
	return nil
}

func (a *deviceAdapter) finish(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDeviceLost) {
		var lost *DeviceLostError
		if errors.As(err, &lost) && lost.ID == a.id && lost.Direction == a.direction {
			return err
		}
		return fmt.Errorf("%w: %v", &DeviceLostError{ID: a.id, Direction: a.direction}, err)
	}
	if a.isClosed() {
		return &ClosedError{Operation: operation, Path: string(a.id)}
	}
	return err
}

func (a *deviceAdapter) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

func (a *deviceAdapter) close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		handle := a.handle
		a.mu.Unlock()

		var closeErr error
		if handle != nil {
			closeErr = handle.Close()
		}
		a.mu.Lock()
		a.closeErr = closeErr
		a.mu.Unlock()
	})
	return a.closeErr
}

type DeviceSource struct {
	adapter     *deviceAdapter
	frameReader deviceFrameReader
	byteReader  deviceByteReader
}

var _ AudioSource = (*DeviceSource)(nil)

func NewDeviceSource(registry DeviceRegistry, id DeviceID) (*DeviceSource, error) {
	handle, err := acquireDevice(registry, id, DirectionInput)
	if err != nil {
		return nil, err
	}
	frameReader, hasFrames := handle.(deviceFrameReader)
	byteReader, hasBytes := handle.(deviceByteReader)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: id, Direction: DirectionInput, Operation: "read", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSource{
		adapter:     newDeviceAdapter(handle, id, DirectionInput),
		frameReader: frameReader,
		byteReader:  byteReader,
	}, nil
}

func (s *DeviceSource) ReadFrame(ctx context.Context, frame []int16) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateFrame("read", frame); err != nil {
		return err
	}
	if err := s.adapter.begin("read"); err != nil {
		return err
	}

	if s.frameReader != nil {
		return s.adapter.finish("read", s.frameReader.ReadFrame(ctx, frame))
	}
	encoded, readErr := s.byteReader.Read(ctx)
	if readErr != nil {
		return s.adapter.finish("read", readErr)
	}
	if len(encoded) != rawFrameBytes {
		return &FrameSizeError{Operation: "device read", Got: len(encoded) / 2, Want: FrameSize}
	}
	decodePCM16(frame, encoded)
	return nil
}

func (s *DeviceSource) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}
	return s.adapter.close()
}

func acquireDevice(registry DeviceRegistry, id DeviceID, direction Direction) (OpenedDevice, error) {
	if nilInterface(registry) {
		return nil, &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilDeviceRegistry}
	}
	handle, err := registry.Open(id)
	if err != nil {
		if !nilInterface(handle) {
			_ = handle.Close()
		}
		return nil, err
	}
	if nilInterface(handle) {
		return nil, &DeviceRegistryError{ID: id, Direction: direction, Err: ErrNilOpenedDevice}
	}
	if got, ok := openedDeviceDirection(handle); ok && got != direction {
		_ = handle.Close()
		return nil, &DeviceDirectionError{ID: id, Direction: direction, Want: direction, Got: got, Kind: ErrDeviceDirectionMismatch}
	}
	return handle, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func openedDeviceDirection(handle OpenedDevice) (Direction, bool) {
	if provider, ok := handle.(interface{ DeviceDirection() Direction }); ok {
		return provider.DeviceDirection(), true
	}
	v := reflect.Indirect(reflect.ValueOf(handle))
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return "", false
	}
	field := v.FieldByName("direction")
	if field.IsValid() && field.Kind() == reflect.String {
		direction := Direction(field.String())
		return direction, direction.IsValid()
	}
	return "", false
}

func (s *VirtualStream) DeviceDirection() Direction {
	if s == nil || s.device == nil {
		return ""
	}
	return s.device.Direction
}
