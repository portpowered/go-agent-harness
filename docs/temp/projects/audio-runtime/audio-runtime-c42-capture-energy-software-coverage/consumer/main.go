package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

const schema = "audio-runtime-c42-capture-energy-consumer.v1"

type report struct {
	Schema        string       `json:"schema"`
	Source        string       `json:"source"`
	GOOS          string       `json:"goos"`
	GOARCH        string       `json:"goarch"`
	IntBits       int          `json:"int_bits"`
	StartedAt     string       `json:"started_at"`
	DurationMS    int64        `json:"duration_ms"`
	CleanShutdown bool         `json:"clean_shutdown"`
	Cases         []caseReport `json:"cases"`
	Error         string       `json:"error,omitempty"`
}

type caseReport struct {
	Name           string    `json:"name"`
	API            string    `json:"api"`
	InputHex       string    `json:"input_hex"`
	Encoding       string    `json:"encoding"`
	BitsPerSample  int       `json:"bits_per_sample"`
	ValidBits      int       `json:"valid_bits_per_sample"`
	Frames         int       `json:"frames"`
	Channels       int       `json:"channels"`
	FrameStride    int       `json:"frame_stride"`
	ActualSamples  []float64 `json:"actual_samples,omitempty"`
	NegativeZero   []bool    `json:"negative_zero,omitempty"`
	ActualEnergy   *float64  `json:"actual_energy,omitempty"`
	ActualError    string    `json:"actual_error,omitempty"`
	ErrorKind      string    `json:"error_kind,omitempty"`
	InputUnchanged bool      `json:"input_unchanged"`
	Panicked       bool      `json:"panicked"`
	Panic          string    `json:"panic,omitempty"`
}

type decodeCase struct {
	name   string
	raw    []byte
	format codec.SampleFormat
}

type energyCase struct {
	name     string
	raw      []byte
	frames   int
	channels int
	stride   int
	format   codec.SampleFormat
}

type decodeResult struct {
	value    float64
	err      error
	panicked bool
	panic    string
}

type energyResult struct {
	energy   *float64
	samples  []float64
	err      error
	panicked bool
	panic    string
}

