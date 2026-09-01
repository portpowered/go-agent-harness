package audio

import (
	"errors"
	"fmt"
)

const (
	// DeviceEncodingPCM16 is the signed little-endian PCM format used by the
	// session media boundary.
	DeviceEncodingPCM16 = "pcm16"
	// DeviceBitDepthPCM16 is the sample width for DeviceEncodingPCM16.
	DeviceBitDepthPCM16 = 16
)

var (
	// ErrInvalidDeviceFormat identifies a malformed format request.
	ErrInvalidDeviceFormat = errors.New("invalid audio device format")
	// ErrUnsupportedDeviceFormat identifies a device or backend that cannot
	// open the requested PCM format.
	ErrUnsupportedDeviceFormat = errors.New("unsupported audio device format")
)

// DeviceFormat is the concrete PCM format requested from a local device.
// FrameSize remains a sample count owned by AudioSource/AudioSink; the sample
// rate belongs here so the same 480-sample frame is paced at the right speed.
type DeviceFormat struct {
	SampleRate int
	Channels   int
	BitDepth   int
	Encoding   string
}

// PCM16DeviceFormat returns a validated-shape mono PCM16 format at rate.
func PCM16DeviceFormat(rate int) DeviceFormat {
	return DeviceFormat{
		SampleRate: rate,
		Channels:   Channels,
		BitDepth:   DeviceBitDepthPCM16,
		Encoding:   DeviceEncodingPCM16,
	}
}

// DefaultDeviceFormat is the compatibility format used by the original
// frame-oriented device constructors.
func DefaultDeviceFormat() DeviceFormat { return PCM16DeviceFormat(SampleRate) }

// Validate checks the format fields that this package can encode and decode.
func (f DeviceFormat) Validate() error {
	if f.SampleRate <= 0 || f.Channels != Channels || f.BitDepth != DeviceBitDepthPCM16 || f.Encoding != DeviceEncodingPCM16 {
		return fmt.Errorf("%w: want mono PCM16 with a positive sample rate, got %v", ErrInvalidDeviceFormat, f)
	}
	return nil
}

func (f DeviceFormat) String() string {
	encoding := f.Encoding
	if encoding == "" {
		encoding = "unknown"
	}
	return fmt.Sprintf("%d Hz, %d channel, %d-bit %s", f.SampleRate, f.Channels, f.BitDepth, encoding)
}

func (f DeviceFormat) equal(other DeviceFormat) bool {
	return f.SampleRate == other.SampleRate &&
		f.Channels == other.Channels &&
		f.BitDepth == other.BitDepth &&
		f.Encoding == other.Encoding
}

func defaultDeviceFormatAvailability() []DeviceFormat {
	return []DeviceFormat{DefaultDeviceFormat()}
}

// DeviceFormatOpener is the optional registry capability for opening a
// device at an explicit PCM format. Registries that do not implement it can
// still be used with NewDeviceSource/NewDeviceSink, which retain the legacy
// backend-selected format.
type DeviceFormatOpener interface {
	OpenWithFormat(DeviceID, DeviceFormat) (OpenedDevice, error)
}

// DeviceFormatProvider is implemented by opened devices that can report the
// format selected by their registry/backend.
type DeviceFormatProvider interface {
	DeviceFormat() DeviceFormat
}

// DeviceFormatError preserves the requested and, when known, available
// formats so callers can explain a provider/device rate mismatch without
// parsing a backend-specific error string.
type DeviceFormatError struct {
	ID        DeviceID
	Direction Direction
	Requested DeviceFormat
	Available []DeviceFormat
	Err       error
}

func (e *DeviceFormatError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("device %q cannot open %s format %s", e.ID, e.Direction, e.Requested)
	if len(e.Available) > 0 {
		message += "; available: "
		for index, format := range e.Available {
			if index > 0 {
				message += ", "
			}
			message += format.String()
		}
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *DeviceFormatError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return errors.Join(ErrUnsupportedDeviceFormat, e.Err)
	}
	return ErrUnsupportedDeviceFormat
}
