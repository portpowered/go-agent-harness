package codec_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestDecodeSampleValuePreservesSupportedLiteralScaling(t *testing.T) {
	tests := []struct {
		name      string
		format    codec.SampleFormat
		raw       []byte
		want      float64
		wantSign  bool
		checkSign bool
	}{
		{name: "pcm8 zero", format: pcmFormat(8, 8), raw: []byte{0x00}, want: -1},
		{name: "pcm8 midpoint", format: pcmFormat(8, 8), raw: []byte{0x80}, want: 0},
		{name: "pcm8 maximum", format: pcmFormat(8, 8), raw: []byte{0xff}, want: 127.0 / 128},
		{name: "pcm16 minimum", format: pcmFormat(16, 16), raw: []byte{0x00, 0x80}, want: -1},
		{name: "pcm16 minus one", format: pcmFormat(16, 16), raw: []byte{0xff, 0xff}, want: -1.0 / 32768},
		{name: "pcm16 maximum", format: pcmFormat(16, 16), raw: []byte{0xff, 0x7f}, want: 32767.0 / 32768},
		{name: "pcm24 sign extension minimum", format: pcmFormat(24, 24), raw: []byte{0x00, 0x00, 0x80}, want: -1},
		{name: "pcm24 minus one", format: pcmFormat(24, 24), raw: []byte{0xff, 0xff, 0xff}, want: -1.0 / 8388608},
		{name: "pcm24 maximum", format: pcmFormat(24, 24), raw: []byte{0xff, 0xff, 0x7f}, want: 8388607.0 / 8388608},
		{name: "pcm32 minimum", format: pcmFormat(32, 32), raw: []byte{0x00, 0x00, 0x00, 0x80}, want: -1},
		{name: "pcm32 maximum", format: pcmFormat(32, 32), raw: []byte{0xff, 0xff, 0xff, 0x7f}, want: 2147483647.0 / 2147483648},
		{name: "pcm64 minimum", format: pcmFormat(64, 64), raw: littleEndian64(0x8000000000000000), want: -1},
		{name: "pcm64 maximum rounds to one", format: pcmFormat(64, 64), raw: littleEndian64(math.MaxInt64), want: 1},
		{name: "reduced valid bits retain container scaling", format: pcmFormat(16, 12), raw: []byte{0x01, 0x40}, want: 16385.0 / 32768},
		{name: "float32 positive zero", format: floatFormat(32), raw: littleEndian32(math.Float32bits(0)), want: 0},
		{name: "float32 negative zero", format: floatFormat(32), raw: littleEndian32(math.Float32bits(float32(math.Copysign(0, -1)))), want: 0, wantSign: true, checkSign: true},
		{name: "float32 half", format: floatFormat(32), raw: littleEndian32(math.Float32bits(0.5)), want: 0.5},
		{name: "float32 outside normalized range", format: floatFormat(32), raw: littleEndian32(math.Float32bits(2.5)), want: 2.5},
		{name: "float64 negative half", format: floatFormat(64), raw: littleEndian64(math.Float64bits(-0.5)), want: -0.5},
		{name: "float64 outside normalized range", format: floatFormat(64), raw: littleEndian64(math.Float64bits(-2.5)), want: -2.5},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := codec.DecodeSampleValue(testCase.raw, testCase.format)
			if err != nil {
				t.Fatalf("DecodeSampleValue() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("DecodeSampleValue() = %.17g, want %.17g", got, testCase.want)
			}
			if testCase.checkSign && math.Signbit(got) != testCase.wantSign {
				t.Fatalf("DecodeSampleValue() signbit = %v, want %v", math.Signbit(got), testCase.wantSign)
			}
		})
	}
}

