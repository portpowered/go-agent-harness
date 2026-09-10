package audio_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestPacketEnergyMeasuresLiteralInterleavedSamplesAndSkipsPadding(t *testing.T) {
	data := []byte{
		0x00, 0x40, 0x00, 0xc0, 0xaa, 0xbb,
		0x00, 0x00, 0x00, 0x80, 0xcc, 0xdd,
	}
	wantInput := append([]byte(nil), data...)
	got, err := audio.PacketEnergy(data, 2, 2, 6, pcmFormat(16, 16))
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
			got, err := audio.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, testCase.format)
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
	got, err := audio.PacketEnergy(nil, 0, 2, 6, pcmFormat(16, 16))
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
		{name: "negative frames", data: []byte{0}, frames: -1, channels: 1, stride: 1, want: audio.ErrInvalidPacketDimensions},
		{name: "zero channels", data: []byte{0}, frames: 1, channels: 0, stride: 1, want: audio.ErrInvalidPacketDimensions},
		{name: "zero stride", data: []byte{0}, frames: 1, channels: 1, stride: 0, want: audio.ErrInvalidPacketDimensions},
		{name: "channel bytes exceed stride", data: []byte{0, 0}, frames: 1, channels: 2, stride: 2, want: audio.ErrInvalidPacketDimensions},
		{name: "frame product overflows int", data: []byte{0}, frames: maximum, channels: 1, stride: 2, want: audio.ErrInvalidPacketDimensions},
		{name: "channel product overflows int", data: []byte{0}, frames: 1, channels: maximum, stride: maximum, want: audio.ErrInvalidPacketDimensions},
		{name: "short final frame", data: make([]byte, 5), frames: 2, channels: 1, stride: 3, want: audio.ErrPacketInputTooShort},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := audio.PacketEnergy(testCase.data, testCase.frames, testCase.channels, testCase.stride, pcmFormat(16, 16))
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
		{name: "sample square overflow", data: float64Packet(math.MaxFloat64), want: audio.ErrPacketEnergyOverflow},
		{name: "sum overflow", data: float64Packet(math.MaxFloat64*0.9, math.MaxFloat64*0.9), want: audio.ErrPacketEnergyOverflow},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			before := append([]byte(nil), testCase.data...)
			_, err := audio.PacketEnergy(testCase.data, len(testCase.data)/8, 1, 8, floatFormat(64))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if !bytes.Equal(testCase.data, before) {
				t.Fatalf("input changed after rejected energy measurement")
			}
		})
	}
}

func pcmFormat(bits, validBits int) codec.SampleFormat {
	return codec.SampleFormat{Encoding: codec.SampleEncodingPCM, BitsPerSample: bits, ValidBitsPerSample: validBits}
}

func floatFormat(bits int) codec.SampleFormat {
	return codec.SampleFormat{Encoding: codec.SampleEncodingIEEEFloat, BitsPerSample: bits, ValidBitsPerSample: bits}
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
