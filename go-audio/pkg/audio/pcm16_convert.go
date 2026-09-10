package audio

import (
	"errors"
	"fmt"
	"math"
	"math/bits"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

var (
	ErrInvalidPCM16ConversionRate     = errors.New("PCM16 conversion sample rates must be positive")
	ErrInvalidPCM16ConversionChannels = errors.New("PCM16 conversion channel counts must be positive")
	ErrPCM16ConversionAlignment       = errors.New("PCM16 conversion payload is not frame-aligned")
	ErrPCM16ConversionSize            = errors.New("PCM16 conversion output size overflows")
)

// ResamplePCM16 converts mono PCM16 samples between arbitrary positive sample
// rates. The established 16/24/48 kHz path uses the exact wavio converter;
// other positive rates use the same linear interpolation and rounding rules.
// The input is never modified or retained.
func ResamplePCM16(samples []int16, inputRate, outputRate int) ([]int16, error) {
	if inputRate <= 0 || outputRate <= 0 {
		return nil, fmt.Errorf("%w: %d Hz to %d Hz", ErrInvalidPCM16ConversionRate, inputRate, outputRate)
	}
	if err := validatePCM16SampleBytes(len(samples)); err != nil {
		return nil, err
	}
	if inputRate == outputRate {
		return append([]int16(nil), samples...), nil
	}
	if supportedPCM16Rate(inputRate) && supportedPCM16Rate(outputRate) {
		return resamplePCM16Supported(samples, inputRate, outputRate)
	}
	return resamplePCM16Arbitrary(samples, inputRate, outputRate)
}

func resamplePCM16Supported(samples []int16, inputRate, outputRate int) ([]int16, error) {
	converted, err := wavio.Resample(samples, inputRate, outputRate)
	if errors.Is(err, wavio.ErrResampleSize) {
		return nil, fmt.Errorf("%w: %w", ErrPCM16ConversionSize, err)
	}
	return converted, err
}

func resamplePCM16Arbitrary(samples []int16, inputRate, outputRate int) ([]int16, error) {
	if len(samples) == 0 {
		return []int16{}, nil
	}
	outputLength, err := checkedPCM16OutputLength(len(samples), inputRate, outputRate)
	if err != nil {
		return nil, err
	}
	if err := validatePCM16SampleBytes(outputLength); err != nil {
		return nil, fmt.Errorf("%w: %d output samples", err, outputLength)
	}
	converted := make([]int16, outputLength)
	for outputIndex := range converted {
		position, err := pcm16SourcePosition(outputIndex, inputRate, outputRate, "sample")
		if err != nil {
			return nil, err
		}
		sourceIndex := int(position)
		if sourceIndex >= len(samples)-1 {
			converted[outputIndex] = samples[len(samples)-1]
			continue
		}
		fraction := position - float64(sourceIndex)
		value := float64(samples[sourceIndex]) + (float64(samples[sourceIndex+1])-float64(samples[sourceIndex]))*fraction
		converted[outputIndex] = int16(math.Round(value))
	}
	return converted, nil
}

// ConvertPCM16Bytes converts interleaved little-endian PCM16 between channel
// layouts and arbitrary positive sample rates. Mono expansion duplicates the
// source channel; downmixing averages source channels; extra output channels
// repeat the final available source channel. The returned bytes are owned by
// the caller.
func ConvertPCM16Bytes(pcm []byte, sourceChannels, sourceRate, targetChannels, targetRate int) ([]byte, error) {
	if invalidPCM16ConversionChannels(sourceChannels, targetChannels) {
		return nil, pcm16ConversionChannelError(sourceChannels, targetChannels)
	}
	if sourceRate <= 0 || targetRate <= 0 {
		return nil, fmt.Errorf("%w: source=%d target=%d", ErrInvalidPCM16ConversionRate, sourceRate, targetRate)
	}
	if len(pcm)%(2*sourceChannels) != 0 {
		return nil, fmt.Errorf("%w: got %d bytes, frame size %d", ErrPCM16ConversionAlignment, len(pcm), 2*sourceChannels)
	}
	if len(pcm) == 0 {
		return []byte{}, nil
	}
	if sourceRate == targetRate && sourceChannels == targetChannels {
		return append([]byte(nil), pcm...), nil
	}

	sourceFrames := len(pcm) / (2 * sourceChannels)
	outputFrames, err := checkedPCM16OutputLength(sourceFrames, sourceRate, targetRate)
	if err != nil {
		return nil, fmt.Errorf("%w: output frames", err)
	} else {
		outputFrames = minimumPCM16Frames(outputFrames)
	}
	outputSampleCount, err := checkedPCM16OutputSampleCount(outputFrames, targetChannels)
	if err != nil {
		return nil, err
	}
	sourceSamples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, err
	}
	outputSamples := make([]int16, outputSampleCount)
	readSample := func(frame, channel int) int16 {
		return sourceSamples[frame*sourceChannels+channel]
	}
	for outputFrame := 0; outputFrame < outputFrames; outputFrame++ {
		position := float64(outputFrame) * float64(sourceRate) / float64(targetRate)
		lower := int(position)
		if lower >= sourceFrames {
			lower = sourceFrames - 1
		}
		upper := lower + 1
		if upper >= sourceFrames {
			upper = sourceFrames - 1
		}
		fraction := position - float64(lower)
		for targetChannel := 0; targetChannel < targetChannels; targetChannel++ {
			var value float64
			switch {
			case sourceChannels == 1:
				value = interpolatePCM16(readSample(lower, 0), readSample(upper, 0), fraction)
			case targetChannels == 1:
				var lowerSum, upperSum int64
				for sourceChannel := 0; sourceChannel < sourceChannels; sourceChannel++ {
					lowerSum += int64(readSample(lower, sourceChannel))
					upperSum += int64(readSample(upper, sourceChannel))
				}
				value = interpolateFloat(float64(lowerSum)/float64(sourceChannels), float64(upperSum)/float64(sourceChannels), fraction)
			case sourceChannels == targetChannels:
				value = interpolatePCM16(readSample(lower, targetChannel), readSample(upper, targetChannel), fraction)
			default:
				sourceChannel := targetChannel
				if sourceChannel >= sourceChannels {
					sourceChannel = sourceChannels - 1
				}
				value = interpolatePCM16(readSample(lower, sourceChannel), readSample(upper, sourceChannel), fraction)
			}
			if value > 32767 {
				value = 32767
			} else if value < -32768 {
				value = -32768
			}
			outputSamples[outputFrame*targetChannels+targetChannel] = int16(math.Round(value))
		}
	}
	return codec.EncodePCM16(outputSamples), nil
}

