package audio

import (
	"errors"
	"fmt"
	"math/bits"
	"time"
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
	// ErrInvalidPCM16Duration identifies an invalid or unrepresentable PCM16
	// payload duration request.
	ErrInvalidPCM16Duration = errors.New("invalid PCM16 duration")
)

// PCM16Duration returns the duration represented by byteCount interleaved
// signed little-endian PCM16 bytes. It validates frame alignment and keeps
// the arithmetic bounded by time.Duration rather than allowing a sample
// count to wrap during evidence or media timing calculations.
func PCM16Duration(byteCount, sampleRate, channels int) (time.Duration, error) {
	if byteCount < 0 || sampleRate <= 0 || channels <= 0 {
		return 0, fmt.Errorf("%w: bytes=%d rate=%d channels=%d", ErrInvalidPCM16Duration, byteCount, sampleRate, channels)
	}
	frameBytes := uint64(channels) * 2
	if frameBytes/2 != uint64(channels) || uint64(byteCount)%frameBytes != 0 {
		return 0, fmt.Errorf("%w: %d bytes is not aligned to %d channels", ErrInvalidPCM16Duration, byteCount, channels)
	}
	frames := uint64(byteCount) / frameBytes
	rate := uint64(sampleRate)
	seconds := frames / rate
	const nanosPerSecond = uint64(time.Second)
	maxDuration := uint64(1<<63 - 1)
	if seconds > maxDuration/nanosPerSecond {
		return 0, fmt.Errorf("%w: duration overflows time.Duration", ErrInvalidPCM16Duration)
	}
	nanos := seconds * nanosPerSecond
	remaining := frames % rate
	if remaining != 0 {
		hi, lo := bits.Mul64(remaining, nanosPerSecond)
		fraction, remainder := bits.Div64(hi, lo, rate)
		if remainder != 0 {
			// Duration values are integral nanoseconds; truncate only the
			// unrepresentable fractional nanosecond after exact division.
			_ = remainder
		}
		if fraction > maxDuration-nanos {
			return 0, fmt.Errorf("%w: duration overflows time.Duration", ErrInvalidPCM16Duration)
		}
		nanos += fraction
	}
	return time.Duration(nanos), nil
}

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

func (f DeviceFormat) Equal(other DeviceFormat) bool {
	return f.SampleRate == other.SampleRate &&
		f.Channels == other.Channels &&
		f.BitDepth == other.BitDepth &&
		f.Encoding == other.Encoding
}

func DefaultDeviceFormatAvailability() []DeviceFormat {
	return []DeviceFormat{DefaultDeviceFormat()}
}
