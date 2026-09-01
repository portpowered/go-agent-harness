package services

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidOpenAIRealtimeVoice identifies a voice that is not one of the
	// built-in voices supported by the OpenAI Realtime session surface.
	ErrInvalidOpenAIRealtimeVoice = errors.New("invalid OpenAI Realtime voice")

	openAIRealtimeVoiceRegistry = [...]string{
		"alloy",
		"ash",
		"ballad",
		"cedar",
		"coral",
		"echo",
		"marin",
		"sage",
		"shimmer",
		"verse",
	}

	// openAIRealtimeVoiceLoudnessGainDB corrects the customer-facing defect
	// where --voice selection silently changes call volume: with no
	// compensating gain, --voice verse rendered ~7.2-7.4 dB quieter (RMS)
	// than --voice alloy, reproduced identically across independent live
	// room runs (see the s2s-normalize-loudness-across-voices ops history).
	// alloy is the measured reference/baseline (the audio-quality probe
	// found it clean: zero clipped samples, a clean edge profile), so it is
	// listed at 0 dB for clarity though that is also the unset default.
	// Every voice without an entry here -- including every voice not yet
	// independently measured -- defaults to 0 dB (no adjustment): that is
	// the deliberately conservative choice, since fabricating a correction
	// without a measurement could just as easily make an unmeasured voice's
	// level worse.
	openAIRealtimeVoiceLoudnessGainDB = map[string]float64{
		"alloy": 0.0,
		"verse": 7.3,
	}
)

// InvalidOpenAIRealtimeVoiceError reports a value outside the documented
// built-in OpenAI Realtime voice set.
//
// The registry follows the OpenAI Realtime API Reference voice parameter:
// https://platform.openai.com/docs/api-reference/realtime (verified 2026-08-26).
type InvalidOpenAIRealtimeVoiceError struct {
	Voice           string
	SupportedVoices []string
}

func (e *InvalidOpenAIRealtimeVoiceError) Error() string {
	if e == nil {
		return ErrInvalidOpenAIRealtimeVoice.Error()
	}
	return fmt.Sprintf(
		"invalid OpenAI Realtime voice %q; supported voices: %s",
		e.Voice,
		strings.Join(e.SupportedVoices, ", "),
	)
}

func (e *InvalidOpenAIRealtimeVoiceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrInvalidOpenAIRealtimeVoice
}

// SupportedOpenAIRealtimeVoices returns the documented built-in voices in a
// deterministic order. The returned slice is independent from the registry.
func SupportedOpenAIRealtimeVoices() []string {
	voices := make([]string, len(openAIRealtimeVoiceRegistry))
	copy(voices, openAIRealtimeVoiceRegistry[:])
	return voices
}

// ValidateOpenAIRealtimeVoice accepts the empty value to preserve the
// provider-selected default and otherwise requires an exact built-in voice.
func ValidateOpenAIRealtimeVoice(voice string) error {
	if voice == "" {
		return nil
	}
	for _, supported := range openAIRealtimeVoiceRegistry {
		if voice == supported {
			return nil
		}
	}
	return &InvalidOpenAIRealtimeVoiceError{
		Voice:           voice,
		SupportedVoices: SupportedOpenAIRealtimeVoices(),
	}
}

// VoiceLoudnessGainDB returns the fixed output gain, in dB, that normalizes
// voice toward the shared cross-voice loudness target. An empty, unknown, or
// unmeasured voice returns exactly 0 (no adjustment): the map's zero value
// is the documented, deliberately conservative default described on
// openAIRealtimeVoiceLoudnessGainDB.
func VoiceLoudnessGainDB(voice string) float64 {
	return openAIRealtimeVoiceLoudnessGainDB[voice]
}