func main() {
	started := time.Now()
	result, err := run()
	result.StartedAt = started.UTC().Format(time.RFC3339Nano)
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		result.CleanShutdown = false
	}
	encoded, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if _, writeErr := os.Stdout.Write(encoded); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (report, error) {
	result := report{
		Schema:        schema,
		Source:        sourceRevision(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		IntBits:       32 << (^uint(0) >> 63),
		CleanShutdown: true,
	}
	for _, testCase := range decoderCases() {
		result.Cases = append(result.Cases, runDecodeCase(testCase))
	}
	for _, testCase := range energyCases() {
		result.Cases = append(result.Cases, runEnergyCase(testCase))
	}
	return result, nil
}

func sourceRevision() string {
	if revision := os.Getenv("C42_SOURCE_REVISION"); revision != "" {
		return revision
	}
	return "working-tree"
}

func decoderCases() []decodeCase {
	return []decodeCase{
		{name: "pcm8-zero", raw: []byte{0x00}, format: pcmFormat(8, 8)},
		{name: "pcm8-midpoint", raw: []byte{0x80}, format: pcmFormat(8, 8)},
		{name: "pcm8-maximum", raw: []byte{0xff}, format: pcmFormat(8, 8)},
		{name: "pcm16-minimum", raw: []byte{0x00, 0x80}, format: pcmFormat(16, 16)},
		{name: "pcm16-maximum", raw: []byte{0xff, 0x7f}, format: pcmFormat(16, 16)},
		{name: "pcm24-negative-one", raw: []byte{0xff, 0xff, 0xff}, format: pcmFormat(24, 24)},
		{name: "pcm24-maximum", raw: []byte{0xff, 0xff, 0x7f}, format: pcmFormat(24, 24)},
		{name: "pcm32-maximum", raw: []byte{0xff, 0xff, 0xff, 0x7f}, format: pcmFormat(32, 32)},
		{name: "pcm64-maximum-rounds-to-one", raw: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, format: pcmFormat(64, 64)},
		{name: "pcm16-reduced-valid-padding", raw: []byte{0x01, 0x40}, format: pcmFormat(16, 12)},
		{name: "pcm8-short", raw: nil, format: pcmFormat(8, 8)},
		{name: "pcm16-short", raw: []byte{0x00}, format: pcmFormat(16, 16)},
		{name: "pcm24-short", raw: []byte{0x00, 0x00}, format: pcmFormat(24, 24)},
		{name: "pcm32-short", raw: []byte{0x00, 0x00, 0x00}, format: pcmFormat(32, 32)},
		{name: "pcm64-short", raw: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, format: pcmFormat(64, 64)},
		{name: "float32-short", raw: []byte{0x00, 0x00, 0x00}, format: floatFormat(32)},
		{name: "float64-short", raw: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, format: floatFormat(64)},
		{name: "unsupported-encoding", raw: []byte{0x00, 0x00}, format: codec.SampleFormat{BitsPerSample: 16, ValidBitsPerSample: 16}},
		{name: "unsupported-pcm-width", raw: []byte{0x00, 0x00, 0x00, 0x00, 0x00}, format: pcmFormat(40, 40)},
		{name: "invalid-valid-bits-zero", raw: []byte{0x00, 0x00}, format: pcmFormat(16, 0)},
		{name: "invalid-valid-bits-too-large", raw: []byte{0x00, 0x00}, format: pcmFormat(16, 17)},
		{name: "float32-positive-zero", raw: littleEndian32(0x00000000), format: floatFormat(32)},
		{name: "float32-negative-zero", raw: littleEndian32(0x80000000), format: floatFormat(32)},
		{name: "float32-half", raw: littleEndian32(0x3f000000), format: floatFormat(32)},
		{name: "float32-outside-range", raw: littleEndian32(0x40200000), format: floatFormat(32)},
		{name: "float64-negative-half", raw: littleEndian64(0xbfe0000000000000), format: floatFormat(64)},
		{name: "float64-outside-range", raw: littleEndian64(0xc004000000000000), format: floatFormat(64)},
		{name: "float32-nan", raw: littleEndian32(0x7fc00001), format: floatFormat(32)},
		{name: "float64-positive-infinity", raw: littleEndian64(0x7ff0000000000000), format: floatFormat(64)},
	}
}

func energyCases() []energyCase {
	return append([]energyCase{
		{
			name:   "pcm16-stereo-padded-energy",
			raw:    []byte{0x00, 0x40, 0x00, 0xc0, 0xaa, 0xbb, 0x00, 0x00, 0x00, 0x80, 0xcc, 0xdd},
			frames: 2, channels: 2, stride: 6, format: pcmFormat(16, 16),
		},
		{name: "pcm8-mono-energy", raw: []byte{0x00, 0x80, 0xff}, frames: 3, channels: 1, stride: 1, format: pcmFormat(8, 8)},
		{name: "float32-stereo-energy", raw: float32Packet(0.5, -0.5, 2, 0), frames: 2, channels: 2, stride: 8, format: floatFormat(32)},
		{name: "float64-mono-energy", raw: float64Packet(1.5, -2), frames: 2, channels: 1, stride: 8, format: floatFormat(64)},
		{name: "zero-frames", raw: nil, frames: 0, channels: 1, stride: 2, format: pcmFormat(16, 16)},
		{name: "short-final-frame", raw: []byte{0, 0, 0, 0, 0}, frames: 2, channels: 1, stride: 3, format: pcmFormat(16, 16)},
		{name: "invalid-channel-stride", raw: []byte{0, 0}, frames: 1, channels: 2, stride: 2, format: pcmFormat(16, 16)},
		{name: "invalid-frame-overflow", raw: []byte{0}, frames: maxInt(), channels: 1, stride: 2, format: pcmFormat(16, 16)},
		{name: "float-nan-energy", raw: littleEndian64(0x7ff8000000000001), frames: 1, channels: 1, stride: 8, format: floatFormat(64)},
		{name: "float-square-overflow", raw: float64Packet(math.MaxFloat64), frames: 1, channels: 1, stride: 8, format: floatFormat(64)},
		{name: "float-sum-overflow", raw: float64Packet(1e154, 1e154), frames: 2, channels: 1, stride: 8, format: floatFormat(64)},
	}, c42EnergyCases()...)
}

func c42EnergyCases() []energyCase {
	return []energyCase{
		{
			name:   "c42-pcm16-three-channel-padded-valid12",
			raw:    []byte{0x01, 0x40, 0x00, 0xc0, 0x00, 0x20, 0xaa, 0xbb, 0x00, 0x00, 0x00, 0x80, 0x00, 0xe0, 0xcc, 0xdd, 0x13, 0x37},
			frames: 2, channels: 3, stride: 8, format: pcmFormat(16, 12),
		},
		{
			name:   "c42-pcm24-two-channel-padded-valid20",
			raw:    []byte{0x00, 0x00, 0x40, 0x00, 0x00, 0xc0, 0xde, 0xad, 0xff, 0xff, 0x7f, 0x00, 0x00, 0x00, 0xbe, 0xef, 0x5a},
			frames: 2, channels: 2, stride: 8, format: pcmFormat(24, 20),
		},
		{
			name:   "c42-pcm32-two-channel-padded-valid24",
			raw:    []byte{0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x00, 0xe0, 0xaa, 0xbb, 0xcc, 0xdd, 0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00, 0x60, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
			frames: 2, channels: 2, stride: 12, format: pcmFormat(32, 24),
		},
		{
			name:   "c42-float64-two-channel-padded",
			raw:    float64PaddedPacket(),
			frames: 2, channels: 2, stride: 24, format: floatFormat(64),
		},
		{
			name:   "c42-zero-frames-trailing-nonfinite",
			raw:    littleEndian64(0x7ff8000000000001),
			frames: 0, channels: 1, stride: 8, format: floatFormat(64),
		},
		{
			name:   "c42-zero-frames-malformed-format",
			raw:    nil,
			frames: 0, channels: 1, stride: 8,
			format: codec.SampleFormat{Encoding: codec.SampleEncodingIEEEFloat, BitsPerSample: 64, ValidBitsPerSample: 0},
		},
		{
			name:   "c42-float64-four-channel-sum-overflow",
			raw:    float64Packet(math.Ldexp(1, 511), math.Ldexp(1, 511), math.Ldexp(1, 511), math.Ldexp(1, 511)),
			frames: 1, channels: 4, stride: 32, format: floatFormat(64),
		},
		{
			name:   "c42-float64-four-frame-sum-overflow",
			raw:    float64Packet(math.Ldexp(1, 511), math.Ldexp(1, 511), math.Ldexp(1, 511), math.Ldexp(1, 511)),
			frames: 4, channels: 1, stride: 8, format: floatFormat(64),
		},
		{
			name:   "c42-float64-later-channel-nan",
			raw:    float64Packet(0.5, -0.25, math.NaN()),
			frames: 1, channels: 3, stride: 24, format: floatFormat(64),
		},
		{
			name:   "c42-float64-later-frame-negative-infinity",
			raw:    float64Packet(0.5, math.Inf(-1)),
			frames: 2, channels: 1, stride: 8, format: floatFormat(64),
		},
		{
			name:   "c42-pcm16-truncated-padded-final-frame",
			raw:    bytes.Repeat([]byte{0xa5}, 15),
			frames: 2, channels: 3, stride: 8, format: pcmFormat(16, 12),
		},
		{
			name:   "c42-pcm16-malformed-channel-stride",
			raw:    []byte{0x00},
			frames: 1, channels: 3, stride: 5, format: pcmFormat(16, 16),
		},
		{
			name:   "c42-pcm16-unrepresentable-frame-extent",
			raw:    []byte{0x00},
			frames: maxInt(), channels: 1, stride: 2, format: pcmFormat(16, 16),
		},
	}
}

func runDecodeCase(testCase decodeCase) (result caseReport) {
	input := append([]byte(nil), testCase.raw...)
	result = caseReport{
		Name:           testCase.name,
		API:            "codec.DecodeSampleValue",
		InputHex:       hex.EncodeToString(input),
		Encoding:       encodingName(testCase.format.Encoding),
		BitsPerSample:  testCase.format.BitsPerSample,
		ValidBits:      testCase.format.ValidBitsPerSample,
		InputUnchanged: true,
	}
	decoded := callDecode(input, testCase.format)
	result.ActualError = errorText(decoded.err)
	result.ErrorKind = errorKind(decoded.err)
	result.Panicked = decoded.panicked
	result.Panic = decoded.panic
	result.InputUnchanged = bytes.Equal(input, testCase.raw)
	if decoded.err == nil && !decoded.panicked {
		result.ActualSamples = []float64{decoded.value}
		result.NegativeZero = []bool{isNegativeZero(decoded.value)}
	}
	return result
}

func runEnergyCase(testCase energyCase) (result caseReport) {
	input := append([]byte(nil), testCase.raw...)
	result = caseReport{
		Name:           testCase.name,
		API:            "codec.PacketEnergy",
		InputHex:       hex.EncodeToString(input),
		Encoding:       encodingName(testCase.format.Encoding),
		BitsPerSample:  testCase.format.BitsPerSample,
		ValidBits:      testCase.format.ValidBitsPerSample,
		Frames:         testCase.frames,
		Channels:       testCase.channels,
		FrameStride:    testCase.stride,
		InputUnchanged: true,
	}
	measured := callEnergy(input, testCase)
	result.ActualError = errorText(measured.err)
	result.ErrorKind = errorKind(measured.err)
	result.Panicked = measured.panicked
	result.Panic = measured.panic
	result.InputUnchanged = bytes.Equal(input, testCase.raw)
	if measured.energy != nil && !measured.panicked {
		result.ActualEnergy = measured.energy
		result.ActualSamples = measured.samples
		result.NegativeZero = make([]bool, len(measured.samples))
		for index, sample := range measured.samples {
			result.NegativeZero[index] = isNegativeZero(sample)
		}
	}
	return result
}

func callDecode(raw []byte, format codec.SampleFormat) (result decodeResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result.panicked = true
			result.panic = fmt.Sprint(recovered)
		}
	}()
	result.value, result.err = codec.DecodeSampleValue(raw, format)
	return result
}

