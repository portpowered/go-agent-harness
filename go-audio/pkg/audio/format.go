package audio

import (
	"errors"
	"fmt"
	"math/big"
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

type pcm16FrameSizeError string

func (e pcm16FrameSizeError) Error() string { return string(e) }

const (
	// ErrInvalidPCM16FrameSize identifies a PCM16 frame dimension that is
	// invalid, not sample-aligned, or cannot be represented by an int-sized
	// sample/byte buffer.
	ErrInvalidPCM16FrameSize pcm16FrameSizeError = "invalid PCM16 frame size"
)

// PCM16FrameSamples returns the number of interleaved PCM16 samples in one
// frame. The duration must produce an exact integral number of samples at the
// requested rate; fractional samples are rejected rather than rounded.
//
// The arithmetic is reduced before multiplication by the duration's nanosecond
// remainder, so a valid result is not lost when rate*duration would overflow
// an int64 intermediate. The returned count is bounded by the platform int
// range, but is not restricted to the byte-safe half of that range.
func PCM16FrameSamples(sampleRate, channels int, frameDuration time.Duration) (int, error) {
	if sampleRate <= 0 || channels <= 0 || frameDuration <= 0 {
		return 0, fmt.Errorf("%w: rate=%d channels=%d duration=%s", ErrInvalidPCM16FrameSize, sampleRate, channels, frameDuration)
	}

	samplesPerChannel, err := pcm16FrameSamplesPerChannel(sampleRate, frameDuration)
	if err != nil {
		return 0, err
	}
	maximumInt := uint64(maximumPCM16Int())
	channelCount := uint64(channels)
	if samplesPerChannel > maximumInt/channelCount {
		return 0, fmt.Errorf("%w: samples=%d channels=%d exceed int range", ErrInvalidPCM16FrameSize, samplesPerChannel, channels)
	}
	return int(samplesPerChannel * channelCount), nil
}

// PCM16FrameBytes returns the number of bytes in one interleaved PCM16 frame.
// It applies the additional byte-buffer bound after validating the exact
// sample count, so callers can distinguish a sample-valid frame from one that
// cannot be represented as a byte slice.
func PCM16FrameBytes(sampleRate, channels int, frameDuration time.Duration) (int, error) {
	samples, err := PCM16FrameSamples(sampleRate, channels, frameDuration)
	if err != nil {
		return 0, err
	}
	maximumInt := maximumPCM16Int()
	if samples > maximumInt/2 {
		return 0, fmt.Errorf("%w: samples=%d exceed PCM16 byte range", ErrInvalidPCM16FrameSize, samples)
	}
	return samples * 2, nil
}

// PCM16ByteCapacity returns a checked byte capacity for frameCount frames of
// frameBytes bytes each. Zero is a valid mathematical capacity; negative
// dimensions and products outside the platform int range are rejected before
// any caller can use the result for allocation or queue sizing.
func PCM16ByteCapacity(frameCount, frameBytes int) (int, error) {
	if frameCount < 0 || frameBytes < 0 {
		return 0, fmt.Errorf("%w: frameCount=%d frameBytes=%d", ErrInvalidPCM16FrameSize, frameCount, frameBytes)
	}
	if frameCount == 0 || frameBytes == 0 {
		return 0, nil
	}
	maximumInt := maximumPCM16Int()
	if frameCount > maximumInt/frameBytes {
		return 0, fmt.Errorf("%w: frameCount=%d frameBytes=%d exceed int range", ErrInvalidPCM16FrameSize, frameCount, frameBytes)
	}
	return frameCount * frameBytes, nil
}

// PCM16ByteDuration returns the audio duration represented by byteCount bytes
// at the supplied interleaved PCM16 rate and channel count. It preserves the
// existing floor-to-nanosecond behavior and saturates at the largest
// representable time.Duration instead of overflowing when the rate/channel
// product exceeds uint64 or the byte-to-nanosecond numerator is wide.
func PCM16ByteDuration(byteCount, sampleRate, channels int) time.Duration {
	if byteCount <= 0 || sampleRate <= 0 || channels <= 0 {
		return 0
	}

	const maxDurationNanoseconds = uint64(1<<63 - 1)
	const maxUint64 = ^uint64(0)
	bytes := uint64(byteCount)
	rate := uint64(sampleRate)
	channelCount := uint64(channels)
	if rate <= maxUint64/channelCount {
		bytesPerSecond := rate * channelCount
		if bytesPerSecond <= maxUint64/2 && bytes <= maxUint64/uint64(time.Second) {
			bytesPerSecond *= 2
			nanoseconds := bytes * uint64(time.Second) / bytesPerSecond
			if nanoseconds > maxDurationNanoseconds {
				return time.Duration(maxDurationNanoseconds)
			}
			return time.Duration(nanoseconds)
		}
	}

	return pcm16ByteDurationWide(bytes, rate, channelCount)
}

func pcm16ByteDurationWide(byteCount, sampleRate, channels uint64) time.Duration {
	numerator := new(big.Int).SetUint64(byteCount)
	numerator.Mul(numerator, new(big.Int).SetUint64(uint64(time.Second)))
	denominator := new(big.Int).SetUint64(sampleRate)
	denominator.Mul(denominator, new(big.Int).SetUint64(channels))
	denominator.Lsh(denominator, 1)
	numerator.Quo(numerator, denominator)
	if !numerator.IsInt64() {
		return time.Duration(uint64(1<<63 - 1))
	}
	return time.Duration(numerator.Int64())
}

func pcm16FrameSamplesPerChannel(sampleRate int, frameDuration time.Duration) (uint64, error) {
	const nanosPerSecond = uint64(time.Second)
	rate := uint64(sampleRate)
	duration := uint64(frameDuration)
	wholeSeconds := duration / nanosPerSecond
	remainingNanos := duration % nanosPerSecond

	wholeSamples, overflow := checkedPCM16Uint64Product(rate, wholeSeconds)
	if overflow {
		return 0, fmt.Errorf("%w: rate=%d duration=%s exceed sample range", ErrInvalidPCM16FrameSize, sampleRate, frameDuration)
	}

	// Reduce rate before multiplying by the sub-second remainder. Both factors
	// are below one billion after reduction, so this product is always safe in
	// uint64 and its remainder is the exact sample-alignment check.
	rateWhole := rate / nanosPerSecond
	rateRemainder := rate % nanosPerSecond
	fractionalProduct := rateRemainder * remainingNanos
	fractionalSamples := rateWhole*remainingNanos + fractionalProduct/nanosPerSecond
	if fractionalProduct%nanosPerSecond != 0 {
		return 0, fmt.Errorf("%w: duration %s is not sample aligned at %d Hz", ErrInvalidPCM16FrameSize, frameDuration, sampleRate)
	}

	samples, overflow := checkedPCM16Uint64Sum(wholeSamples, fractionalSamples)
	if overflow || samples == 0 {
		return 0, fmt.Errorf("%w: rate=%d duration=%s exceed sample range", ErrInvalidPCM16FrameSize, sampleRate, frameDuration)
	}
	return samples, nil
}

func checkedPCM16Uint64Product(left, right uint64) (uint64, bool) {
	if left != 0 && right > ^uint64(0)/left {
		return 0, true
	}
	return left * right, false
}

func checkedPCM16Uint64Sum(left, right uint64) (uint64, bool) {
	if right > ^uint64(0)-left {
		return 0, true
	}
	return left + right, false
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
