package agentruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration pins
// the fix for "agent session hard-fails unless --record or --replay is
// supplied, for any non-bare invocation" (operator: "we should not require a
// record flag, that was a mistake"). Recording is an optional capability, not
// an admission requirement: a session named by any explicit mode (a prompt,
// audio input, an image, browser tools, ...) or by no mode at all (a bare
// invocation) must be admitted by validateSessionRunOptions without a
// --record or --replay path. This must FAIL on the unpatched
// validateSessionRunOptions, which unconditionally rejected every
// combination below except the already-exempted bare and browser-tools
// cases.
func TestValidateSessionRunOptionsAdmitsSessionsWithoutCaptureConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts SessionRunOptions
	}{
		{
			name: "prompt only, no record or replay",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog(), Prompt: "hello", PromptProvided: true},
		},
		{
			name: "scheduled audio input, no record or replay",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog(), AudioInputs: []ScheduledAudioInput{{}}},
		},
		{
			name: "bare invocation, no record or replay",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog(), BareLive: true},
		},
		{
			name: "browser tools enabled, no record or replay",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog(), BrowserToolsEnabled: true},
		},
		{
			name: "no mode flags and not bare (e.g. audio-in, image) still admitted",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog()},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateSessionRunOptions(testCase.opts); err != nil {
				t.Fatalf("validateSessionRunOptions(%+v) = %v, want admission (nil error)", testCase.opts, err)
			}
		})
	}
}

// TestValidateSessionRunOptionsStillRejectsRecordAndReplayTogether pins that
// removing the blanket --record/--replay requirement did not weaken the
// still-meaningful capture-mode conflict and extension checks that follow it.
func TestValidateSessionRunOptionsStillRejectsRecordAndReplayTogether(t *testing.T) {
	err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), RecordPath: "a.json", ReplayPath: "b.json"})
	if err == nil || !strings.Contains(err.Error(), "does not support --record and --replay together") {
		t.Fatalf("validateSessionRunOptions() = %v, want the record/replay conflict error", err)
	}
}

// TestValidateSessionRunOptionsStillRejectsNonJSONRecordPath pins that the
// --record extension check is unaffected by removing the admission gate.
func TestValidateSessionRunOptionsStillRejectsNonJSONRecordPath(t *testing.T) {
	err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), RecordPath: "capture.txt"})
	if err == nil || !strings.Contains(err.Error(), `--record path "capture.txt" must end with .json`) {
		t.Fatalf("validateSessionRunOptions() = %v, want the --record extension error", err)
	}
}

// TestValidateSessionRunOptionsAdmitsRecordAndReplay pins that a genuine
// --record or --replay invocation is still admitted exactly as before: a
// --record path only needs the .json extension, and a --replay path must
// resolve to a real, valid capture.
func TestValidateSessionRunOptionsAdmitsRecordAndReplay(t *testing.T) {
	if err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), RecordPath: filepath.Join(t.TempDir(), "capture.json")}); err != nil {
		t.Fatalf("validateSessionRunOptions(--record) = %v, want admission", err)
	}

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "grok", Model: "grok-replay"},
		Session:  gwtesting.SessionMetadata{FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic},
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	replayPath := filepath.Join(t.TempDir(), "replay.json")
	if writeErr := os.WriteFile(replayPath, data, 0o600); writeErr != nil {
		t.Fatalf("write capture: %v", writeErr)
	}
	if err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), ReplayPath: replayPath}); err != nil {
		t.Fatalf("validateSessionRunOptions(--replay) = %v, want admission", err)
	}
}

func TestValidateSessionRunOptionsReplayTiming(t *testing.T) {
	if err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), ReplayTiming: "recorded"}); err == nil || !strings.Contains(err.Error(), "requires --replay") {
		t.Fatalf("recorded timing without replay error = %v, want --replay requirement", err)
	}
	if err := validateSessionRunOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), ReplayTiming: "elastic"}); err == nil || !strings.Contains(err.Error(), "immediate or recorded") {
		t.Fatalf("invalid replay timing error = %v, want accepted-value guidance", err)
	}
}
