package ttscorpus

import (
	"bytes"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// Clip sanity bounds from the pin contract (docs/architecture/s2s-tts-pinning.md).
const (
	// SilenceThresholdRMS is the minimum normalized RMS of a valid clip.
	SilenceThresholdRMS = 0.001
	// MinDurationSeconds and MaxDurationSeconds bound valid clip duration.
	MinDurationSeconds = 0.25
	MaxDurationSeconds = 8.0
)

// ValidateClip asserts a decoded clip has strictly positive energy above the
// silence threshold and a duration inside the inclusive pin bounds.
func ValidateClip(sampleRate int, samples []int16) error {
	rms := codec.RMS(samples) / codec.PCM16FullScale
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
