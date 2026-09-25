//go:build live

// Bounded, billed live session process proofs, excluded from ordinary and
// hermetic test runs:
//   - the zero-flag live voice path (opt in with
//     AGENT_HARNESS_LIVE_BARE_SESSION=1 and provide OPENAI_API_KEY);
//   - the exact max-duration recording reproduction (opt in with
//     AGENT_HARNESS_LIVE_MAX_DURATION=1). The hermetic CLI test remains the
//     default regression proof and all generated artifacts stay in TempDir.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	bareSessionLiveOptIn          = "AGENT_HARNESS_LIVE_BARE_SESSION"
	bareSessionLiveModel          = "gpt-realtime-2.1-mini"
	bareSessionLiveListeningBound = 10 * time.Second
	bareSessionLiveShutdownBound  = 5 * time.Second
)

// TestLiveBareSessionDefaultDevicesStartsAndStops is intentionally a
// process-boundary probe. The child receives only the positional "session"
// argument; all live-session defaults must therefore come from the shipped
// command's bare resolver and the host's default device registry.
func TestLiveBareSessionDefaultDevicesStartsAndStops(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the billed bare-session live probe")
	}
	if os.Getenv(bareSessionLiveOptIn) != "1" {
		t.Skip(bareSessionLiveOptIn + "!=1; this live test bills provider and opens host audio devices")
	}

	home := t.TempDir()
	cmd := exec.Command(buildAgentBinary(t), "session")
	cmd.Dir = agentCLIRoot(t)
	cmd.Env = bareSessionLiveEnvironment(home, apiKey)
	if len(cmd.Args) != 2 || cmd.Args[1] != "session" {
		t.Fatalf("bare probe argv = %q, want exactly [binary session]", cmd.Args)
	}

	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bare live session: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			killLiveProcess(cmd.Process)
		}
	}()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	started := time.Now()
	if err := waitForBareSessionListening(t, &stdout, &stderr, wait, bareSessionLiveListeningBound); err != nil {
		t.Fatalf("bare live session did not reach listening: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
	listeningElapsed := time.Since(started)
	combined := stdout.String() + stderr.String()
	assertBareSessionLiveReadyOutput(t, combined)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send one SIGINT to bare live session: %v", err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("bare live session after one SIGINT: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(bareSessionLiveShutdownBound):
		killLiveProcess(cmd.Process)
		<-wait
		t.Fatalf("bare live session did not finish within %s after one SIGINT", bareSessionLiveShutdownBound)
	}

	combined = stdout.String() + stderr.String()
	assertBareSessionLiveTerminal(t, combined)
	t.Logf("bare live startup probe: argv=session provider=openai model=%s transport=ws input-device=%s output-device=%s session.created=observed readiness=listening elapsed-to-listening=%s sigint=one terminal=clean", bareSessionLiveModel, bareSessionLiveField(combined, "input-device"), bareSessionLiveField(combined, "output-device"), listeningElapsed.Round(time.Millisecond))
}

func bareSessionLiveEnvironment(home, apiKey string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "HOME" || name == "OPENAI_API_KEY" || name == "OPENAI_API_KEY_FILE" || strings.HasPrefix(name, "AGENT_") || strings.Contains(strings.ToUpper(name), "API_KEY") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+home, "OPENAI_API_KEY="+apiKey)
	return env
}

func waitForBareSessionListening(t *testing.T, stdout, stderr *syncBuffer, wait <-chan error, bound time.Duration) error {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if strings.Contains(stdout.String()+stderr.String(), "Listening:") {
			return nil
		}
		select {
		case err := <-wait:
			if err == nil {
				return errors.New("process exited before listening")
			}
			return err
		case <-deadline.C:
			return errors.New("listening deadline exceeded")
		case <-ticker.C:
		}
	}
}

