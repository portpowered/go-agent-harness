package audio

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestResamplePCM16PreservesEstablishedRatesAndArbitraryRates(t *testing.T) {
	tests := []struct {
		name       string
		input      []int16
		inputRate  int
		outputRate int
		want       []int16
	}{
		{name: "wavio supported", input: []int16{0, 3000}, inputRate: 16000, outputRate: 24000, want: []int16{0, 2000, 3000}},
		{name: "arbitrary fallback", input: []int16{0, 1000}, inputRate: 1000, outputRate: 1500, want: []int16{0, 667, 1000}},
		{name: "identity copy", input: []int16{1, -2}, inputRate: 1000, outputRate: 1000, want: []int16{1, -2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := append([]int16(nil), tt.input...)
			got, err := ResamplePCM16(input, tt.inputRate, tt.outputRate)
			if err != nil {
				t.Fatalf("ResamplePCM16() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ResamplePCM16() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(input, tt.input) {
				t.Fatalf("ResamplePCM16() mutated input: %v", input)
			}
		})
	}
}

func TestResamplePCM16RejectsInvalidRates(t *testing.T) {
	if _, err := ResamplePCM16([]int16{1}, 0, 16000); !errors.Is(err, ErrInvalidPCM16ConversionRate) {
		t.Fatalf("invalid input rate error = %v", err)
	}
	if _, err := ResamplePCM16([]int16{1}, 16000, -1); !errors.Is(err, ErrInvalidPCM16ConversionRate) {
		t.Fatalf("invalid output rate error = %v", err)
	}
	if got, err := ResamplePCM16(nil, 1000, 2000); err != nil || len(got) != 0 {
		t.Fatalf("empty arbitrary conversion = %v, %v", got, err)
	}
}

func TestConvertPCM16BytesChannelLayoutsAndRates(t *testing.T) {
	pcm := codec.EncodePCM16([]int16{0, 1000, 2000, 3000})
	got, err := ConvertPCM16Bytes(pcm, 2, 1000, 1, 1000)
	if err != nil {
		t.Fatalf("ConvertPCM16Bytes() error = %v", err)
	}
	samples, err := codec.DecodePCM16(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int16{500, 2500}; !reflect.DeepEqual(samples, want) {
		t.Fatalf("downmix samples = %v, want %v", samples, want)
	}

	mono := codec.EncodePCM16([]int16{0, 3000})
	got, err = ConvertPCM16Bytes(mono, 1, 1000, 2, 1500)
	if err != nil {
		t.Fatalf("ConvertPCM16Bytes() expansion error = %v", err)
	}
	samples, err = codec.DecodePCM16(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int16{0, 0, 2000, 2000, 3000, 3000}; !reflect.DeepEqual(samples, want) {
		t.Fatalf("expanded samples = %v, want %v", samples, want)
	}
}

func TestConvertPCM16BytesIdentityAndMalformedInputs(t *testing.T) {
	pcm := codec.EncodePCM16([]int16{1, -2})
	got, err := ConvertPCM16Bytes(pcm, 1, 16000, 1, 16000)
	if err != nil || !reflect.DeepEqual(got, pcm) {
		t.Fatalf("identity conversion = %v, %v; want copy of %v", got, err, pcm)
	}
	got[0]++
	if got[0] == pcm[0] {
		t.Fatal("identity conversion aliased input")
	}
	if _, err := ConvertPCM16Bytes([]byte{0}, 1, 16000, 1, 16000); !errors.Is(err, ErrPCM16ConversionAlignment) {
		t.Fatalf("odd PCM payload error = %v", err)
	}
	if _, err := ConvertPCM16Bytes(pcm, 0, 16000, 1, 16000); !errors.Is(err, ErrInvalidPCM16ConversionChannels) {
		t.Fatalf("invalid source channels error = %v", err)
	}
	if _, err := ConvertPCM16Bytes(pcm, 1, 16000, 0, 16000); !errors.Is(err, ErrInvalidPCM16ConversionChannels) {
		t.Fatalf("invalid target channels error = %v", err)
	}
	if _, err := ConvertPCM16Bytes(pcm, 1, 0, 1, 16000); !errors.Is(err, ErrInvalidPCM16ConversionRate) {
		t.Fatalf("invalid source rate error = %v", err)
	}
	stereo := codec.EncodePCM16([]int16{1, 2})
	converted, err := ConvertPCM16Bytes(stereo, 2, 16000, 3, 16000)
	if err != nil {
		t.Fatalf("extra-channel conversion error = %v", err)
	}
	if samples, decodeErr := codec.DecodePCM16(converted); decodeErr != nil || !reflect.DeepEqual(samples, []int16{1, 2, 2}) {
		t.Fatalf("extra-channel conversion = %v, %v; want [1 2 2]", samples, decodeErr)
	}
}

func TestPCM16RMSEnergyExactAmplitudeAndEmptyInput(t *testing.T) {
	if got := PCM16RMSEnergy(nil); got != 0 {
		t.Fatalf("PCM16RMSEnergy(nil) = %v, want 0", got)
	}
	if got := PCM16RMSEnergy([]int16{3, 4}); math.Abs(got-3.5355339059327378) > 1e-12 {
		t.Fatalf("PCM16RMSEnergy([3 4]) = %.16f, want %.16f", got, 3.5355339059327378)
	}
	if got := PCM16RMSEnergy([]int16{-32768, 32767}); got <= 0 {
		t.Fatalf("PCM16RMSEnergy(full-scale pair) = %v, want positive", got)
	}
}

func TestPCM16DurationUsesInterleavedFrameShape(t *testing.T) {
	got, err := PCM16Duration(1600, 8000, 1)
	if err != nil {
		t.Fatalf("PCM16Duration() error = %v", err)
	}
	if got != 100*time.Millisecond {
		t.Fatalf("PCM16Duration() = %s, want 100ms", got)
	}

	got, err = PCM16Duration(3200, 8000, 2)
	if err != nil {
		t.Fatalf("stereo PCM16Duration() error = %v", err)
	}
	if got != 100*time.Millisecond {
		t.Fatalf("stereo PCM16Duration() = %s, want 100ms", got)
	}
}

func TestPCM16DurationRejectsMalformedOrUnrepresentableInput(t *testing.T) {
	for _, input := range [][3]int{
		{1, 8000, 1},
		{1600, 0, 1},
		{1600, 8000, 0},
		{-2, 8000, 1},
	} {
		if _, err := PCM16Duration(input[0], input[1], input[2]); !errors.Is(err, ErrInvalidPCM16Duration) {
			t.Fatalf("PCM16Duration(%v) error = %v", input, err)
		}
	}
	maxInt := int(^uint(0) >> 1)
	if _, err := PCM16Duration(maxInt, 1, 1); !errors.Is(err, ErrInvalidPCM16Duration) {
		t.Fatalf("overflow PCM16Duration() error = %v", err)
	}
}
