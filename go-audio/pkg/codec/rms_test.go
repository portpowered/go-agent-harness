package codec

import (
	"math"
	"testing"
)

func TestRMSExactAmplitudeAndEmptyInput(t *testing.T) {
	if got := RMS(nil); got != 0 {
		t.Fatalf("RMS(nil) = %v, want 0", got)
	}
	if got := RMS([]int16{3, 4}); math.Abs(got-3.5355339059327378) > 1e-12 {
		t.Fatalf("RMS([3 4]) = %.16f, want %.16f", got, 3.5355339059327378)
	}
	if got := RMS([]int16{-32768, -32768}); got != PCM16FullScale {
		t.Fatalf("RMS(negative full scale) = %v, want %d", got, PCM16FullScale)
	}
}

func TestPCM16RMSMatchesDecodedSamplesAndIgnoresPartialSample(t *testing.T) {
	samples := []int16{-32768, -1, 0, 1, 32767, 1200}
	encoded := EncodePCM16(samples)
	if got, want := PCM16RMS(encoded), RMS(samples); got != want {
		t.Fatalf("PCM16RMS = %v, want decoded RMS %v", got, want)
	}
	if got, want := PCM16RMS(append(encoded, 0x7f)), RMS(samples); got != want {
		t.Fatalf("PCM16RMS with trailing partial byte = %v, want %v", got, want)
	}
	for _, short := range [][]byte{nil, {0x7f}} {
		if got := PCM16RMS(short); got != 0 {
			t.Fatalf("PCM16RMS(%v) = %v, want 0", short, got)
		}
	}
}

func TestPCM16SamplesDecodesCompleteSamplesWithoutBound(t *testing.T) {
	want := []int16{-32768, -1, 0, 1, 32767}
	got := PCM16Samples(append(EncodePCM16(want), 0x7f))
	if len(got) != len(want) {
		t.Fatalf("PCM16Samples decoded %d samples, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sample %d = %d, want %d", index, got[index], want[index])
		}
	}
	if large := PCM16Samples(make([]byte, MaxPCM16Bytes+2)); len(large) != MaxPCM16Bytes/2+1 {
		t.Fatalf("PCM16Samples applied a payload bound: %d samples", len(large))
	}
}