func invalidPCM16ConversionChannels(sourceChannels, targetChannels int) bool {
	return sourceChannels <= 0 || targetChannels <= 0 || sourceChannels > maximumPCM16Int()/2
}

func pcm16ConversionChannelError(sourceChannels, targetChannels int) error {
	if sourceChannels <= 0 || targetChannels <= 0 {
		return fmt.Errorf("%w: source=%d target=%d", ErrInvalidPCM16ConversionChannels, sourceChannels, targetChannels)
	}
	return fmt.Errorf("%w: source frame size for %d channels", ErrPCM16ConversionSize, sourceChannels)
}

func minimumPCM16Frames(outputFrames int) int {
	if outputFrames < 1 {
		return 1
	}
	return outputFrames
}

func pcm16SourcePosition(outputIndex, inputRate, outputRate int, unit string) (float64, error) {
	position := float64(outputIndex) * float64(inputRate) / float64(outputRate)
	if !isFinitePCM16Position(position) {
		return 0, fmt.Errorf("%w: nonfinite source position at output %s %d", ErrPCM16ConversionSize, unit, outputIndex)
	}
	return position, nil
}

func maximumPCM16Int() int {
	return int(^uint(0) >> 1)
}

func checkedPCM16OutputLength(inputLength, inputRate, outputRate int) (int, error) {
	if inputLength < 0 || inputRate <= 0 || outputRate <= 0 {
		return 0, fmt.Errorf("%w: invalid dimensions input=%d source=%d target=%d", ErrPCM16ConversionSize, inputLength, inputRate, outputRate)
	}
	if inputLength == 0 {
		return 0, nil
	}
	outputLength, overflow := ceilPCM16ProductQuotient(uint64(inputLength), uint64(outputRate), uint64(inputRate))
	maximumSamples := uint64(maximumPCM16Int() / 2)
	if overflow || outputLength == 0 || outputLength > maximumSamples {
		return 0, fmt.Errorf("%w: input=%d source=%d target=%d", ErrPCM16ConversionSize, inputLength, inputRate, outputRate)
	}
	return int(outputLength), nil
}

func ceilPCM16ProductQuotient(left, right, divisor uint64) (uint64, bool) {
	whole := left / divisor
	remainder := left % divisor
	if whole > ^uint64(0)/right {
		return 0, true
	}
	wholeProduct := whole * right
	partialHigh, partialLow := bits.Mul64(remainder, right)
	if partialHigh >= divisor {
		return 0, true
	}
	partial, remainderProduct := bits.Div64(partialHigh, partialLow, divisor)
	if remainderProduct != 0 {
		if partial == ^uint64(0) {
			return 0, true
		}
		partial++
	}
	if wholeProduct > ^uint64(0)-partial {
		return 0, true
	}
	return wholeProduct + partial, false
}

func checkedPCM16OutputSampleCount(outputFrames, targetChannels int) (int, error) {
	if outputFrames < 0 || targetChannels <= 0 {
		return 0, fmt.Errorf("%w: frames=%d channels=%d", ErrPCM16ConversionSize, outputFrames, targetChannels)
	}
	maximumSamples := maximumPCM16Int() / 2
	if outputFrames > maximumSamples/targetChannels {
		return 0, fmt.Errorf("%w: frames=%d channels=%d", ErrPCM16ConversionSize, outputFrames, targetChannels)
	}
	return outputFrames * targetChannels, nil
}

func validatePCM16SampleBytes(sampleCount int) error {
	if sampleCount < 0 || sampleCount > maximumPCM16Int()/2 {
		return fmt.Errorf("%w: %d samples", ErrPCM16ConversionSize, sampleCount)
	}
	return nil
}

func isFinitePCM16Position(position float64) bool {
	return !math.IsNaN(position) && !math.IsInf(position, 0) && position >= 0
}

func supportedPCM16Rate(rate int) bool {
	return rate == wavio.Rate16kHz || rate == wavio.Rate24kHz || rate == wavio.Rate48kHz
}

func interpolatePCM16(left, right int16, fraction float64) float64 {
	return interpolateFloat(float64(left), float64(right), fraction)
}

func interpolateFloat(left, right, fraction float64) float64 {
	return left + (right-left)*fraction
}
