package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"errors"
	"strings"
	"testing"
)

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
	if !errors.Is(withoutCause, audio.ErrUnsupportedDeviceFormat) || withoutCause.Unwrap() != audio.ErrUnsupportedDeviceFormat {
		t.Fatalf("cause-free format error unwrap = %v, want ErrUnsupportedDeviceFormat", withoutCause.Unwrap())
	}
	var nilFormatErr *DeviceFormatError
	if nilFormatErr.Error() != "<nil>" || nilFormatErr.Unwrap() != nil {
		t.Fatalf("nil format error = %q/%v, want <nil>/nil", nilFormatErr.Error(), nilFormatErr.Unwrap())
	}
}