func TestDecodeSampleValueRejectsShortInputWithoutPanic(t *testing.T) {
	formats := []codec.SampleFormat{
		pcmFormat(8, 8),
		pcmFormat(16, 16),
		pcmFormat(24, 24),
		pcmFormat(32, 32),
		pcmFormat(64, 64),
		floatFormat(32),
		floatFormat(64),
	}
	for _, format := range formats {
		width, err := format.ByteWidth()
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("short %v-byte input panicked: %v", width, recovered)
				}
			}()
			_, err := codec.DecodeSampleValue(make([]byte, width-1), format)
			if !errors.Is(err, codec.ErrSampleInputTooShort) {
				t.Fatalf("short %v-byte input error = %v, want ErrSampleInputTooShort", width, err)
			}
		}()
	}
}

func TestDecodeSampleValueRejectsInvalidMetadataAndEncodings(t *testing.T) {
	tests := []struct {
		name   string
		format codec.SampleFormat
		want   error
	}{
		{name: "missing encoding", format: codec.SampleFormat{BitsPerSample: 16, ValidBitsPerSample: 16}, want: codec.ErrUnsupportedSampleEncoding},
		{name: "non-byte-aligned container", format: codec.SampleFormat{Encoding: codec.SampleEncodingPCM, BitsPerSample: 7, ValidBitsPerSample: 7}, want: codec.ErrInvalidSampleFormat},
		{name: "zero valid bits", format: pcmFormat(16, 0), want: codec.ErrInvalidSampleFormat},
		{name: "valid bits exceed container", format: pcmFormat(16, 17), want: codec.ErrInvalidSampleFormat},
		{name: "unsupported PCM width", format: pcmFormat(40, 40), want: codec.ErrUnsupportedSampleWidth},
		{name: "unsupported float width", format: floatFormat(16), want: codec.ErrUnsupportedSampleWidth},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := codec.DecodeSampleValue(make([]byte, 8), testCase.format)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestDecodeSampleValueRejectsNonFiniteFloat(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  []byte
	}{
		{name: "float32 NaN", raw: littleEndian32(0x7fc00001)},
		{name: "float32 positive infinity", raw: littleEndian32(0x7f800000)},
		{name: "float64 negative infinity", raw: littleEndian64(0xfff0000000000000)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := codec.DecodeSampleValue(testCase.raw, floatFormat(len(testCase.raw)*8))
			if !errors.Is(err, codec.ErrNonFiniteSample) {
				t.Fatalf("error = %v, want ErrNonFiniteSample", err)
			}
		})
	}
}

