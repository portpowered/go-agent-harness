package audio

import (
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

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

// PCM16Framer converts a continuous byte stream into provider PCM packets.
// It shares the stream Processor with device capture and playback. Flush
// emits the exact tail and resets filter history for the next utterance.
type PCM16Framer struct{ processor *Processor }

func NewPCM16Framer(sourceRate, destinationRate, quantum int) (*PCM16Framer, error) {
	processor, err := NewProcessor(PCM16DeviceFormat(sourceRate), PCM16DeviceFormat(destinationRate), quantum)
	if err != nil {
		return nil, err
	}
	return &PCM16Framer{processor: processor}, nil
}

func (f *PCM16Framer) Push(pcm []byte) ([][]byte, error) {
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, err
	}
	frames, err := f.processor.Process(PCMFrame{Samples: samples})
	return encodeFrames(frames), err
}

func (f *PCM16Framer) Flush() ([][]byte, error) {
	frames, err := f.processor.Process(PCMFrame{EndOfResponse: true})
	if err != nil {
		return nil, err
	}
	_, err = f.processor.Reset()
	return encodeFrames(frames), err
}

func encodeFrames(frames []PCMFrame) [][]byte {
	result := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		if len(frame.Samples) > 0 {
			result = append(result, codec.EncodePCM16(frame.Samples))
		}
	}
	return result
}
