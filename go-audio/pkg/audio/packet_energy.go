package audio

import (
	"fmt"
	"math"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// PacketEnergyError is a stable, immutable error identity for bounded packet
// measurement failures.
type PacketEnergyError string

func (e PacketEnergyError) Error() string { return string(e) }

const (
	// ErrInvalidPacketDimensions identifies dimensions that cannot describe a
	// bounded interleaved packet.
	ErrInvalidPacketDimensions PacketEnergyError = "invalid audio packet dimensions"
	// ErrPacketInputTooShort identifies a packet that ends before all requested
	// frames are present.
	ErrPacketInputTooShort PacketEnergyError = "audio packet input is too short"
	// ErrPacketEnergyOverflow identifies a finite sample square or sum that
	// cannot be represented as a finite float64.
	ErrPacketEnergyOverflow PacketEnergyError = "audio packet energy overflows float64"
)

// PacketEnergy sums the squares of every channel sample in an interleaved
// little-endian packet. frameStride includes any per-frame padding; padding is
// never measured. Dimensions are validated before indexing, and the function
// performs no packet-sized allocation or input mutation.
func PacketEnergy(data []byte, frames, channels, frameStride int, format codec.SampleFormat) (float64, error) {
	sampleBytes, err := validatePacketLayout(data, frames, channels, frameStride, format)
	if err != nil {
		return 0, err
	}
	return sumPacketEnergy(data, frames, channels, frameStride, sampleBytes, format)
}

func validatePacketLayout(data []byte, frames, channels, frameStride int, format codec.SampleFormat) (int, error) {
	if frames < 0 || channels <= 0 || frameStride <= 0 {
		return 0, fmt.Errorf("%w: frames=%d channels=%d stride=%d", ErrInvalidPacketDimensions, frames, channels, frameStride)
	}
	sampleBytes, err := format.ByteWidth()
	if err != nil {
		return 0, err
	}
	channelBytes, overflow := checkedPacketProduct(channels, sampleBytes)
	if overflow || channelBytes > frameStride {
		return 0, fmt.Errorf("%w: channels=%d sample-bytes=%d stride=%d", ErrInvalidPacketDimensions, channels, sampleBytes, frameStride)
	}
	if frames == 0 {
		return sampleBytes, nil
	}
	if frames > int(^uint(0)>>1)/frameStride {
		return 0, fmt.Errorf("%w: frames=%d stride=%d overflows int", ErrInvalidPacketDimensions, frames, frameStride)
	}
	if frames > len(data)/frameStride {
		return 0, fmt.Errorf("%w: got %d bytes for %d frames at stride %d", ErrPacketInputTooShort, len(data), frames, frameStride)
	}
	return sampleBytes, nil
}

func sumPacketEnergy(data []byte, frames, channels, frameStride, sampleBytes int, format codec.SampleFormat) (float64, error) {
	var energy float64
	for frame := 0; frame < frames; frame++ {
		frameEnergy, err := packetFrameEnergy(data, frame, channels, frameStride, sampleBytes, format)
		if err != nil {
			return 0, err
		}
		energy, err = addPacketEnergy(energy, frameEnergy)
		if err != nil {
			return 0, fmt.Errorf("%w: sum at frame=%d", err, frame)
		}
	}
	return energy, nil
}

func packetFrameEnergy(data []byte, frame, channels, frameStride, sampleBytes int, format codec.SampleFormat) (float64, error) {
	frameOffset := frame * frameStride
	var energy float64
	for channel := 0; channel < channels; channel++ {
		channelOffset := channel * sampleBytes
		value, err := codec.DecodeSampleValue(data[frameOffset+channelOffset:frameOffset+channelOffset+sampleBytes], format)
		if err != nil {
			return 0, fmt.Errorf("packet sample frame=%d channel=%d: %w", frame, channel, err)
		}
		square, err := sampleSquare(value)
		if err != nil {
			return 0, fmt.Errorf("%w: sample square at frame=%d channel=%d", err, frame, channel)
		}
		energy, err = addPacketEnergy(energy, square)
		if err != nil {
			return 0, fmt.Errorf("%w: frame=%d channel=%d", err, frame, channel)
		}
	}
	return energy, nil
}

func sampleSquare(value float64) (float64, error) {
	square := value * value
	if math.IsNaN(square) || math.IsInf(square, 0) {
		return 0, ErrPacketEnergyOverflow
	}
	return square, nil
}

func addPacketEnergy(energy, value float64) (float64, error) {
	next := energy + value
	if math.IsNaN(next) || math.IsInf(next, 0) {
		return 0, ErrPacketEnergyOverflow
	}
	return next, nil
}

func checkedPacketProduct(left, right int) (int, bool) {
	maximum := int(^uint(0) >> 1)
	if left < 0 || right < 0 || (right != 0 && left > maximum/right) {
		return 0, true
	}
	return left * right, false
}
