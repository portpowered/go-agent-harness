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
	handle    OpenedDevice
	id        DeviceID
	direction Direction
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newDeviceAdapter(handle OpenedDevice, id DeviceID, direction Direction) *deviceAdapter {
	return &deviceAdapter{handle: handle, id: id, direction: direction, closed: make(chan struct{})}
}

func (a *deviceAdapter) begin(operation string) error {
	select {
	case <-a.closed:
		return &ClosedError{Operation: operation, Path: string(a.id)}
	default:
		return nil
	}
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
	select {
	case <-a.closed:
		return &ClosedError{Operation: operation, Path: string(a.id)}
	default:
		return err
	}
}

func (a *deviceAdapter) close() error {
	a.closeOnce.Do(func() {
		close(a.closed)
		a.closeErr = a.handle.Close()
	})
	return a.closeErr
}

type DeviceSource struct {
	adapter     *deviceAdapter
	frameReader deviceFrameReader
	byteReader  deviceByteReader
	format      DeviceFormat
}

var _ AudioSource = (*DeviceSource)(nil)

func NewDeviceSource(registry DeviceRegistry, id DeviceID) (*DeviceSource, error) {
	return NewDeviceSourceWithFormat(registry, id, DefaultDeviceFormat())
}

// NewDeviceSourceAtRate opens a capture device as mono PCM16 at rate.
func NewDeviceSourceAtRate(registry DeviceRegistry, id DeviceID, rate int) (*DeviceSource, error) {
	return NewDeviceSourceWithFormat(registry, id, PCM16DeviceFormat(rate))
}

// NewDeviceSourceWithFormat opens a capture device using an explicit format.
// Registries that do not expose DeviceFormatOpener retain compatibility for
// the default format but cannot safely claim support for another rate.
func NewDeviceSourceWithFormat(registry DeviceRegistry, id DeviceID, format DeviceFormat) (*DeviceSource, error) {
	if err := format.Validate(); err != nil {
		return nil, err
	}
	resolvedID, err := resolveDeviceIDForOpen(registry, id, DirectionInput)
	if err != nil {
		return nil, err
	}
	handle, err := acquireDeviceWithFormat(registry, resolvedID, DirectionInput, format)
	if err != nil {
		return nil, err
	}
	frames, hasFrames := handle.(deviceFrameReader)
	bytes, hasBytes := handle.(deviceByteReader)
	if !hasFrames && !hasBytes {
		_ = handle.Close()
		return nil, &DeviceCapabilityError{ID: resolvedID, Direction: DirectionInput, Operation: "read", Kind: ErrDeviceCapabilityMismatch}
	}
	return &DeviceSource{adapter: newDeviceAdapter(handle, resolvedID, DirectionInput), frameReader: frames, byteReader: bytes, format: format}, nil
}

// DeviceID returns the stable ID acquired by the source. When the source was
// opened with an empty selector, this is the ID returned by the registry's
// directional default.
func (s *DeviceSource) DeviceID() DeviceID {
	if s == nil || s.adapter == nil {
		return ""
	}
	return s.adapter.id
}

// DeviceFormat reports the format selected when the source was opened.
func (s *DeviceSource) DeviceFormat() DeviceFormat {
	if s == nil {
		return DeviceFormat{}
	}
	return s.format
}

// SampleRate reports the selected capture rate for callers that only need
// the pacing contract.
func (s *DeviceSource) SampleRate() int {
	return s.DeviceFormat().SampleRate
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
	encoded, err := s.byteReader.Read(ctx)
	if err != nil {
		return s.adapter.finish("read", err)
	}
	if len(encoded) != rawFrameBytes {
		return s.adapter.finish("read", &FrameSizeError{Operation: "device read", Got: len(encoded) / 2, Want: FrameSize})
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
