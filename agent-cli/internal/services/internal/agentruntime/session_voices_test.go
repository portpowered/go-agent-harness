package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

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
	got := sessioncontract.SupportedOpenAIRealtimeVoices()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("supported voices = %v, want %v", got, want)
	}

	for _, voice := range append([]string{""}, want...) {
		t.Run("accept/"+voice, func(t *testing.T) {
			if err := sessioncontract.ValidateOpenAIRealtimeVoice(voice); err != nil {
				t.Fatalf("sessioncontract.ValidateOpenAIRealtimeVoice(%q): %v", voice, err)
			}
		})
	}

	got[0] = "mutated"
	if sessioncontract.SupportedOpenAIRealtimeVoices()[0] != want[0] {
		t.Fatal("supported voice registry was mutated through returned slice")
	}
}

func TestValidateOpenAIRealtimeVoiceReturnsStableTypedError(t *testing.T) {
	const rejected = "fable"

	err := sessioncontract.ValidateOpenAIRealtimeVoice(rejected)
	if err == nil {
		t.Fatal("expected invalid voice error")
	}
	if !errors.Is(err, sessioncontract.ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want sessioncontract.ErrInvalidOpenAIRealtimeVoice", err)
	}
	var typed *sessioncontract.InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want sessioncontract.InvalidOpenAIRealtimeVoiceError", err)
	}
	if typed.Voice != rejected {
		t.Fatalf("rejected voice = %q, want %q", typed.Voice, rejected)
	}
	for _, supported := range sessioncontract.SupportedOpenAIRealtimeVoices() {
		if !strings.Contains(err.Error(), supported) {
			t.Fatalf("error %q does not list supported voice %q", err, supported)
		}
	}
}

// TestVoiceLoudnessGainDBAppliesOnlyMeasuredCorrections pins the fixed
// per-voice loudness gain table to the values measured from one live
// gpt-realtime-2.1-mini session per voice (see the PR body for the full
// before/after table, calibration utterance, and frame-selection method).
// All 10 documented built-in voices are measured; only a voice absent from
// the registry (or the empty/unset default) falls back to 0 dB.
func TestVoiceLoudnessGainDBAppliesOnlyMeasuredCorrections(t *testing.T) {
	want := map[string]float64{
		"alloy":   0.0,
		"ash":     6.2,
		"ballad":  9.3,
		"cedar":   3.9,
		"coral":   10.0,
		"echo":    5.5,
		"marin":   5.5,
		"sage":    15.1,
		"shimmer": 2.4,
		"verse":   8.3,
	}
	supported := sessioncontract.SupportedOpenAIRealtimeVoices()
	if len(want) != len(supported) {
		t.Fatalf("gain table has %d entries, registry has %d voices; every built-in voice must be measured", len(want), len(supported))
	}
	for _, voice := range supported {
		wantGain, ok := want[voice]
		if !ok {
			t.Fatalf("registry voice %q has no expected gain in this test's table", voice)
		}
		if got := VoiceLoudnessGainDB(voice); got != wantGain {
			t.Fatalf("VoiceLoudnessGainDB(%q) = %v, want %v", voice, got, wantGain)
		}
	}
	if got := VoiceLoudnessGainDB(""); got != 0 {
		t.Fatalf("VoiceLoudnessGainDB(\"\") = %v, want 0 (unset default)", got)
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
	if !errors.Is(err, sessioncontract.ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want sessioncontract.ErrInvalidOpenAIRealtimeVoice", err)
	}
	if strings.Contains(err.Error(), "missing.session.json") {
		t.Fatalf("invalid voice validation consumed replay path: %v", err)
	}
}
