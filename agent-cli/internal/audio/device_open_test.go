package audio

import (
	"errors"
	"testing"
)

func TestDeviceAdaptersResolveDirectionalDefaultsAndExposeIDs(t *testing.T) {
	registry := adapterTestRegistry(t)

	source, err := NewDeviceSource(registry, "")
	if err != nil {
		t.Fatalf("NewDeviceSource(default) = %v", err)
	}
	if got, want := source.DeviceID(), DeviceID("virtual:input"); got != want {
		t.Fatalf("source DeviceID() = %q, want %q", got, want)
	}

	sink, err := NewDeviceSink(registry, "")
	if err != nil {
		_ = source.Close()
		t.Fatalf("NewDeviceSink(default) = %v", err)
	}
	if got, want := sink.DeviceID(), DeviceID("virtual:output"); got != want {
		_ = source.Close()
		_ = sink.Close()
		t.Fatalf("sink DeviceID() = %q, want %q", got, want)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	var nilSource *DeviceSource
	if got := nilSource.DeviceID(); got != "" {
		t.Fatalf("nil source DeviceID() = %q, want empty", got)
	}
	var nilSink *DeviceSink
	if got := nilSink.DeviceID(); got != "" {
		t.Fatalf("nil sink DeviceID() = %q, want empty", got)
	}
}

func TestDuplexDeviceOpenRejectsSwappedDirectionsAndClosesGraph(t *testing.T) {
	input := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: DefaultDeviceFormat()}
	output := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionInput}, format: DefaultDeviceFormat()}
	registry := &adversarialDuplexRegistry{input: input, output: output}

	_, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", DefaultDeviceFormat(), "output", DefaultDeviceFormat())
	if !errors.Is(err, ErrDeviceDirectionMismatch) {
		t.Fatalf("swapped duplex open = %v, want direction mismatch", err)
	}
	if input.closeCount != 1 || output.closeCount != 1 {
		t.Fatalf("swapped graph close counts = input:%d output:%d, want one each", input.closeCount, output.closeCount)
	}
}

func TestDuplexDeviceOpenRejectsWrongNegotiatedFormatAndClosesGraph(t *testing.T) {
	want := PCM16DeviceFormat(24000)
	input := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionInput}, format: DefaultDeviceFormat()}
	output := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: want}
	registry := &adversarialDuplexRegistry{input: input, output: output}

	_, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", want, "output", want)
	if !errors.Is(err, ErrUnsupportedDeviceFormat) {
		t.Fatalf("wrong-format duplex open = %v, want unsupported format", err)
	}
	if input.closeCount != 1 || output.closeCount != 1 {
		t.Fatalf("wrong-format graph close counts = input:%d output:%d, want one each", input.closeCount, output.closeCount)
	}
}

type adversarialDuplexRegistry struct {
	input  OpenedDevice
	output OpenedDevice
	err    error
}

func (r *adversarialDuplexRegistry) List() ([]Device, error) { return nil, nil }
func (r *adversarialDuplexRegistry) Default(direction Direction) (Device, error) {
	return Device{ID: DeviceID(string(direction)), Direction: direction}, nil
}
func (r *adversarialDuplexRegistry) Open(DeviceID) (OpenedDevice, error) {
	return nil, errors.New("unexpected independent open")
}
func (r *adversarialDuplexRegistry) OpenDuplexWithFormat(DeviceID, DeviceFormat, DeviceID, DeviceFormat) (OpenedDevice, OpenedDevice, error) {
	return r.input, r.output, r.err
}

