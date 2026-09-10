package mixer

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

const (
	pcm16MaxSample = 32767
	pcm16MinSample = -32768

	// MaxPCM16MixSources keeps the exact accumulation bound explicit. The
	// largest possible supported magnitude is 32768 * MaxPCM16MixSources,
	// which is well below the int64 limit.
	MaxPCM16MixSources = 1 << 20
	// MaxPCM16MixSamples bounds the temporary accumulator and returned frame.
	// Callers select the output length, so this also prevents an accidental
	// unbounded allocation at this small sample/buffer boundary.
	MaxPCM16MixSamples = 1 << 20
)

// PCM16MixError is a comparable error value, so the sample operation can
// expose errors.Is targets without mutable package state.
type PCM16MixError string

func (e PCM16MixError) Error() string { return string(e) }

const (
	ErrPCM16MixInvalidLength PCM16MixError = "PCM16 mix output length is invalid"
	ErrPCM16MixSourceLimit   PCM16MixError = "PCM16 mix source count exceeds supported bound"
	ErrPCM16MixSourceTooLong PCM16MixError = "PCM16 mix source exceeds output length"
)

// MixPCM16Samples sums signed PCM16 sources sample-by-sample and clips each
// total once to the PCM16 range. It is intentionally only a sample/buffer
// operation: it does not know about devices, clocks, codecs, metadata, or
// lifecycle state.
//
// The returned slice always has outputLength samples. A shorter or nil source
// contributes silence for its missing tail; an input longer than
// outputLength is rejected so callers cannot silently drop samples. Inputs
// are read only and are neither retained nor mutated. Source order therefore
// does not affect the result, while callers remain responsible for preserving
// any source attribution alongside their input slices.
//
// ValidatePCM16MixBounds checks the source and output limits without
// allocating or consuming any input. Callers that pull from a queue or decode
// bytes before calling MixPCM16Samples can use it to reject unsupported work
// before making that state change.
func ValidatePCM16MixBounds(sourceCount, outputLength int) error {
	if outputLength < 0 || outputLength > MaxPCM16MixSamples {
		return fmt.Errorf("%w: got %d samples, want 0..%d", ErrPCM16MixInvalidLength, outputLength, MaxPCM16MixSamples)
	}
	if sourceCount > MaxPCM16MixSources {
		return fmt.Errorf("%w: got %d sources, want at most %d", ErrPCM16MixSourceLimit, sourceCount, MaxPCM16MixSources)
	}
	return nil
}

// The source and output bounds are explicit. With at most
// MaxPCM16MixSources int16 inputs, int64 accumulation is mathematically safe
// before the final clip, including source counts beyond the int32 threshold.
func MixPCM16Samples(sources [][]int16, outputLength int) ([]int16, error) {
	if err := ValidatePCM16MixBounds(len(sources), outputLength); err != nil {
		return nil, err
	}
	for index, source := range sources {
		if len(source) > outputLength {
			return nil, fmt.Errorf("%w: source %d has %d samples, want at most %d", ErrPCM16MixSourceTooLong, index, len(source), outputLength)
		}
	}

	accumulated := make([]int64, outputLength)
	for _, source := range sources {
		for index, sample := range source {
			accumulated[index] += int64(sample)
		}
	}

	result := make([]int16, outputLength)
	for index, sample := range accumulated {
		if sample > pcm16MaxSample {
			sample = pcm16MaxSample
		} else if sample < pcm16MinSample {
			sample = pcm16MinSample
		}
		result[index] = int16(sample)
	}
	return result, nil
}

// MixPCM16Bytes decodes, mixes, and encodes signed little-endian PCM16
// sources. outputLength is the requested number of interleaved samples, not
// a byte count. A source shorter than outputLength contributes silence for its
// missing tail; a source longer than outputLength is rejected instead of being
// silently truncated. The byte and sample buffers are temporary and are never
// retained or aliased with the caller's input.
//
// All source and output bounds, including PCM16 alignment, are checked before
// any source is decoded or any output-sized buffer is allocated. The largest
// temporary allocation is proportional to the actual source sample lengths,
// plus the bounded output accumulator/result and encoded frame.
func MixPCM16Bytes(sources [][]byte, outputLength int) ([]byte, error) {
	if err := ValidatePCM16MixBounds(len(sources), outputLength); err != nil {
		return nil, err
	}
	maxInt := int(^uint(0) >> 1)
	if outputLength > maxInt/2 {
		return nil, fmt.Errorf("%w: %d samples cannot be represented as bytes", ErrPCM16MixInvalidLength, outputLength)
	}

	sourceLengths := make([]int, len(sources))
	for index, source := range sources {
		if len(source)%2 != 0 {
			return nil, fmt.Errorf("%w: source %d has %d bytes", codec.ErrPCM16OddLength, index, len(source))
		}
		sampleLength := len(source) / 2
		if sampleLength > outputLength {
			return nil, fmt.Errorf("%w: source %d has %d samples, want at most %d", ErrPCM16MixSourceTooLong, index, sampleLength, outputLength)
		}
		sourceLengths[index] = sampleLength
	}

	decoded := make([][]int16, len(sources))
	for index, source := range sources {
		if sourceLengths[index] == 0 {
			continue
		}
		samples := make([]int16, sourceLengths[index])
		if err := codec.DecodePCM16Into(samples, source); err != nil {
			return nil, fmt.Errorf("decode PCM16 source %d: %w", index, err)
		}
		decoded[index] = samples
	}

	mixed, err := MixPCM16Samples(decoded, outputLength)
	if err != nil {
		return nil, fmt.Errorf("mix PCM16 bytes: %w", err)
	}
	encoded := make([]byte, outputLength*2)
	if err := codec.EncodePCM16Into(encoded, mixed); err != nil {
		return nil, fmt.Errorf("encode PCM16 mix: %w", err)
	}
	return encoded, nil
}