func TestDecodeSampleValueDoesNotMutateInput(t *testing.T) {
	raw := []byte{0xff, 0x7f, 0x4d}
	want := append([]byte(nil), raw...)
	if _, err := codec.DecodeSampleValue(raw, pcmFormat(16, 12)); err != nil {
		t.Fatalf("successful decode returned error: %v", err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("input changed after successful decode: got %v, want %v", raw, want)
	}
	if _, err := codec.DecodeSampleValue(raw[:1], pcmFormat(16, 16)); !errors.Is(err, codec.ErrSampleInputTooShort) {
		t.Fatalf("short decode error = %v, want ErrSampleInputTooShort", err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("input changed after rejected decode: got %v, want %v", raw, want)
	}
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

func TestPacketEnergyMeasuresLiteralInterleavedSamplesAndSkipsPadding(t *testing.T) {
	data := []byte{
		0x00, 0x40, 0x00, 0xc0, 0xaa, 0xbb,
		0x00, 0x00, 0x00, 0x80, 0xcc, 0xdd,
	}
	wantInput := append([]byte(nil), data...)
	got, err := codec.PacketEnergy(data, 2, 2, 6, pcmFormat(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1.5 {
		t.Fatalf("PacketEnergy() = %.17g, want 1.5", got)
	}
	if !bytes.Equal(data, wantInput) {
		t.Fatalf("packet changed after energy measurement: got %v, want %v", data, wantInput)
	}
}

func TestPacketEnergyMeasuresIndependentMonoAndFloatLiterals(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		frames   int
		channels int
		stride   int
		format   codec.SampleFormat
		want     float64
	}{
		{
			name:     "PCM8 mono",
			data:     []byte{0x00, 0x80, 0xff},
			frames:   3,
			channels: 1,
			stride:   1,
			format:   pcmFormat(8, 8),
			want:     1 + (127.0/128)*(127.0/128),
		},
		{
			name:     "float32 stereo",
			data:     float32Packet(0.5, -0.5, 2, 0),
			frames:   2,
			channels: 2,
			stride:   8,
			format:   floatFormat(32),
			want:     4.5,
		},
		{
			name:     "float64 mono",
			data:     float64Packet(1.5, -2),
			frames:   2,
			channels: 1,
			stride:   8,
			format:   floatFormat(64),
			want:     6.25,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := codec.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, testCase.format)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("PacketEnergy() = %.17g, want %.17g", got, testCase.want)
			}
		})
	}
}

func TestPacketEnergyZeroFramesDoesNotReadData(t *testing.T) {
	got, err := codec.PacketEnergy(nil, 0, 2, 6, pcmFormat(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("zero-frame energy = %g, want zero", got)
	}
}

func TestPacketEnergyRejectsDimensionsBeforeTraversal(t *testing.T) {
	maximum := int(^uint(0) >> 1)
	tests := []struct {
		name                     string
		data                     []byte
		frames, channels, stride int
		want                     error
	}{
		{name: "negative frames", data: []byte{0}, frames: -1, channels: 1, stride: 1, want: codec.ErrInvalidPacketDimensions},
		{name: "zero channels", data: []byte{0}, frames: 1, channels: 0, stride: 1, want: codec.ErrInvalidPacketDimensions},
		{name: "zero stride", data: []byte{0}, frames: 1, channels: 1, stride: 0, want: codec.ErrInvalidPacketDimensions},
		{name: "channel bytes exceed stride", data: []byte{0, 0}, frames: 1, channels: 2, stride: 2, want: codec.ErrInvalidPacketDimensions},
		{name: "frame product overflows int", data: []byte{0}, frames: maximum, channels: 1, stride: 2, want: codec.ErrInvalidPacketDimensions},
		{name: "channel product overflows int", data: []byte{0}, frames: 1, channels: maximum, stride: maximum, want: codec.ErrInvalidPacketDimensions},
		{name: "short final frame", data: make([]byte, 5), frames: 2, channels: 1, stride: 3, want: codec.ErrPacketInputTooShort},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := codec.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, pcmFormat(16, 16))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestPacketEnergyRejectsNonFiniteAndOverflowingResults(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "NaN sample", data: float64Packet(math.NaN()), want: codec.ErrNonFiniteSample},
		{name: "infinite sample", data: float64Packet(math.Inf(1)), want: codec.ErrNonFiniteSample},
		{name: "sample square overflow", data: float64Packet(math.MaxFloat64), want: codec.ErrPacketEnergyOverflow},
		{name: "sum overflow", data: float64Packet(math.MaxFloat64*0.9, math.MaxFloat64*0.9), want: codec.ErrPacketEnergyOverflow},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			before := append([]byte(nil), testCase.data...)
			_, err := codec.PacketEnergy(testCase.data, len(testCase.data)/8, 1, 8, floatFormat(64))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if !bytes.Equal(testCase.data, before) {
				t.Fatalf("input changed after rejected energy measurement")
			}
		})
	}
}

func float32Packet(values ...float32) []byte {
	data := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(data[index*4:], math.Float32bits(value))
	}
	return data
}

func float64Packet(values ...float64) []byte {
	data := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(data[index*8:], math.Float64bits(value))
	}
	return data
}

func TestPacketEnergyC42Coverage(t *testing.T) {
	runPacketEnergyC42Coverage(t)
}

// TestWindowsPortablePlaybackBurstPreservesFIFOCaptureEnergyCodecCoverage is
// selected by the existing Windows portable software-test regex. It keeps the
// codec controls in this package so the Windows job exercises the same
// canonical implementation as the normal package run.
func TestWindowsPortablePlaybackBurstPreservesFIFOCaptureEnergyCodecCoverage(t *testing.T) {
	runPacketEnergyC42Coverage(t)
}

