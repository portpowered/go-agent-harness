package devices

import (
	"errors"
	"strings"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
		closeForTest(t, "source", source)
		t.Fatalf("NewDeviceSink(default) = %v", err)
	}
	if got, want := sink.DeviceID(), DeviceID("virtual:output"); got != want {
		closeForTest(t, "source", source)
		closeForTest(t, "sink", sink)
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
	input := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: audio.DefaultDeviceFormat()}
	output := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionInput}, format: audio.DefaultDeviceFormat()}
	registry := &adversarialDuplexRegistry{input: input, output: output}

	_, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", audio.DefaultDeviceFormat(), "output", audio.DefaultDeviceFormat())
	if !errors.Is(err, ErrDeviceDirectionMismatch) {
		t.Fatalf("swapped duplex open = %v, want direction mismatch", err)
	}
	if input.closeCount != 1 || output.closeCount != 1 {
		t.Fatalf("swapped graph close counts = input:%d output:%d, want one each", input.closeCount, output.closeCount)
	}
}

func TestDuplexDeviceOpenRejectsWrongNegotiatedFormatAndClosesGraph(t *testing.T) {
	want := audio.PCM16DeviceFormat(24000)
	input := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionInput}, format: audio.DefaultDeviceFormat()}
	output := &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: DirectionOutput}, format: want}
	registry := &adversarialDuplexRegistry{input: input, output: output}

	_, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", want, "output", want)
	if !errors.Is(err, audio.ErrUnsupportedDeviceFormat) {
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
func (r *adversarialDuplexRegistry) OpenDuplexWithFormat(DeviceID, audio.DeviceFormat, DeviceID, audio.DeviceFormat) (OpenedDevice, OpenedDevice, error) {
	return r.input, r.output, r.err
}

func TestDuplexDeviceOpenLifecycleFailuresAndSuccess(t *testing.T) {
	format := audio.DefaultDeviceFormat()
	newHandle := func(direction Direction) *adapterFormatHandle {
		return &adapterFormatHandle{adapterFrameHandle: &adapterFrameHandle{direction: direction}, format: format}
	}

	t.Run("invalid formats", func(t *testing.T) {
		registry := &adversarialDuplexRegistry{}
		if _, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", audio.DeviceFormat{}, "output", format); err == nil {
			t.Fatal("invalid input format was accepted")
		}
		if _, _, err := NewDuplexDeviceSourceSinkWithFormat(registry, "input", format, "output", audio.DeviceFormat{}); err == nil {
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

	assertDuplexOpenValidationClosesGraph(t, format, newHandle)
	assertDuplexOpenTransfersOwnership(t, format, newHandle)
}

// assertDuplexOpenValidationClosesGraph covers the post-open validation
// failures that must release both halves of the graph.
func assertDuplexOpenValidationClosesGraph(t *testing.T, format audio.DeviceFormat, newHandle func(Direction) *adapterFormatHandle) {
	t.Helper()
	t.Run("output validation closes graph", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionInput)
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, ErrDeviceDirectionMismatch) || input.closeCount != 1 || output.closeCount != 1 {
			t.Fatalf("duplex error=%v close counts=%d,%d", err, input.closeCount, output.closeCount)
		}
	})

	t.Run("output format validation closes graph", func(t *testing.T) {
		input, output := newHandle(DirectionInput), newHandle(DirectionOutput)
		output.format = audio.PCM16DeviceFormat(24000)
		_, _, err := NewDuplexDeviceSourceSinkWithFormat(&adversarialDuplexRegistry{input: input, output: output}, "input", format, "output", format)
		if !errors.Is(err, audio.ErrUnsupportedDeviceFormat) || input.closeCount != 1 || output.closeCount != 1 {
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
}

// assertDuplexOpenTransfersOwnership checks that a validated duplex graph is
// handed to the returned source and sink with resolved default IDs.
func assertDuplexOpenTransfersOwnership(t *testing.T, format audio.DeviceFormat, newHandle func(Direction) *adapterFormatHandle) {
	t.Helper()
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
	format    audio.DeviceFormat
	closed    int
}

func (h *duplexCapabilityHandle) DeviceDirection() Direction       { return h.direction }
func (h *duplexCapabilityHandle) DeviceFormat() audio.DeviceFormat { return h.format }
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

func TestDeviceFormatValidationAndErrorDetails(t *testing.T) {
	valid := audio.PCM16DeviceFormat(24000)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid device format rejected: %v", err)
	}
	for _, invalid := range []audio.DeviceFormat{
		{},
		{SampleRate: 24000, Channels: 2, BitDepth: audio.DeviceBitDepthPCM16, Encoding: audio.DeviceEncodingPCM16},
		{SampleRate: 24000, Channels: audio.Channels, BitDepth: 8, Encoding: audio.DeviceEncodingPCM16},
		{SampleRate: 24000, Channels: audio.Channels, BitDepth: audio.DeviceBitDepthPCM16, Encoding: "g711"},
	} {
		if err := invalid.Validate(); !errors.Is(err, audio.ErrInvalidDeviceFormat) {
			t.Fatalf("invalid format %v error = %v, want ErrInvalidDeviceFormat", invalid, err)
		}
	}
	if got := (audio.DeviceFormat{SampleRate: 24000, Channels: audio.Channels, BitDepth: audio.DeviceBitDepthPCM16}).String(); !strings.Contains(got, "unknown") {
		t.Fatalf("format with no encoding = %q, want unknown encoding", got)
	}
	if got := audio.DefaultDeviceFormatAvailability(); len(got) != 1 || !got[0].Equal(audio.DefaultDeviceFormat()) {
		t.Fatalf("default format availability = %#v, want the legacy default", got)
	}

	cause := errors.New("backend rejected requested rate")
	formatErr := &DeviceFormatError{
		ID:        "virtual:output",
		Direction: DirectionOutput,
		Requested: valid,
		Available: []audio.DeviceFormat{audio.DefaultDeviceFormat(), audio.PCM16DeviceFormat(48000)},
		Err:       cause,
	}
	message := formatErr.Error()
	for _, want := range []string{"virtual:output", "24000 Hz", "16000 Hz", "48000 Hz", cause.Error()} {
		if !strings.Contains(message, want) {
			t.Fatalf("format error %q does not contain %q", message, want)
		}
	}
	if !errors.Is(formatErr, audio.ErrUnsupportedDeviceFormat) || !errors.Is(formatErr, cause) {
		t.Fatalf("format error = %v, want unsupported and backend causes", formatErr)
	}
	withoutCause := &DeviceFormatError{ID: "virtual:output", Direction: DirectionOutput, Requested: valid}
	if !errors.Is(withoutCause, audio.ErrUnsupportedDeviceFormat) || !errors.Is(withoutCause.Unwrap(), audio.ErrUnsupportedDeviceFormat) {
		t.Fatalf("cause-free format error unwrap = %v, want ErrUnsupportedDeviceFormat", withoutCause.Unwrap())
	}
	var nilFormatErr *DeviceFormatError
	if nilFormatErr.Error() != nilErrorText || nilFormatErr.Unwrap() != nil {
		t.Fatalf("nil format error = %q/%v, want <nil>/nil", nilFormatErr.Error(), nilFormatErr.Unwrap())
	}
}
