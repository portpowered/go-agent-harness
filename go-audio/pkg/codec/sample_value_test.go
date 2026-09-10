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
