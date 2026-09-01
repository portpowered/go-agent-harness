package services

import (
	"context"
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

// TestVoiceLoudnessGainDBAppliesOnlyMeasuredCorrections pins the fixed
// per-voice loudness gain table: alloy (the measured reference) and verse
// (the measured ~7.3 dB deficit) are the only two voices with an
// independent loudness measurement, and every other documented voice --
// plus the empty/unset default -- must resolve to exactly 0 dB. That 0 dB
// default is what keeps normalization from disturbing any existing test
// that does not explicitly configure a corrected voice.
func TestVoiceLoudnessGainDBAppliesOnlyMeasuredCorrections(t *testing.T) {
	if got := VoiceLoudnessGainDB("alloy"); got != 0 {
		t.Fatalf("VoiceLoudnessGainDB(alloy) = %v, want 0", got)
	}
	if got := VoiceLoudnessGainDB("verse"); got != 7.3 {
		t.Fatalf("VoiceLoudnessGainDB(verse) = %v, want 7.3", got)
	}
	for _, voice := range append([]string{""}, SupportedOpenAIRealtimeVoices()...) {
		if voice == "alloy" || voice == "verse" {
			continue
		}
		if got := VoiceLoudnessGainDB(voice); got != 0 {
			t.Fatalf("VoiceLoudnessGainDB(%q) = %v, want 0 (unmeasured voices default to no adjustment)", voice, got)
		}
	}
	if got := VoiceLoudnessGainDB("not-a-real-voice"); got != 0 {
		t.Fatalf("VoiceLoudnessGainDB(unknown) = %v, want 0", got)
	}
}

func TestRunSession_InvalidVoiceFailsBeforeReplayConsumption(t *testing.T) {
	err := RunSession(context.Background(), io.Discard, SessionRunOptions{
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