func assertBareSessionLiveReadyOutput(t *testing.T, output string) {
	t.Helper()
	if strings.Count(output, "Starting bare live session:") != 1 || strings.Count(output, "Listening:") != 1 {
		t.Fatalf("bare live readiness output = %q, want one startup and one listening banner", output)
	}
	for _, want := range []string{
		"provider=openai",
		"model=" + bareSessionLiveModel,
		"transport=ws",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("bare live readiness output missing %q: %q", want, output)
		}
	}
	for _, field := range []string{"input-device", "output-device"} {
		value := bareSessionLiveField(output, field)
		if value == "" || value == "unavailable" {
			t.Fatalf("bare live readiness %s = %q, want a host default device identity: %q", field, value, output)
		}
	}
	if strings.Contains(output, "OPENAI_API_KEY") || strings.Contains(output, "sk-") {
		t.Fatalf("bare live readiness output contains credential-shaped data: %q", output)
	}
}

func assertBareSessionLiveTerminal(t *testing.T, output string) {
	t.Helper()
	if strings.Count(output, "[session terminal:") != 1 {
		t.Fatalf("bare live terminal count = %d, want one: %q", strings.Count(output, "[session terminal:"), output)
	}
	want := "classification=user_cancelled terminal_reason=cancellation terminal_provenance=cli output_state=none"
	if !strings.Contains(output, want) {
		t.Fatalf("bare live terminal = %q, want clean one-signal cancellation containing %q", output, want)
	}
}

func bareSessionLiveField(output, name string) string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Listening:") {
			continue
		}
		for _, field := range strings.Fields(line) {
			prefix := name + "="
			if strings.HasPrefix(field, prefix) {
				return strings.TrimPrefix(field, prefix)
			}
		}
	}
	return ""
}

// Wire and classification values shared by the opt-in live suites.
const (
	liveProviderOpenAI                = "openai"
	liveMIMEImagePNG                  = "image/png"
	liveUnknownCode                   = "unknown"
	liveBargeInRuntimeContractFailure = "runtime-contract-failure"
	rtStatusIncomplete                = "incomplete"
	rtEventError                      = "error"
	rtEventOutputAudioTranscriptDone  = "response.output_audio_transcript.done"
	rtEventConversationItemCreated    = "conversation.item.created"
	rtEventLegacyAudioDelta           = "response.audio.delta"
)

// killLiveProcess force-stops a live child on a teardown path. The kill error
// only means the process already exited, which cannot change the outcome the
// test asserts on.
func killLiveProcess(process *os.Process) {
	if err := process.Kill(); err != nil {
		return
	}
}

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
		"--provider", liveProviderOpenAI,
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

	assertLiveMaxDurationCapture(t, capturePath)
	sidecarPath := strings.TrimSuffix(capturePath, filepath.Ext(capturePath)) + ".jsonl"
	sidecar := readSessionDurationSidecarTerminal(t, sidecarPath)
	if sidecar.count != 1 {
		t.Fatalf("live sidecar terminal count = %d, want exactly one", sidecar.count)
	}
	assertMaxDurationTerminalFields(t, "live sidecar", sidecar.fields)
	artifacts := assertLiveMaxDurationRecordDir(t, recordDir, sidecar.fields)
	t.Logf("live max-duration proof: status=0, partial output, one sidecar terminal, matching five-field record-dir terminal, %d hash-verified artifacts", artifacts)
}

// assertLiveMaxDurationCapture requires observed provider output and no
// provider terminal before the planned cutoff.
func assertLiveMaxDurationCapture(t *testing.T, capturePath string) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load live raw capture: %v", err)
	}
	if !captureHasWireRecord(capture, gwtesting.DirectionServerToClient, rtEventOutputTextDelta) && !captureHasWireRecord(capture, gwtesting.DirectionServerToClient, rtEventOutputAudioDelta) {
		t.Fatalf("live raw capture omitted observed provider output")
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && (record.Type == rtEventResponseDone || record.Type == rtEventSessionClosed) {
			t.Fatalf("live raw capture contains a provider terminal before the planned cutoff: %q", record.Type)
		}
	}
}

// assertLiveMaxDurationRecordDir checks the record-dir terminal summary
// against the sidecar terminal and verifies every artifact hash. It returns
// the verified artifact count.
func assertLiveMaxDurationRecordDir(t *testing.T, recordDir string, sidecarFields map[string]string) int {
	t.Helper()
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
	assertTerminalFieldAgreement(t, "live sidecar vs record-dir manifest", sidecarFields, manifestTerminal)

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
	return len(manifest.Artifacts)
}