func runPacketEnergyC42Coverage(t *testing.T) {
	t.Helper()
	t.Run("pcm16-padded", runC42PCM16Padded)
	t.Run("pcm24-padded", runC42PCM24Padded)
	t.Run("pcm32-padded", runC42PCM32Padded)
	t.Run("float64-padded", runC42Float64Padded)
	t.Run("zero-frames", runC42ZeroFrames)
	t.Run("accumulation-overflow", runC42AccumulationOverflow)
	t.Run("later-nonfinite", runC42LaterNonFinite)
	t.Run("truncated", runC42Truncated)
	t.Run("malformed", runC42Malformed)
	t.Run("allocations", runC42Allocations)
}

func runC42PCM16Padded(t *testing.T) {
	format := pcmFormat(16, 12)
	data := []byte{
		0x01, 0x40, 0x00, 0xc0, 0x00, 0x20, 0xaa, 0xbb,
		0x00, 0x00, 0x00, 0x80, 0x00, 0xe0, 0xcc, 0xdd,
		0x13, 0x37,
	}
	wantSamples := []float64{16385.0 / 32768.0, -0.5, 0.25, 0, -1, -0.25}
	wantEnergy := 1744863233.0 / 1073741824.0
	assertC42PacketSamples(t, data, 2, 3, 8, format, wantSamples)
	assertC42PacketMutations(t, data, 2, 3, 8, format, []int{6, 7, 14, 15, 16, 17}, 0, 0x01, wantEnergy)
}

func runC42PCM24Padded(t *testing.T) {
	format := pcmFormat(24, 20)
	data := []byte{
		0x00, 0x00, 0x40, 0x00, 0x00, 0xc0, 0xde, 0xad,
		0xff, 0xff, 0x7f, 0x00, 0x00, 0x00, 0xbe, 0xef,
		0x5a,
	}
	assertC42PacketSamples(t, data, 2, 2, 8, format, []float64{0.5, -0.5, 8388607.0 / 8388608.0, 0})
	assertC42PacketMutations(t, data, 2, 2, 8, format, []int{6, 7, 14, 15, 16}, 0, 0x01, 0.5+(8388607.0/8388608.0)*(8388607.0/8388608.0))
}

func runC42PCM32Padded(t *testing.T) {
	format := pcmFormat(32, 24)
	data := []byte{
		0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x00, 0xe0, 0xaa, 0xbb, 0xcc, 0xdd,
		0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00, 0x60, 0x11, 0x22, 0x33, 0x44,
		0x55, 0x66,
	}
	assertC42PacketSamples(t, data, 2, 2, 12, format, []float64{0.5, -0.25, -1, 0.75})
	assertC42PacketMutations(t, data, 2, 2, 12, format, []int{8, 9, 10, 11, 20, 21, 22, 23, 24, 25}, 0, 0x01, 1.875)
}

func runC42Float64Padded(t *testing.T) {
	data := make([]byte, 50)
	copy(data[0:16], float64Packet(0.5, -0.25))
	copy(data[24:40], float64Packet(-1, 0.75))
	for _, index := range []int{16, 17, 18, 19, 20, 21, 22, 23, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49} {
		data[index] = 0xd3
	}
	format := floatFormat(64)
	assertC42PacketSamples(t, data, 2, 2, 24, format, []float64{0.5, -0.25, -1, 0.75})
	assertC42PacketMutations(t, data, 2, 2, 24, format, []int{16, 17, 18, 19, 20, 21, 22, 23, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49}, 7, 0x01, 1.875)
}

