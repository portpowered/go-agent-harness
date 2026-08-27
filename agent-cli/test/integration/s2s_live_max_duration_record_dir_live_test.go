//go:build live

// Opt-in live proof for the exact max-duration recording reproduction. The
// hermetic CLI test remains the default regression proof; this test requires
// an explicit billing opt-in and keeps all generated artifacts in TempDir.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLiveSession_MaxDurationRecordDirTerminalAgreement(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live OpenAI Realtime max-duration record-dir proof")
	}
	if os.Getenv("AGENT_HARNESS_LIVE_MAX_DURATION") != "1" {
		t.Skip("AGENT_HARNESS_LIVE_MAX_DURATION!=1; this live test bills real API usage and must be opted into explicitly")
	}

	workDir := t.TempDir()
	capturePath := filepath.Join(workDir, "max-duration-live.session.json")
	recordDir := filepath.Join(workDir, "max-duration-live-recording")
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI composition: %v", err)
	}

	stdout := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", workDir,
		"session",
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", apiKey,
		"--record", capturePath,
		"--record-dir", recordDir,
		"--max-duration", "5s",
		"--system-prompt", "Speak continuously for at least 60 seconds without stopping or ending the response.",
		"Start speaking now and continue continuously.",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("live max-duration command returned status 1: %v\nstdout: %s", err, stdout.String())
	}
	if strings.TrimSpace(stdout.String()) == "" || !strings.Contains(stdout.String(), "terminal_reason=max_duration") || !strings.Contains(stdout.String(), "output_state=partial") {
		t.Fatalf("live max-duration output did not prove a partial planned cutoff: %q", stdout.String())
	}

	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load live raw capture: %v", err)
	}
	if !captureHasWireRecord(capture, gwtesting.DirectionServerToClient, "response.output_text.delta") && !captureHasWireRecord(capture, gwtesting.DirectionServerToClient, "response.output_audio.delta") {
		t.Fatalf("live raw capture omitted observed provider output")
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && (record.Type == "response.done" || record.Type == "session.closed") {
			t.Fatalf("live raw capture contains a provider terminal before the planned cutoff: %q", record.Type)
		}
	}

	sidecarPath := strings.TrimSuffix(capturePath, filepath.Ext(capturePath)) + ".jsonl"
	sidecar := readSessionDurationSidecarTerminal(t, sidecarPath)
	if sidecar.count != 1 {
		t.Fatalf("live sidecar terminal count = %d, want exactly one", sidecar.count)
	}
	assertMaxDurationTerminalFields(t, "live sidecar", sidecar.fields)

	manifestBytes, err := os.ReadFile(filepath.Join(recordDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read live record-dir manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode live record-dir manifest: %v", err)
	}
	wantSummary := transcript.RecordingTerminalSummary{
		Reason:             "max_duration",
		Classification:     "max_duration",
		TerminalReason:     messages.TerminalReason("max_duration"),
		TerminalProvenance: messages.TerminalProvenanceLoop,
		OutputState:        messages.TerminalOutputPartial,
	}
	if manifest.Terminal == nil || *manifest.Terminal != wantSummary {
		t.Fatalf("live record-dir terminal summary = %+v, want %+v", manifest.Terminal, wantSummary)
	}
	var manifestFields map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &manifestFields); err != nil {
		t.Fatalf("decode live manifest fields: %v", err)
	}
	var terminalFields map[string]json.RawMessage
	if err := json.Unmarshal(manifestFields["terminal"], &terminalFields); err != nil {
		t.Fatalf("decode live terminal fields: %v", err)
	}
	if len(terminalFields) != 5 {
		t.Fatalf("live manifest terminal field count = %d, want exactly 5", len(terminalFields))
	}
	manifestTerminal := assertMaxDurationTerminalJSONFields(t, "live record-dir manifest", terminalFields)
	assertTerminalFieldAgreement(t, "live sidecar vs record-dir manifest", sidecar.fields, manifestTerminal)

	if len(manifest.Artifacts) == 0 {
		t.Fatal("live record-dir manifest has no artifacts")
	}
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(recordDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read live record-dir artifact %q: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("live record-dir artifact hash for %q = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}

	t.Logf("live max-duration proof: status=0, partial output, one sidecar terminal, matching five-field record-dir terminal, %d hash-verified artifacts", len(manifest.Artifacts))
}
