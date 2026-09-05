package audio

import (
	"errors"
	"fmt"
	"math"

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
	if inputRate == outputRate {
		return append([]int16(nil), samples...), nil
	}
	if supportedPCM16Rate(inputRate) && supportedPCM16Rate(outputRate) {
		return wavio.Resample(samples, inputRate, outputRate)
	}
	if len(samples) == 0 {
		return []int16{}, nil
	}
	outputLengthFloat := math.Ceil(float64(len(samples)) * float64(outputRate) / float64(inputRate))
	maximumInt := int(^uint(0) >> 1)
	if outputLengthFloat > float64(maximumInt) {
		return nil, fmt.Errorf("%w: %g samples", ErrPCM16ConversionSize, outputLengthFloat)
	}
	outputLength := int(outputLengthFloat)
	converted := make([]int16, outputLength)
	for outputIndex := range converted {
		position := float64(outputIndex) * float64(inputRate) / float64(outputRate)
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
	if sourceChannels <= 0 || targetChannels <= 0 {
		return nil, fmt.Errorf("%w: source=%d target=%d", ErrInvalidPCM16ConversionChannels, sourceChannels, targetChannels)
	}
	if sourceRate <= 0 || targetRate <= 0 {
		return nil, fmt.Errorf("%w: source=%d target=%d", ErrInvalidPCM16ConversionRate, sourceRate, targetRate)
	}
	frameBytes := 2 * sourceChannels
	if len(pcm)%frameBytes != 0 {
		return nil, fmt.Errorf("%w: got %d bytes, frame size %d", ErrPCM16ConversionAlignment, len(pcm), frameBytes)
	}
	if len(pcm) == 0 {
		return []byte{}, nil
	}
	sourceSamples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, err
	}
	if sourceRate == targetRate && sourceChannels == targetChannels {
		return append([]byte(nil), pcm...), nil
	}

	sourceFrames := len(pcm) / frameBytes
	outputFramesFloat := math.Ceil(float64(sourceFrames) * float64(targetRate) / float64(sourceRate))
	maximumInt := int(^uint(0) >> 1)
	if outputFramesFloat > float64(maximumInt)/float64(targetChannels) {
		return nil, fmt.Errorf("%w: %g frames", ErrPCM16ConversionSize, outputFramesFloat)
	}
	outputFrames := int(outputFramesFloat)
	if outputFrames < 1 {
		outputFrames = 1
	}
	outputSamples := make([]int16, outputFrames*targetChannels)
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

func supportedPCM16Rate(rate int) bool {
	return rate == wavio.Rate16kHz || rate == wavio.Rate24kHz || rate == wavio.Rate48kHz
}

func interpolatePCM16(left, right int16, fraction float64) float64 {
	return interpolateFloat(float64(left), float64(right), fraction)
}

func interpolateFloat(left, right, fraction float64) float64 {
	return left + (right-left)*fraction
}
