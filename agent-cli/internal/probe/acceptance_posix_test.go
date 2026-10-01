//go:build !windows

package probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// TestLiveTransportDoesNotForwardParentWorkspaceEnvironment is the one live
// process test: the child writes its objective artifact and report, so it
// also proves the live transport captures a real process end to end.
func TestLiveTransportDoesNotForwardParentWorkspaceEnvironment(t *testing.T) {
	workspaceSentinel := filepath.Join(t.TempDir(), "parent-workspace-sentinel")
	t.Setenv("GITHUB_WORKSPACE", workspaceSentinel)
	t.Setenv("OLDPWD", workspaceSentinel)

	path := filepath.Join(t.TempDir(), "blind-environment-agent.sh")
	script := `#!/bin/sh
if [ -n "${GITHUB_WORKSPACE:-}" ] || [ -n "${OLDPWD:-}" ]; then
  printf '%s\n' 'parent workspace environment leaked' >&2
  exit 7
fi
printf '%s\n' 'blind environment attained' > result.txt
printf '%s\n' '{"claimed_success":true,"objective_artifact_path":"result.txt","checked_claim":"blind environment attained","subjective_rating":"easy"}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write process fixture: %v", err)
	}

	runner := NewLiveRunner(recordedArtifactVerifier())
	runner.ArtifactRoot = t.TempDir()
	verdict, err := runner.Run(context.Background(), loopprobe.AcceptanceInput{BinaryPath: path, Goal: "Run without parent workspace context"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !verdict.Pass || !verdict.ObjectiveEvidence.Verified {
		t.Fatalf("live verdict = %+v, want pass without leaked parent environment", verdict)
	}
	if data, readErr := os.ReadFile(filepath.Join(verdict.RunDirectory, "stderr.txt")); readErr != nil {
		t.Fatalf("read stderr artifact: %v", readErr)
	} else if strings.Contains(string(data), "parent workspace environment leaked") {
		t.Fatalf("child observed parent workspace environment: %q", data)
	}
}
