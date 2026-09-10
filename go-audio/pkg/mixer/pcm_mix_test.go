package mixer

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestMixPCM16SamplesSumsBeforeOneFinalClip(t *testing.T) {
	tests := []struct {
		name    string
		sources [][]int16
		length  int
		want    []int16
	}{
		{name: "silence", sources: nil, length: 4, want: []int16{0, 0, 0, 0}},
		{name: "unequal tails", sources: [][]int16{{1, 2, 3}, {4}}, length: 4, want: []int16{5, 2, 3, 0}},
		{name: "positive cancellation before clip", sources: [][]int16{{32767}, {32767}, {-32768}}, length: 1, want: []int16{32766}},
		{name: "negative cancellation before clip", sources: [][]int16{{-32768}, {-32768}, {32767}}, length: 1, want: []int16{-32768}},
		{name: "empty output", sources: [][]int16{{}}, length: 0, want: []int16{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MixPCM16Samples(test.sources, test.length)
			if err != nil {
				t.Fatalf("mix: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mixed samples = %v, want %v", got, test.want)
			}
		})
	}

	got, err := MixPCM16Samples([][]int16{{32767}, {32767}, {-32768}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	intermediate := int32(0)
	for _, sample := range []int16{32767, 32767, -32768} {
		intermediate += int32(sample)
		if intermediate > pcm16MaxSample {
			intermediate = pcm16MaxSample
		} else if intermediate < pcm16MinSample {
			intermediate = pcm16MinSample
		}
	}
	if got[0] == int16(intermediate) {
		t.Fatalf("mix matches intermediate-clipping control: got %d", got[0])
	}
}

func TestMixPCM16SamplesIsPermutationInvariantAndDoesNotRetainInputs(t *testing.T) {
	first := []int16{30000, -1000}
	second := []int16{10000, 2000}
	third := []int16{-32768, 10}
	want := []int16{7232, 1010}
	permutations := [][][]int16{
		{first, second, third}, {first, third, second},
		{second, first, third}, {second, third, first},
		{third, first, second}, {third, second, first},
	}
	for index, sources := range permutations {
		got, err := MixPCM16Samples(sources, 2)
		if err != nil {
			t.Fatalf("permutation %d: %v", index, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d = %v, want %v", index, got, want)
		}
	}

	before := append([]int16(nil), first...)
	got, err := MixPCM16Samples([][]int16{first}, len(first))
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 7
	if !reflect.DeepEqual(first, []int16{7, -1000}) {
		t.Fatalf("source was unexpectedly changed: %v", first)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("returned samples changed after input mutation: got %v, want %v", got, before)
	}
}

func TestMixPCM16SamplesUsesSafeAccumulationBeyondInt32Threshold(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		sample int16
		want   int16
	}{
		{name: "negative int32 boundary", count: 65536, sample: -32768, want: -32768},
		{name: "negative first overflow", count: 65537, sample: -32768, want: -32768},
		{name: "positive int32 boundary", count: 65538, sample: 32767, want: 32767},
		{name: "positive first overflow", count: 65539, sample: 32767, want: 32767},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := repeatedPCM16Sources(test.count, test.sample)
			got, err := MixPCM16Samples(sources, 1)
			if err != nil {
				t.Fatalf("mix: %v", err)
			}
			if !reflect.DeepEqual(got, []int16{test.want}) {
				t.Fatalf("mixed samples = %v, want %v", got, []int16{test.want})
			}
		})
	}
}

func TestMixPCM16SamplesRejectsUnsupportedBounds(t *testing.T) {
	if _, err := MixPCM16Samples([][]int16{{1, 2}}, 1); !errors.Is(err, ErrPCM16MixSourceTooLong) {
		t.Fatalf("long source error = %v, want ErrPCM16MixSourceTooLong", err)
	}
	if _, err := MixPCM16Samples(nil, -1); !errors.Is(err, ErrPCM16MixInvalidLength) {
		t.Fatalf("negative length error = %v, want ErrPCM16MixInvalidLength", err)
	}
	if _, err := MixPCM16Samples(make([][]int16, MaxPCM16MixSources+1), 1); !errors.Is(err, ErrPCM16MixSourceLimit) {
		t.Fatalf("source limit error = %v, want ErrPCM16MixSourceLimit", err)
	}
}

func TestValidatePCM16MixBoundsDoesNotAllocateOrConsume(t *testing.T) {
	if err := ValidatePCM16MixBounds(MaxPCM16MixSources, MaxPCM16MixSamples); err != nil {
		t.Fatalf("maximum supported bounds rejected: %v", err)
	}
	if !errors.Is(ValidatePCM16MixBounds(MaxPCM16MixSources+1, 1), ErrPCM16MixSourceLimit) {
		t.Fatal("source overflow was not rejected")
	}
	if !errors.Is(ValidatePCM16MixBounds(1, MaxPCM16MixSamples+1), ErrPCM16MixInvalidLength) {
		t.Fatal("output overflow was not rejected")
	}
}

