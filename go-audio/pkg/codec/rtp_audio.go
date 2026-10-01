package codec

import (
	"encoding/binary"
	"strings"
)

// DecodeRTPAudioPayload decodes the audio payload formats accepted by the
// RTSP/RTP source. RTP framing remains owned by the transport; this function
// only converts a payload into signed PCM16 samples.
//
// L16/PCM16/RAW payloads are big-endian and truncate a trailing byte, matching
// the historical RTSP decoder. PCMA and PCMU decode G.711 A-law and mu-law.
// Unknown codecs retain the historical byte-preserving fallback: complete
// big-endian pairs become samples and a trailing byte is shifted by eight.
func DecodeRTPAudioPayload(codecName string, payload []byte) []int16 {
	if len(payload) == 0 {
		return nil
	}
	codecName = strings.TrimPrefix(strings.ToUpper(codecName), "AUDIO/")
	if codecName == "L16" || codecName == "PCM16" || codecName == "RAW" {
		out := make([]int16, len(payload)/2)
		for index := range out {
			out[index] = int16(binary.BigEndian.Uint16(payload[index*2:]))
		}
		return out
	}
	out := make([]int16, len(payload))
	for index, value := range payload {
		switch {
		case codecName == "PCMA" || codecName == "G711A":
			out[index] = decodeALaw(value)
		case codecName == "PCMU" || codecName == "G711U":
			out[index] = decodeMuLaw(value)
		case index*2+1 < len(payload):
			out[index] = int16(binary.BigEndian.Uint16(payload[index*2:]))
		default:
			out[index] = int16(value) << pcm8ToPCM16Shift
		}
	}
	return out
}

// G.711 code-word fields (ITU-T G.711): sign bit, 3-bit segment (exponent)
// and 4-bit mantissa, plus the mu-law bias and the A-law even-bit toggle.
const (
	g711MantissaMask  = 0x0f
	g711SegmentMask   = 0x70
	g711SegmentShift  = 4
	g711SignBit       = 0x80
	muLawBias         = 132
	muLawMantissaLeft = 3
	aLawToggleMask    = 0x55
	aLawMantissaLeft  = 4
	aLawSegmentBase   = 0x100
	pcm8ToPCM16Shift  = 8
)

func decodeMuLaw(value byte) int16 {
	value = ^value
	sample := int16((value&g711MantissaMask)<<muLawMantissaLeft + muLawBias)
	sample <<= (value & g711SegmentMask) >> g711SegmentShift
	if value&g711SignBit != 0 {
		return muLawBias - sample
	}
	return sample - muLawBias
}

func decodeALaw(value byte) int16 {
	value ^= aLawToggleMask
	sample := int16(value&g711MantissaMask) << aLawMantissaLeft
	if value&g711SegmentMask != 0 {
		sample += aLawSegmentBase
		sample <<= (value&g711SegmentMask)>>g711SegmentShift - 1
	}
	if value&0x80 != 0 {
		return sample
	}
	return -sample
}
