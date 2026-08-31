package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

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