func assertC42PacketMutations(t *testing.T, data []byte, frames, channels, stride int, format codec.SampleFormat, padding []int, measuredIndex int, measuredXor byte, wantEnergy float64) {
	t.Helper()
	before := append([]byte(nil), data...)
	energy, err := codec.PacketEnergy(data, frames, channels, stride, format)
	if err != nil {
		t.Fatalf("padded energy error = %v", err)
	}
	if energy != wantEnergy {
		t.Fatalf("padded energy = %.17g, want %.17g", energy, wantEnergy)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("padded packet changed after successful measurement")
	}
	paddingOnly := append([]byte(nil), data...)
	for _, index := range padding {
		paddingOnly[index] ^= 0xff
	}
	paddingEnergy, err := codec.PacketEnergy(paddingOnly, frames, channels, stride, format)
	if err != nil || paddingEnergy != wantEnergy {
		t.Fatalf("padding/trailing mutation changed energy: got %.17g, err %v, want %.17g", paddingEnergy, err, wantEnergy)
	}
	measuredMutation := append([]byte(nil), data...)
	measuredMutation[measuredIndex] ^= measuredXor
	changedEnergy, err := codec.PacketEnergy(measuredMutation, frames, channels, stride, format)
	if err != nil {
		t.Fatalf("measured-sample mutation error = %v", err)
	}
	if changedEnergy == wantEnergy {
		t.Fatal("measured-sample mutation did not change literal energy")
	}
}

func runC42ZeroFrames(t *testing.T) {
	data := littleEndian64(math.Float64bits(math.NaN()))
	before := append([]byte(nil), data...)
	energy, err := codec.PacketEnergy(data, 0, 1, 8, floatFormat(64))
	if err != nil || energy != 0 {
		t.Fatalf("zero-frame nonfinite tail = %.17g, %v, want exact zero", energy, err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("zero-frame packet changed while trailing sample was ignored")
	}
	malformedFormat := floatFormat(64)
	malformedFormat.ValidBitsPerSample = 0
	if _, err := codec.PacketEnergy(nil, 0, 1, 8, malformedFormat); !errors.Is(err, codec.ErrInvalidSampleFormat) {
		t.Fatalf("zero-frame malformed format error = %v, want codec.ErrInvalidSampleFormat", err)
	}
	if _, err := codec.PacketEnergy(nil, 0, 0, 8, floatFormat(64)); !errors.Is(err, codec.ErrInvalidPacketDimensions) {
		t.Fatalf("zero-frame malformed dimensions error = %v, want codec.ErrInvalidPacketDimensions", err)
	}
}

func runC42AccumulationOverflow(t *testing.T) {
	const sample float64 = 0x1p+511
	const wantSquare float64 = 0x1p+1022

	t.Run("single square is finite", func(t *testing.T) {
		data := float64Packet(sample)
		before := append([]byte(nil), data...)
		energy, err := codec.PacketEnergy(data, 1, 1, 8, floatFormat(64))
		if err != nil {
			t.Fatalf("single-square energy error = %v", err)
		}
		if energy != wantSquare {
			t.Fatalf("single-square energy = %.17g, want literal 0x1p+1022 (%.17g)", energy, wantSquare)
		}
		if !bytes.Equal(data, before) {
			t.Fatal("single-square packet changed after successful measurement")
		}
	})

	data := float64Packet(sample, sample, sample, sample)
	for _, testCase := range []struct {
		name             string
		frames, channels int
		stride           int
	}{
		{name: "four channels in one frame", frames: 1, channels: 4, stride: 32},
		{name: "four mono frames", frames: 4, channels: 1, stride: 8},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			before := append([]byte(nil), data...)
			_, err := codec.PacketEnergy(data, testCase.frames, testCase.channels, testCase.stride, floatFormat(64))
			if !errors.Is(err, codec.ErrPacketEnergyOverflow) {
				t.Fatalf("error = %v, want codec.ErrPacketEnergyOverflow", err)
			}
			if !bytes.Equal(data, before) {
				t.Fatal("true accumulation-overflow packet changed after rejection")
			}
		})
	}
}