func TestDuplexDeviceOpenLifecycleFailuresAndSuccess(t *testing.T) {
	format := DefaultDeviceFormat()
	newHandle := func(direction Direction) *adapterFormatHandle {
		return &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: direction}, format: format}
	}

	t.Run("invalid formats", func(t *testing.T) {
		registry := &adversarialDuplexRegistry{}
		if _, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", DeviceFormat{}, "output", format); err == nil {
			t.Fatal("invalid input format was accepted")
		}
		if _, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", format, "output", DeviceFormat{}); err == nil {
			t.Fatal("invalid output format was accepted")
		}
	})

	t.Run("registry without atomic duplex support", func(t *testing.T) {
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adapterTestRegistryStub{}, "input", format, "output", format)
		if !errors.Is(err, ErrDuplexDeviceUnavailable) {
			t.Fatalf("duplex open = %v, want unavailable", err)
		}
	})

	t.Run("backend error closes partial graph", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionOutput)
		want := errors.New("atomic open failed")
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output, err: want}, "input", format, "output", format)
		if !errors.Is(err, want) || input.closeCount != 1 || output.closeCount != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closeCount, output.closeCount)
		}
	})

	for _, testCase := range []struct {
		name   string
		input  OpenedDevice
		output OpenedDevice
		closed *adapterFormatHandle
	}{
		{name: "nil input", output: newHandle(DirectionOutput)},
		{name: "nil output", input: newHandle(DirectionInput)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if handle, ok := testCase.input.(*adapterFormatHandle); ok {
				testCase.closed = handle
			} else if handle, ok := testCase.output.(*adapterFormatHandle); ok {
				testCase.closed = handle
			}
			_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: testCase.input, output: testCase.output}, "input", format, "output", format)
			if !errors.Is(err, ErrNilOpenedDevice) || testCase.closed.closeCount != 1 {
				t.Fatalf("duplex error=%v close count=%d", err, testCase.closed.closeCount)
			}
		})
	}

	t.Run("output validation closes graph", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionInput)
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, ErrDeviceDirectionMismatch) || input.closeCount != 1 || output.closeCount != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closeCount, output.closeCount)
		}
	})

	t.Run("output format validation closes graph", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionOutput)
		output.format = PCM16DeviceFormat(24000)
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, ErrUnsupportedDeviceFormat) || input.closeCount != 1 || output.closeCount != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closeCount, output.closeCount)
		}
	})

	t.Run("missing input capability closes graph", func(t *testing.T) {
		input := &duplexCapabilityHandle{direction: DirectionInput, format: format}
		output := newHandle(DirectionOutput)
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, ErrDeviceCapabilityMismatch) || input.closed != 1 || output.closeCount != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closed, output.closeCount)
		}
	})

	t.Run("missing output capability closes graph", func(t *testing.T) {
		input := newHandle(DirectionInput)
		output := &duplexCapabilityHandle{direction: DirectionOutput, format: format}
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, ErrDeviceCapabilityMismatch) || input.closeCount != 1 || output.closed != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closeCount, output.closed)
		}
	})

	t.Run("success transfers graph ownership", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionOutput)
		source, sink, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "", format, "", format)
		if err != nil {
			t.Fatalf("duplex open: %v", err)
		}
		if source.DeviceID() != "input" || sink.DeviceID() != "output" {
			t.Fatalf("resolved IDs = %q,%q", source.DeviceID(), sink.DeviceID())
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

type duplexCapabilityHandle struct {
	direction Direction
	format    DeviceFormat
	closed    int
}

func (h *duplexCapabilityHandle) DeviceDirection() Direction { return h.direction }
func (h *duplexCapabilityHandle) DeviceFormat() DeviceFormat { return h.format }
func (h *duplexCapabilityHandle) Close() error {
	h.closed++
	return nil
}

func TestDeviceIDResolutionPreservesDirectionalDefaultFailures(t *testing.T) {
	tests := []struct {
		name      string
		registry  DeviceRegistry
		direction Direction
		want      error
	}{
		{
			name:      "no default",
			registry:  &adapterTestRegistryStub{defaultErr: ErrNoDefaultDevice},
			direction: DirectionInput,
			want:      ErrNoDefaultDevice,
		},
		{
			name: "wrong direction",
			registry: &adapterTestRegistryStub{defaultDevice: Device{
				ID:        "virtual:output",
				Direction: DirectionOutput,
			}},
			direction: DirectionInput,
			want:      ErrDeviceDirectionMismatch,
		},
		{
			name: "empty ID",
			registry: &adapterTestRegistryStub{defaultDevice: Device{
				Direction: DirectionInput,
			}},
			direction: DirectionInput,
			want:      ErrInvalidDevice,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := resolveDeviceIDForOpen(testCase.registry, "", testCase.direction)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("resolveDeviceIDForOpen() = %v, want %v", err, testCase.want)
			}
		})
	}
}
