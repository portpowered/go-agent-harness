//go:build !windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	acceptanceprobe "github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestProbeAcceptanceLiveTimeoutStopsHangingBinary(t *testing.T) {
	runner := acceptanceprobe.NewLiveRunner(nil)
	runner.ArtifactRoot = t.TempDir()
	root := newTestRootCommandWithAcceptance(runner, 40*time.Millisecond)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"probe", "acceptance", "sleep", "60"})

	started := time.Now()
	err := root.ExecuteContext(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hanging binary took %s to stop", elapsed)
	}
	if err == nil || !errors.Is(err, acceptanceprobe.ErrProbeAgentStuck) {
		t.Fatalf("error = %v, want stuck error", err)
	}
	var verdict loopprobe.AcceptanceVerdict
	if decodeErr := json.Unmarshal(stdout.Bytes(), &verdict); decodeErr != nil {
		t.Fatalf("decode verdict %q: %v", stdout.String(), decodeErr)
	}
	if verdict.Pass || verdict.TerminalState != loopprobe.AcceptanceStuckPendingDownstream {
		t.Fatalf("verdict = %+v, want non-passing stuck verdict", verdict)
	}
}