func callEnergy(raw []byte, testCase energyCase) (result energyResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result.panicked = true
			result.panic = fmt.Sprint(recovered)
		}
	}()
	value, err := codec.PacketEnergy(raw, testCase.frames, testCase.channels, testCase.stride, testCase.format)
	result.err = err
	if err == nil {
		result.energy = &value
		result.samples = decodePacketSamples(raw, testCase)
	}
	return result
}

func decodePacketSamples(raw []byte, testCase energyCase) []float64 {
	if testCase.frames == 0 {
		return []float64{}
	}
	width, err := testCase.format.ByteWidth()
	if err != nil || testCase.frames < 0 || testCase.channels <= 0 || testCase.stride <= 0 || testCase.frames > len(raw)/testCase.stride || testCase.channels > testCase.stride/width {
		return nil
	}
	samples := make([]float64, 0, testCase.frames*testCase.channels)
	for frame := 0; frame < testCase.frames; frame++ {
		frameOffset := frame * testCase.stride
		for channel := 0; channel < testCase.channels; channel++ {
			offset := frameOffset + channel*width
			value, decodeErr := codec.DecodeSampleValue(raw[offset:offset+width], testCase.format)
			if decodeErr != nil {
				return nil
			}
			samples = append(samples, value)
		}
	}
	return samples
}

func errorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, codec.ErrInvalidSampleFormat):
		return "ErrInvalidSampleFormat"
	case errors.Is(err, codec.ErrUnsupportedSampleEncoding):
		return "ErrUnsupportedSampleEncoding"
	case errors.Is(err, codec.ErrUnsupportedSampleWidth):
		return "ErrUnsupportedSampleWidth"
	case errors.Is(err, codec.ErrSampleInputTooShort):
		return "ErrSampleInputTooShort"
	case errors.Is(err, codec.ErrNonFiniteSample):
		return "ErrNonFiniteSample"
	case errors.Is(err, codec.ErrInvalidPacketDimensions):
		return "ErrInvalidPacketDimensions"
	case errors.Is(err, codec.ErrPacketInputTooShort):
		return "ErrPacketInputTooShort"
	case errors.Is(err, codec.ErrPacketEnergyOverflow):
		return "ErrPacketEnergyOverflow"
	default:
		return "unknown"
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func encodingName(encoding codec.SampleEncoding) string {
	switch encoding {
	case codec.SampleEncodingPCM:
		return "pcm"
	case codec.SampleEncodingIEEEFloat:
		return "ieee-float"
	default:
		return "unknown"
	}
}

func isNegativeZero(value float64) bool {
	return value == 0 && math.Signbit(value)
}

func pcmFormat(bits, validBits int) codec.SampleFormat {
	return codec.SampleFormat{Encoding: codec.SampleEncodingPCM, BitsPerSample: bits, ValidBitsPerSample: validBits}
}

func floatFormat(bits int) codec.SampleFormat {
	return codec.SampleFormat{Encoding: codec.SampleEncodingIEEEFloat, BitsPerSample: bits, ValidBitsPerSample: bits}
}

func littleEndian32(value uint32) []byte {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	return raw
}

func littleEndian64(value uint64) []byte {
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint64(raw, value)
	return raw
}

func float32Packet(values ...float32) []byte {
	raw := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(value))
	}
	return raw
}

func float64PaddedPacket() []byte {
	raw := make([]byte, 50)
	copy(raw[0:16], float64Packet(0.5, -0.25))
	copy(raw[24:40], float64Packet(-1, 0.75))
	for _, index := range []int{16, 17, 18, 19, 20, 21, 22, 23, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49} {
		raw[index] = 0xd3
	}
	return raw
}

func float64Packet(values ...float64) []byte {
	raw := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(raw[index*8:], math.Float64bits(value))
	}
	return raw
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
