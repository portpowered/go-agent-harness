package wavio

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestStreamingResamplerIsChunkInvariantAcrossRateMatrix(t *testing.T) {
	rates := []int{Rate16kHz, Rate24kHz, Rate48kHz}
	chunks := []int{1, 2, 17, 479, 480, 481, 719, 720, 31, 509}
	input := make([]int16, 4097)
	for i := range input {
		input[i] = int16((i*7919)%60001 - 30000)
	}
	for _, inRate := range rates {
		for _, outRate := range rates {
			t.Run(fmt.Sprintf("%d_to_%d", inRate, outRate), func(t *testing.T) {
				whole := processStreaming(t, inRate, outRate, [][]int16{input})
				var segmented [][]int16
				for at, index := 0, 0; at < len(input); index++ {
					n := chunks[index%len(chunks)]
					if n > len(input)-at {
						n = len(input) - at
					}
					segmented = append(segmented, input[at:at+n])
					at += n
				}
				got := processStreaming(t, inRate, outRate, segmented)
				if !reflect.DeepEqual(got, whole) {
					t.Fatalf("segmentation changed output: got=%d whole=%d", len(got), len(whole))
				}
				wantCount := (len(input)*outRate + inRate - 1) / inRate
				if len(got) != wantCount {
					t.Fatalf("count=%d want=%d", len(got), wantCount)
				}
			})
		}
	}
}

func TestStreamingResamplerLongRunSampleCountAndReset(t *testing.T) {
	r, err := NewPCM16Resampler(Rate24kHz, Rate16kHz)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for i := 0; i < 10000; i++ {
		out, err := r.Process(make([]int16, 479), false)
		if err != nil {
			t.Fatal(err)
		}
		count += len(out)
	}
	out, err := r.Process(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	count += len(out)
	want := (10000*479*Rate16kHz + Rate24kHz - 1) / Rate24kHz
	if count != want {
		t.Fatalf("long-run count=%d want=%d", count, want)
	}
	if _, err := r.Process(nil, false); !errors.Is(err, ErrResamplerEnded) {
		t.Fatalf("post-end error=%v", err)
	}
	if err := r.Reset(Rate48kHz, Rate16kHz); err != nil {
		t.Fatal(err)
	}
	if r.DelaySamples() <= 0 {
		t.Fatal("downsampler did not report filter delay")
	}
}

func TestDownsample48To16RejectsOutOfBandAlias(t *testing.T) {
	const samples = 48000
	makeTone := func(hz float64) []int16 {
		out := make([]int16, samples)
		for i := range out {
			out[i] = int16(math.Round(20000 * math.Sin(2*math.Pi*hz*float64(i)/Rate48kHz)))
		}
		return out
	}
	pass := processStreaming(t, Rate48kHz, Rate16kHz, [][]int16{makeTone(1000)})
	stop := processStreaming(t, Rate48kHz, Rate16kHz, [][]int16{makeTone(12000)})
	passRMS := pcmRMS(pass[200:])
	stopRMS := pcmRMS(stop[200:])
	if passRMS < 10000 {
		t.Fatalf("passband RMS=%f", passRMS)
	}
	if stopRMS > passRMS*0.02 {
		t.Fatalf("aliased stopband RMS=%f, passband=%f", stopRMS, passRMS)
	}
}

func TestStreamingResamplerSilenceAndExtremes(t *testing.T) {
	for _, input := range [][]int16{make([]int16, 2000), {math.MinInt16, math.MaxInt16, math.MinInt16, math.MaxInt16}} {
		got := processStreaming(t, Rate48kHz, Rate16kHz, [][]int16{input})
		if allPCMZero(input) && !allPCMZero(got) {
			t.Fatal("silence produced nonzero output")
		}
	}
}

func processStreaming(t *testing.T, inRate, outRate int, chunks [][]int16) []int16 {
	t.Helper()
	r, err := NewPCM16Resampler(inRate, outRate)
	if err != nil {
		t.Fatal(err)
	}
	var result []int16
	for i, chunk := range chunks {
		out, err := r.Process(chunk, i == len(chunks)-1)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, out...)
	}
	return result
}
func pcmRMS(samples []int16) float64 {
	var sum float64
	for _, v := range samples {
		x := float64(v)
		sum += x * x
	}
	return math.Sqrt(sum / float64(len(samples)))
}
func allPCMZero(samples []int16) bool {
	for _, v := range samples {
		if v != 0 {
			return false
		}
	}
	return true
}

// An exact rational source position does not need interpolation lookahead.
func TestStreamingResamplerEmitsExactPositionWithoutLookahead(t *testing.T) {
	r, err := NewPCM16Resampler(Rate16kHz, Rate24kHz)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Process([]int16{1234}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 1234 {
		t.Fatalf("first available sample = %v", got)
	}
	tail, err := r.Process(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0] != 1234 {
		t.Fatalf("tail = %v", tail)
	}
}
