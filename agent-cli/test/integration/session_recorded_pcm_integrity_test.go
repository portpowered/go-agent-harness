package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

func TestSessionRecordedPCMIntegrity(t *testing.T) {
	source := writeRecordedPCMIntegrityBundle(t)
	untouched := copyRecordedPCMIntegrityBundle(t, source, "untouched")

	stdout, stderr, err := runRecordedPCMIntegrityCLI(t, "replay", untouched)
	if err != nil {
		t.Fatalf("session replay untouched bundle: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "Replay verified") {
		t.Fatalf("session replay did not report verification: stdout=%q stderr=%q", stdout, stderr)
	}

	sessionOutput := filepath.Join(t.TempDir(), "session-output.pcm")
	stdout, stderr, err = runRecordedPCMIntegrityCLI(t, "--replay", untouched, "--audio-out", sessionOutput, "--max-duration", "5s")
	if err != nil {
		t.Fatalf("session --replay untouched bundle: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
	}
	for _, expected := range []string{"classification=replay_complete", "output_state=complete", "[session replay complete]"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("session --replay output missing %q: stdout=%q stderr=%q", expected, stdout, stderr)
		}
	}

	mutated := copyRecordedPCMIntegrityBundle(t, source, "mutated")
	pcmPath := filepath.Join(mutated, "audio/out-000.pcm")
	pcm, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	pcm[0] ^= 1
	if err := os.WriteFile(pcmPath, pcm, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err = runRecordedPCMIntegrityCLI(t, "replay", mutated)
	assertRecordedPCMIntegrityFailure(t, err, stdout, stderr)
	stdout, stderr, err = runRecordedPCMIntegrityCLI(t, "--replay", mutated, "--audio-out", filepath.Join(t.TempDir(), "mutated-output.pcm"), "--max-duration", "5s")
	assertRecordedPCMIntegrityFailure(t, err, stdout, stderr)
}

func runRecordedPCMIntegrityCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	var stdout, stderr bytes.Buffer
	root := agentCLI.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"session"}, args...))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func assertRecordedPCMIntegrityFailure(t *testing.T, err error, stdout, stderr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("mutated recording unexpectedly succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	message := err.Error() + "\n" + stdout + "\n" + stderr
	for _, expected := range []string{"audio/out-000.pcm", "digest mismatch"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("mutated recording error = %q, want %q", message, expected)
		}
	}
	if strings.Contains(message, "Replay verified") || strings.Contains(message, "[session replay complete]") {
		t.Fatalf("mutated recording emitted success evidence: %q", message)
	}
}

func writeRecordedPCMIntegrityBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	tracePath := filepath.Join(root, "audio-trace")
	writeRecordedPCMIntegrityTrace(t, tracePath)
	providerPath := filepath.Join(root, "provider.json")
	writeOpenAIBarePromptCapture(t, providerPath)

	artifacts := []struct {
		path string
		data []byte
	}{
		{path: "client.transcript.jsonl", data: []byte("client transcript\n")},
		{path: "agent.transcript.jsonl", data: []byte("agent transcript\n")},
		{path: "provider.json", data: mustReadRecordedPCMIntegrityFile(t, providerPath)},
		{path: "audio/out-000.pcm", data: []byte{1, 2, 3, 4}},
	}
	hashes := make([]transcript.ArtifactHash, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := filepath.Join(root, filepath.FromSlash(artifact.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact.data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(artifact.data)
		hashes = append(hashes, transcript.ArtifactHash{Path: artifact.path, SHA256: hex.EncodeToString(digest[:])})
	}
	manifest := transcript.RecordingManifest{
		FormatVersion:   transcript.RecordingManifestVersion,
		RecordingStatus: &transcript.RecordingStatus{State: transcript.RecordingStatusComplete},
		Artifacts:       hashes,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeRecordedPCMIntegrityTrace(t *testing.T, directory string) {
	t.Helper()
	trace, err := recording.NewTrace(directory, clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_send", `{"type":"session.update","session":{"model":"gpt-realtime"}}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_receive", `{"type":"session.created","session":{"model":"gpt-realtime"}}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_send", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"recorded integrity prompt"}]}}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_send", `{"type":"response.create"}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_receive", `{"type":"response.created"}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_receive", `{"type":"response.output_text.delta","delta":"recorded integrity response"}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_receive", `{"type":"response.output_text.done"}`)
	writeRecordedPCMIntegrityWire(t, trace, "provider_wire_receive", `{"type":"response.done"}`)
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRecordedPCMIntegrityWire(t *testing.T, trace *recording.Trace, kind, payload string) {
	t.Helper()
	envelope, err := json.Marshal(struct {
		MessageType int             `json:"message_type"`
		Payload     json.RawMessage `json:"payload"`
	}{MessageType: 1, Payload: json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: kind, Payload: envelope, Clean: true})
}

func copyRecordedPCMIntegrityBundle(t *testing.T, source, name string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), name)
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

func mustReadRecordedPCMIntegrityFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type completedReplayRun struct {
	stdout string
	stderr string
	audio  []byte
}

func TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "openai-complete-replay.session.json")
	writeOpenAIBarePromptCapture(t, capturePath)

	baseline := runCompletedReplay(t, capturePath, "")
	assertCompletedReplayOutput(t, "unbounded", baseline.stdout, baseline.stderr)
	if len(baseline.audio) == 0 {
		t.Fatal("unbounded replay produced an empty audio artifact")
	}

	for _, maxDuration := range []string{"100ms", "500ms", "1500ms"} {
		t.Run(maxDuration, func(t *testing.T) {
			bounded := runCompletedReplay(t, capturePath, maxDuration)
			assertCompletedReplayOutput(t, maxDuration, bounded.stdout, bounded.stderr)
			if bounded.stdout != baseline.stdout {
				t.Fatalf("bounded replay stdout differs from unbounded baseline for %s:\nbounded=%q\nbaseline=%q", maxDuration, bounded.stdout, baseline.stdout)
			}
			if !bytes.Equal(bounded.audio, baseline.audio) {
				t.Fatalf("bounded replay audio artifact differs from unbounded baseline for %s: got %d bytes, want %d", maxDuration, len(bounded.audio), len(baseline.audio))
			}
		})
	}
}

func runCompletedReplay(t *testing.T, capturePath, maxDuration string) completedReplayRun {
	t.Helper()
	artifactPath := filepath.Join(t.TempDir(), "assistant.wav")
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	var stdout, stderr bytes.Buffer
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	args := []string{
		"session",
		"--replay", capturePath,
		"--audio-out", artifactPath,
	}
	if maxDuration != "" {
		args = append(args, "--max-duration", maxDuration)
	}
	rootCmd.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute %s replay: %v\nstdout=%q\nstderr=%q", maxDurationOrUnbounded(maxDuration), err, stdout.String(), stderr.String())
	}
	audio, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read %s replay audio artifact: %v", maxDurationOrUnbounded(maxDuration), err)
	}
	return completedReplayRun{stdout: stdout.String(), stderr: stderr.String(), audio: audio}
}

func assertCompletedReplayOutput(t *testing.T, run string, stdout, stderr string) {
	t.Helper()
	if strings.Count(stdout, "[session terminal:") != 1 {
		t.Fatalf("%s replay terminal block count = %d, want 1; stdout=%q", run, strings.Count(stdout, "[session terminal:"), stdout)
	}
	for _, want := range []string{
		"recorded bare replay transcript",
		"classification=replay_complete",
		"terminal_reason=replay_complete",
		"terminal_provenance=replay",
		"output_state=complete",
		"[session replay complete]",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("%s replay stdout missing %q: %q", run, want, stdout)
		}
	}
	for _, forbidden := range []string{
		"classification=max_duration",
		"terminal_reason=max_duration",
		"output_state=partial",
		"terminal_reason=terminal_failure",
		"fatal error",
		"Usage:",
	} {
		if strings.Contains(stdout, forbidden) || strings.Contains(stderr, forbidden) {
			t.Fatalf("%s replay contains forbidden terminal evidence %q: stdout=%q stderr=%q", run, forbidden, stdout, stderr)
		}
	}
	if strings.Count(stdout, "classification=replay_complete") != 1 || strings.Count(stdout, "terminal_reason=replay_complete") != 1 || strings.Count(stdout, "[session replay complete]") != 1 {
		t.Fatalf("%s replay completion was not emitted exactly once: stdout=%q", run, stdout)
	}
	if stderr != "" {
		t.Fatalf("%s replay wrote stderr: %q", run, stderr)
	}
}

func maxDurationOrUnbounded(maxDuration string) string {
	if maxDuration == "" {
		return "unbounded"
	}
	return maxDuration
}
