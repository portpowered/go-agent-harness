package codec

import (
	"encoding/binary"
	"fmt"
	"math"
)

// SampleEncoding identifies the byte representation of one audio sample.
// SampleFormat.BitsPerSample is the complete container width, including any
// padding bits present in an extensible PCM format.
type SampleEncoding uint8

const (
	// SampleEncodingPCM is signed little-endian PCM, except for the unsigned
	// 8-bit PCM representation defined by the audio container convention.
	SampleEncodingPCM SampleEncoding = iota + 1
	// SampleEncodingIEEEFloat is little-endian IEEE-754 float32 or float64.
	SampleEncodingIEEEFloat
)

const (
	bitsPerByte              = 8
	pcm8Bits                 = 8
	pcm16Bits                = 16
	pcm24Bits                = 24
	pcm32Bits                = 32
	pcm64Bits                = 64
	pcm8Offset               = 128
	pcm16FractionBits        = 15
	pcm24FractionBits        = 23
	pcm32FractionBits        = 31
	pcm64FractionBits        = 63
	pcm24SignBit      uint32 = 1 << (pcm24Bits - 1)
	pcm24Mask         uint32 = (1 << pcm24Bits) - 1
)

// SampleFormat describes one bounded sample value. ValidBitsPerSample is
// metadata only: samples are normalized by their complete container width so
// reduced-valid-bit PCM retains the same historical scaling and low padding
// bits are not silently shifted away.
type SampleFormat struct {
	Encoding           SampleEncoding
	BitsPerSample      int
	ValidBitsPerSample int
}

// SampleError is a stable, immutable error identity for sample decoding
// failures. Constants avoid mutable package state while remaining usable with
// errors.Is through the wrapped error returned by the decoder.
type SampleError string

func (e SampleError) Error() string { return string(e) }

const (
	// ErrInvalidSampleFormat identifies malformed container or valid-bit
	// metadata.
	ErrInvalidSampleFormat SampleError = "invalid audio sample format"
	// ErrUnsupportedSampleEncoding identifies an encoding not understood by
	// the canonical sample decoder.
	ErrUnsupportedSampleEncoding SampleError = "unsupported audio sample encoding"
	// ErrUnsupportedSampleWidth identifies a container width not supported for
	// the selected encoding.
	ErrUnsupportedSampleWidth SampleError = "unsupported audio sample width"
	// ErrSampleInputTooShort identifies a byte slice that cannot contain one
	// complete sample.
	ErrSampleInputTooShort SampleError = "audio sample input is too short"
	// ErrNonFiniteSample identifies NaN and either infinity at the public
	// measurement boundary.
	ErrNonFiniteSample SampleError = "audio sample is non-finite"
)

// ByteWidth validates the format and returns the number of bytes occupied by
// one sample. It does not inspect or retain audio data.
func (f SampleFormat) ByteWidth() (int, error) {
	if f.BitsPerSample <= 0 || f.BitsPerSample%bitsPerByte != 0 {
		return 0, fmt.Errorf("%w: container bits=%d must be a positive multiple of 8", ErrInvalidSampleFormat, f.BitsPerSample)
	}
	if f.ValidBitsPerSample <= 0 || f.ValidBitsPerSample > f.BitsPerSample {
		return 0, fmt.Errorf("%w: valid bits=%d container bits=%d", ErrInvalidSampleFormat, f.ValidBitsPerSample, f.BitsPerSample)
	}
	switch f.Encoding {
	case SampleEncodingPCM:
		switch f.BitsPerSample {
		case pcm8Bits, pcm16Bits, pcm24Bits, pcm32Bits, pcm64Bits:
			return f.BitsPerSample / bitsPerByte, nil
		default:
			return 0, fmt.Errorf("%w: PCM container bits=%d", ErrUnsupportedSampleWidth, f.BitsPerSample)
		}
	case SampleEncodingIEEEFloat:
		switch f.BitsPerSample {
		case pcm32Bits, pcm64Bits:
			return f.BitsPerSample / bitsPerByte, nil
		default:
			return 0, fmt.Errorf("%w: IEEE-float container bits=%d", ErrUnsupportedSampleWidth, f.BitsPerSample)
		}
	default:
		return 0, fmt.Errorf("%w: value=%d", ErrUnsupportedSampleEncoding, f.Encoding)
	}
}

// Validate checks the format without reading audio data.
func (f SampleFormat) Validate() error {
	_, err := f.ByteWidth()
	return err
}

// DecodeSampleValue decodes one little-endian sample into the historical
// normalized float64 representation. The returned value is finite on success;
// the input is read-only and no allocation is performed.
func DecodeSampleValue(raw []byte, format SampleFormat) (float64, error) {
	sampleBytes, err := format.ByteWidth()
	if err != nil {
		return 0, err
	}
	if len(raw) < sampleBytes {
		return 0, fmt.Errorf("%w: got %d bytes, want %d", ErrSampleInputTooShort, len(raw), sampleBytes)
	}

	var value float64
	if format.Encoding == SampleEncodingIEEEFloat {
		switch format.BitsPerSample {
		case pcm32Bits:
			value = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw)))
		case pcm64Bits:
			value = math.Float64frombits(binary.LittleEndian.Uint64(raw))
		}
	} else {
		switch format.BitsPerSample {
		case pcm8Bits:
			// PCM8 is the one supported PCM width with an unsigned container.
			value = float64(int(raw[0])-pcm8Offset) / pcm8Offset
		case pcm16Bits:
			value = float64(int16(binary.LittleEndian.Uint16(raw))) / math.Ldexp(1, pcm16FractionBits)
		case pcm24Bits:
			value = float64(decodePCM24(raw)) / math.Ldexp(1, pcm24FractionBits)
		case pcm32Bits:
			value = float64(int32(binary.LittleEndian.Uint32(raw))) / math.Ldexp(1, pcm32FractionBits)
		case pcm64Bits:
			// The int64-to-float64 conversion intentionally happens before
			// normalization, matching the adapter's historical rounding at
			// the extrema of a PCM64 container.
			value = float64(int64(binary.LittleEndian.Uint64(raw))) / math.Ldexp(1, pcm64FractionBits)
		}
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%w: %v", ErrNonFiniteSample, value)
	}
	return value, nil
}

func decodePCM24(raw []byte) int32 {
	value := int32(uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16)
	if uint32(value)&pcm24SignBit != 0 {
		value |= ^int32(pcm24Mask)
	}
	return value
}
