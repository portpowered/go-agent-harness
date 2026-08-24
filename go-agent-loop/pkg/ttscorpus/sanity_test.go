package ttscorpus

import (
	"bytes"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestValidateClipAcceptsAudibleInBoundsClip(t *testing.T) {
	samples := loudSamples(wavio.Rate24kHz, 1000)
	if err := ValidateClip(wavio.Rate24kHz, samples); err != nil {
		t.Fatalf("ValidateClip() = %v", err)
	}
}

func TestSilentNegativeControlFailsRMS(t *testing.T) {
	silent := make([]int16, wavio.Rate16kHz)
	err := ValidateClip(wavio.Rate16kHz, silent)
	if err == nil || !strings.Contains(err.Error(), "silence threshold") {
		t.Fatalf("silent clip error = %v; want silence-threshold failure", err)
	}
	if RMS(silent) != 0 {
		t.Fatalf("RMS(silence) = %f", RMS(silent))
	}
}

func TestTruncatedNegativeControlFailsDuration(t *testing.T) {
	short := loudSamples(wavio.Rate24kHz, int(MinDurationSeconds*1000)/10) // 25ms
	err := ValidateClip(wavio.Rate24kHz, short)
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("truncated clip error = %v; want duration failure", err)
	}
}

func TestOverlongClipFailsDuration(t *testing.T) {
	long := loudSamples(wavio.Rate24kHz, wavio.Rate24kHz*(int(MaxDurationSeconds)+1))
	if err := ValidateClip(wavio.Rate24kHz, long); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("overlong clip error = %v; want duration failure", err)
	}
}

func TestValidateWAVBytesAppliesContract(t *testing.T) {
	var audible bytes.Buffer
	if err := wavio.Write(&audible, wavio.Rate24kHz, loudSamples(wavio.Rate24kHz, 500)); err != nil {
		t.Fatal(err)
	}
	if err := validateWAVBytes(audible.Bytes()); err != nil {
		t.Fatalf("x= %v", err)
	}
	var silent bytes.Buffer
	if err := wavio.Write(&silent, wavio.Rate24kHz, make([]int16, wavio.Rate24kHz)); err != nil {
		t.Fatal(err)
	}
	if err := validateWAVBytes(silent.Bytes()); err == nil {
		t.Fatal("validateWAVBytes(silent) succeeded")
	}
	if err := validateWAVBytes([]byte("not a wav")); err == nil {
		t.Fatal("validateWAVBytes(garbage) succeeded")
	}
}

func loudSamples(rate, milliseconds int) []int16 {
	samples := make([]int16, rate*milliseconds/1000)
	for i := range samples {
		samples[i] = 12000
	}
	return samples
}
