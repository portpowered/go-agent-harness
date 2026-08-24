package ttscorpus

import (
	"bytes"
	"fmt"
	"math"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// Clip sanity bounds from the pin contract (docs/architecture/s2s-tts-pinning.md).
const (
	// SilenceThresholdRMS is the minimum normalized RMS of a valid clip.
	SilenceThresholdRMS = 0.001
	// MinDurationSeconds and MaxDurationSeconds bound valid clip duration.
	MinDurationSeconds = 0.25
	MaxDurationSeconds = 8.0
)

// RMS returns the root mean square of signed PCM16 samples normalized to
// [-1, 1): sqrt(mean((s / 32768)^2)).
func RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	sum := 0.0
	for _, sample := range samples {
		normalized := float64(sample) / 32768.0
		sum += normalized * normalized
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// ValidateClip asserts a decoded clip has strictly positive energy above the
// silence threshold and a duration inside the inclusive pin bounds.
func ValidateClip(sampleRate int, samples []int16) error {
	rms := RMS(samples)
	if rms <= SilenceThresholdRMS {
		return fmt.Errorf("ttscorpus: audio RMS %f is not strictly above silence threshold %f", rms, SilenceThresholdRMS)
	}
	duration := float64(len(samples)) / float64(sampleRate)
	if duration < MinDurationSeconds || duration > MaxDurationSeconds {
		return fmt.Errorf("ttscorpus: audio duration %fs at %d Hz is outside the inclusive range [%f, %f] seconds", duration, sampleRate, MinDurationSeconds, MaxDurationSeconds)
	}
	return nil
}

// validateWAVBytes decodes a WAV payload and applies the clip sanity contract.
func validateWAVBytes(data []byte) error {
	sampleRate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("ttscorpus: decode WAV: %w", err)
	}
	return ValidateClip(sampleRate, samples)
}
