package mixer

import "fmt"

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
// The source and output bounds are explicit. With at most
// MaxPCM16MixSources int16 inputs, int64 accumulation is mathematically safe
// before the final clip, including source counts beyond the int32 threshold.
func MixPCM16Samples(sources [][]int16, outputLength int) ([]int16, error) {
	if outputLength < 0 || outputLength > MaxPCM16MixSamples {
		return nil, fmt.Errorf("%w: got %d samples, want 0..%d", ErrPCM16MixInvalidLength, outputLength, MaxPCM16MixSamples)
	}
	if len(sources) > MaxPCM16MixSources {
		return nil, fmt.Errorf("%w: got %d sources, want at most %d", ErrPCM16MixSourceLimit, len(sources), MaxPCM16MixSources)
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
