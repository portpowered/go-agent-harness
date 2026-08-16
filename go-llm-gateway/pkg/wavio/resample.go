package wavio

import (
	"errors"
	"fmt"
)

const (
	// Rate48kHz is the 48 kHz sample rate supported by Resample. It is kept
	// separate from the WAV read/write rate set because this package's WAV
	// contract currently accepts only 16 kHz and 24 kHz files.
	Rate48kHz = 48000

	// MaxResampleRoundTripErrorLSB is the measured maximum absolute PCM16 LSB
	// error for a 16 kHz -> 48 kHz -> 16 kHz round trip. The exact rational
	// phases used by Resample make the bound zero.
	MaxResampleRoundTripErrorLSB = 0

	pcm16Minimum int64 = -32768
	pcm16Maximum int64 = 32767
)

var (
	// ErrUnsupportedResampleRate identifies a rate outside Resample's closed
	// set of 16 kHz, 24 kHz, and 48 kHz.
	ErrUnsupportedResampleRate = errors.New("unsupported resample rate")
	// ErrResampleSize identifies an output length that cannot be represented
	// safely as an allocated PCM16 slice.
	ErrResampleSize = errors.New("resample output size overflow")
)

// UnsupportedResampleRateError reports the input or output rate rejected by
// Resample. It also matches the package's general unsupported-rate sentinels
// for callers that use the existing WAV validation taxonomy.
type UnsupportedResampleRateError struct {
	Direction string
	Rate      int
}

func (e *UnsupportedResampleRateError) Error() string {
	return fmt.Sprintf("unsupported resample %s rate %d; want 16000, 24000, or 48000 Hz", e.Direction, e.Rate)
}

func (e *UnsupportedResampleRateError) Unwrap() error { return ErrUnsupportedResampleRate }

func (e *UnsupportedResampleRateError) Is(target error) bool {
	return target == ErrUnsupportedResampleRate || target == ErrUnsupported || target == ErrUnsupportedRate
}

// ResampleSizeError reports an output length that would overflow the host's
// slice or allocation-size representation.
type ResampleSizeError struct {
	InputLength int
	InputRate   int
	OutputRate  int
}

func (e *ResampleSizeError) Error() string {
	return fmt.Sprintf("resample output size overflows for %d samples at %d Hz to %d Hz", e.InputLength, e.InputRate, e.OutputRate)
}

func (e *ResampleSizeError) Unwrap() error { return ErrResampleSize }

func (e *ResampleSizeError) Is(target error) bool {
	return target == ErrResampleSize || target == ErrSize
}

// Resample converts mono PCM16 samples between 16 kHz, 24 kHz, and 48 kHz.
// The input is never modified or retained, and a supported identity-rate
// conversion returns a defensive copy.
//
// Output length is exactly ceil(len(samples) * outputRate / inputRate),
// including for an empty input and ratios that do not divide evenly. The
// length is calculated with checked integer arithmetic before allocation.
// Each output position uses its exact rational phase to linearly interpolate
// between adjacent source samples. Interpolation is rounded to the nearest
// integer, with halfway cases rounded away from zero. Positions beyond the
// last source interval extend the final source sample. Every result is
// saturated to the PCM16 range instead of wrapping.
//
// The measured maximum absolute error for a 16 kHz -> 48 kHz -> 16 kHz
// round trip is MaxResampleRoundTripErrorLSB (0 PCM16 LSBs), because the
// reverse conversion samples the exact source-phase positions.
func Resample(samples []int16, inputRate, outputRate int) ([]int16, error) {
	if err := validateResampleRate("input", inputRate); err != nil {
		return nil, err
	}
	if err := validateResampleRate("output", outputRate); err != nil {
		return nil, err
	}

	outputLength, err := resampleOutputLength(len(samples), inputRate, outputRate)
	if err != nil {
		return nil, err
	}
	if outputLength == 0 {
		return make([]int16, 0), nil
	}

	result := make([]int16, outputLength)
	if inputRate == outputRate {
		copy(result, samples)
		return result, nil
	}

	inputRateValue := uint64(inputRate)
	outputRateValue := uint64(outputRate)
	lastSourceIndex := uint64(len(samples) - 1)
	for outputIndex := range result {
		position := uint64(outputIndex) * inputRateValue
		sourceIndex := position / outputRateValue
		phase := position % outputRateValue
		if sourceIndex >= lastSourceIndex {
			result[outputIndex] = samples[lastSourceIndex]
			continue
		}

		result[outputIndex] = interpolatePCM16(
			samples[sourceIndex],
			samples[sourceIndex+1],
			phase,
			outputRateValue,
		)
	}
	return result, nil
}

func validateResampleRate(direction string, rate int) error {
	if rate == Rate16kHz || rate == Rate24kHz || rate == Rate48kHz {
		return nil
	}
	return &UnsupportedResampleRateError{Direction: direction, Rate: rate}
}

func resampleOutputLength(inputLength, inputRate, outputRate int) (int, error) {
	if inputLength == 0 {
		return 0, nil
	}

	inputLengthValue := uint64(inputLength)
	inputRateValue := uint64(inputRate)
	outputRateValue := uint64(outputRate)
	wholeInputSamples := inputLengthValue / inputRateValue
	partialInputSamples := inputLengthValue % inputRateValue

	if wholeInputSamples > ^uint64(0)/outputRateValue {
		return 0, &ResampleSizeError{InputLength: inputLength, InputRate: inputRate, OutputRate: outputRate}
	}
	wholeOutputSamples := wholeInputSamples * outputRateValue
	partialProduct := partialInputSamples * outputRateValue
	partialOutputSamples := partialProduct / inputRateValue
	if partialProduct%inputRateValue != 0 {
		partialOutputSamples++
	}
	if wholeOutputSamples > ^uint64(0)-partialOutputSamples {
		return 0, &ResampleSizeError{InputLength: inputLength, InputRate: inputRate, OutputRate: outputRate}
	}
	outputLength := wholeOutputSamples + partialOutputSamples

	// A PCM16 slice needs two addressable bytes per sample. Check both the
	// slice index limit and the byte-size limit before make can panic or wrap.
	maximumInt := uint64(^uint(0) >> 1)
	maximumSamples := maximumInt / uint64(2)
	if outputLength > maximumSamples {
		return 0, &ResampleSizeError{InputLength: inputLength, InputRate: inputRate, OutputRate: outputRate}
	}
	return int(outputLength), nil
}

func interpolatePCM16(left, right int16, phase, denominator uint64) int16 {
	delta := int64(right) - int64(left)
	product := delta * int64(phase)
	denominatorValue := int64(denominator)
	var roundedDelta int64
	if product >= 0 {
		roundedDelta = (product + denominatorValue/2) / denominatorValue
	} else {
		roundedDelta = -((-product + denominatorValue/2) / denominatorValue)
	}
	return saturatePCM16(int64(left) + roundedDelta)
}

func saturatePCM16(value int64) int16 {
	if value < pcm16Minimum {
		return int16(pcm16Minimum)
	}
	if value > pcm16Maximum {
		return int16(pcm16Maximum)
	}
	return int16(value)
}
