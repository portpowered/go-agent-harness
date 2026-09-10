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