func runC42LaterNonFinite(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		data             []byte
		frames, channels int
		stride           int
	}{
		{name: "later channel NaN", data: float64Packet(0.5, -0.25, math.NaN()), frames: 1, channels: 3, stride: 24},
		{name: "later frame negative infinity", data: float64Packet(0.5, math.Inf(-1)), frames: 2, channels: 1, stride: 8},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			before := append([]byte(nil), testCase.data...)
			first, firstErr := codec.DecodeSampleValue(testCase.data[:8], floatFormat(64))
			if firstErr != nil || first != 0.5 {
				t.Fatalf("adjacent finite literal = %.17g, %v, want 0.5", first, firstErr)
			}
			energy, err := codec.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, floatFormat(64))
			if !errors.Is(err, codec.ErrNonFiniteSample) || energy != 0 {
				t.Fatalf("energy/error = %.17g, %v, want zero and codec.ErrNonFiniteSample", energy, err)
			}
			if !bytes.Equal(testCase.data, before) {
				t.Fatal("nonfinite packet changed after rejection")
			}
		})
	}
}

func runC42Truncated(t *testing.T) {
	backing := make([]byte, 24)
	for index := range backing {
		backing[index] = 0xa5
	}
	before := append([]byte(nil), backing...)
	if _, err := codec.PacketEnergy(backing[:15], 2, 3, 8, pcmFormat(16, 12)); !errors.Is(err, codec.ErrPacketInputTooShort) {
		t.Fatalf("one-byte-short padded final frame error = %v, want codec.ErrPacketInputTooShort", err)
	}
	if !bytes.Equal(backing, before) {
		t.Fatal("truncated packet backing storage changed after rejection")
	}
}

func runC42Malformed(t *testing.T) {
	if _, err := codec.PacketEnergy([]byte{0x00}, 1, 3, 5, pcmFormat(16, 16)); !errors.Is(err, codec.ErrInvalidPacketDimensions) {
		t.Fatalf("malformed channel/stride error = %v, want codec.ErrInvalidPacketDimensions", err)
	}
	maximum := int(^uint(0) >> 1)
	if _, err := codec.PacketEnergy([]byte{0x00}, maximum, 1, 2, pcmFormat(16, 16)); !errors.Is(err, codec.ErrInvalidPacketDimensions) {
		t.Fatalf("unrepresentable frame extent error = %v, want codec.ErrInvalidPacketDimensions", err)
	}
}

func runC42Allocations(t *testing.T) {
	packets := []struct {
		name             string
		data             []byte
		frames, channels int
		stride           int
		format           codec.SampleFormat
		want             float64
	}{
		{name: "small preallocated", data: []byte{0x00, 0x80, 0xff}, frames: 3, channels: 1, stride: 1, format: pcmFormat(8, 8), want: 1 + (127.0/128)*(127.0/128)},
		{name: "large preallocated", data: make([]byte, 64*8), frames: 64, channels: 1, stride: 8, format: floatFormat(64), want: 0},
	}
	for _, testCase := range packets {
		t.Run(testCase.name, func(t *testing.T) {
			allocations := testing.AllocsPerRun(100, func() {
				got, err := codec.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, testCase.format)
				if err != nil || got != testCase.want {
					t.Fatalf("codec.PacketEnergy() = %.17g, %v, want %.17g", got, err, testCase.want)
				}
			})
			if allocations != 0 {
				t.Fatalf("successful PacketEnergy path allocated %.2f times", allocations)
			}
		})
	}
}

func assertC42PacketSamples(t *testing.T, data []byte, frames, channels, stride int, format codec.SampleFormat, want []float64) {
	t.Helper()
	width, err := format.ByteWidth()
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	for frame := 0; frame < frames; frame++ {
		for channel := 0; channel < channels; channel++ {
			value, err := codec.DecodeSampleValue(data[frame*stride+channel*width:], format)
			if err != nil {
				t.Fatalf("literal sample frame=%d channel=%d error = %v", frame, channel, err)
			}
			if value != want[index] {
				t.Fatalf("literal sample frame=%d channel=%d = %.17g, want %.17g", frame, channel, value, want[index])
			}
			index++
		}
	}
}
