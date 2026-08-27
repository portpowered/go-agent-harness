package services

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOpenAIRealtimeVoiceAcceptsOnlyDocumentedBuiltIns(t *testing.T) {
	want := []string{"alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse"}
	got := SupportedOpenAIRealtimeVoices()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("supported voices = %v, want %v", got, want)
	}

	for _, voice := range append([]string{""}, want...) {
		t.Run("accept/"+voice, func(t *testing.T) {
			if err := ValidateOpenAIRealtimeVoice(voice); err != nil {
				t.Fatalf("ValidateOpenAIRealtimeVoice(%q): %v", voice, err)
			}
		})
	}

	got[0] = "mutated"
	if SupportedOpenAIRealtimeVoices()[0] != want[0] {
		t.Fatal("supported voice registry was mutated through returned slice")
	}
}

func TestValidateOpenAIRealtimeVoiceReturnsStableTypedError(t *testing.T) {
	const rejected = "fable"

	err := ValidateOpenAIRealtimeVoice(rejected)
	if err == nil {
		t.Fatal("expected invalid voice error")
	}
	if !errors.Is(err, ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want ErrInvalidOpenAIRealtimeVoice", err)
	}
	var typed *InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want InvalidOpenAIRealtimeVoiceError", err)
	}
	if typed.Voice != rejected {
		t.Fatalf("rejected voice = %q, want %q", typed.Voice, rejected)
	}
	for _, supported := range SupportedOpenAIRealtimeVoices() {
		if !strings.Contains(err.Error(), supported) {
			t.Fatalf("error %q does not list supported voice %q", err, supported)
		}
	}
}

func TestRunSession_InvalidVoiceFailsBeforeReplayConsumption(t *testing.T) {
	err := RunSession(nil, io.Discard, SessionRunOptions{
		ReplayPath: filepath.Join(t.TempDir(), "missing.session.json"),
		Voice:      "not-a-voice",
	})
	if err == nil {
		t.Fatal("expected invalid voice error")
	}
	if !errors.Is(err, ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want ErrInvalidOpenAIRealtimeVoice", err)
	}
	if strings.Contains(err.Error(), "missing.session.json") {
		t.Fatalf("invalid voice validation consumed replay path: %v", err)
	}
}