func TestMixPCM16BytesUsesIndependentLiteralPCM16Oracles(t *testing.T) {
	tests := []struct {
		name    string
		sources [][]byte
		length  int
		want    []byte
	}{
		{
			name: "signed extrema and one final clip",
			sources: [][]byte{
				{0xff, 0x7f, 0xff, 0x7f, 0x00, 0x00},
				{0xff, 0x7f, 0x00, 0x80, 0x01, 0x00},
				{0x00, 0x80, 0x04, 0x00, 0x00, 0x80},
			},
			length: 3,
			want:   []byte{0xfe, 0x7f, 0x03, 0x00, 0x01, 0x80},
		},
		{
			name:    "short tail and empty source",
			sources: [][]byte{{0x01, 0x00, 0x02, 0x00}, {}, nil},
			length:  4,
			want:    []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			name:    "empty output",
			sources: [][]byte{{}, nil},
			length:  0,
			want:    []byte{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := MixPCM16Bytes(test.sources, test.length)
			if err != nil {
				t.Fatalf("mix bytes: %v", err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("mixed bytes = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMixPCM16BytesIsPermutationInvariantAndDoesNotAliasInputs(t *testing.T) {
	first := []byte{0x30, 0x75, 0x18, 0xfc}
	second := []byte{0x10, 0x27, 0xd0, 0x07}
	third := []byte{0x00, 0x80, 0x0a, 0x00}
	want := []byte{0x40, 0x1c, 0xf2, 0x03}
	permutations := [][][]byte{
		{first, second, third}, {first, third, second},
		{second, first, third}, {second, third, first},
		{third, first, second}, {third, second, first},
	}
	for index, sources := range permutations {
		got, err := MixPCM16Bytes(sources, 2)
		if err != nil {
			t.Fatalf("permutation %d: %v", index, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("permutation %d = %v, want %v", index, got, want)
		}
	}

	before := append([]byte(nil), first...)
	got, err := MixPCM16Bytes([][]byte{first}, 2)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 0x07
	if !bytes.Equal(first, []byte{0x07, 0x75, 0x18, 0xfc}) {
		t.Fatalf("source was unexpectedly changed: %v", first)
	}
	if !bytes.Equal(got, before) {
		t.Fatalf("returned bytes changed or aliased input: got %v, want %v", got, before)
	}
}

func TestMixPCM16BytesRejectsEveryInvalidDimensionBeforeDecoding(t *testing.T) {
	if _, err := MixPCM16Bytes(nil, -1); !errors.Is(err, ErrPCM16MixInvalidLength) {
		t.Fatalf("negative output error = %v, want ErrPCM16MixInvalidLength", err)
	}
	if _, err := MixPCM16Bytes(nil, MaxPCM16MixSamples+1); !errors.Is(err, ErrPCM16MixInvalidLength) {
		t.Fatalf("oversized output error = %v, want ErrPCM16MixInvalidLength", err)
	}
	if _, err := MixPCM16Bytes([][]byte{{0x01}}, 1); !errors.Is(err, codec.ErrPCM16OddLength) {
		t.Fatalf("odd source error = %v, want codec.ErrPCM16OddLength", err)
	}
	if _, err := MixPCM16Bytes([][]byte{{0, 0, 0, 0}}, 1); !errors.Is(err, ErrPCM16MixSourceTooLong) {
		t.Fatalf("overlong source error = %v, want ErrPCM16MixSourceTooLong", err)
	}
	if _, err := MixPCM16Bytes(make([][]byte, MaxPCM16MixSources+1), 1); !errors.Is(err, ErrPCM16MixSourceLimit) {
		t.Fatalf("source limit error = %v, want ErrPCM16MixSourceLimit", err)
	}
}

func TestCombineUsesSharedSafePCMOperation(t *testing.T) {
	frames := make([]audio.PCMFrame, 65539)
	for index := range frames {
		frames[index].Samples = []int16{32767}
	}
	got, err := combine(Format{SampleRate: 1000, Channels: 1}, 1, frames)
	if err != nil {
		t.Fatalf("combine: %v", err)
	}
	if !reflect.DeepEqual(got.Samples, []int16{32767}) {
		t.Fatalf("combined samples = %v, want final-clipped positive sum", got.Samples)
	}
}

func repeatedPCM16Sources(count int, sample int16) [][]int16 {
	sources := make([][]int16, count)
	for index := range sources {
		sources[index] = []int16{sample}
	}
	return sources
}
