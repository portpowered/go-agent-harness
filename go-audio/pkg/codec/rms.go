package codec

import (
	"encoding/binary"
	"math"
)

// PCM16FullScale is the magnitude of the most negative PCM16 sample. Dividing
// an RMS by it maps integer amplitude onto the normalized [0, 1] scale.
const PCM16FullScale = 32768

// RMS returns the root-mean-square of signed PCM16 samples in integer
// amplitude units. Empty input has zero energy.
func RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// PCM16RMS returns the root-mean-square of signed little-endian PCM16 bytes in
// integer amplitude units without decoding into a sample buffer. Only complete
// samples are measured: a trailing partial byte is ignored and input shorter
// than one sample has zero energy. Callers that must reject misaligned audio
// validate it first (see ValidatePCM16).
func PCM16RMS(encoded []byte) float64 {
	count := len(encoded) / 2
	if count == 0 {
		return 0
	}
	var sum float64
	for offset := 0; offset+1 < len(encoded); offset += 2 {
		value := float64(int16(binary.LittleEndian.Uint16(encoded[offset:])))
		sum += value * value
	}
	return math.Sqrt(sum / float64(count))
}
