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
