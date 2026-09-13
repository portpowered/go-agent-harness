package agentruntime_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
)

func TestRunSessionWithInstructionsPreservesFilesystemRootIdentity(t *testing.T) {
	missingWorkspace := filepath.Join(t.TempDir(), "missing-workspace")
	inferencer := newSessionInstructionsTestInferencer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := agentruntime.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), agentruntime.SessionRunOptions{
		ConfigDir:         missingWorkspace,
		SessionInferencer: inferencer,
	}, "")
	if err == nil {
		t.Fatal("RunSessionWithInstructions returned nil for an invalid filesystem root")
	}
	if !errors.Is(err, tools.ErrInvalidFilesystemRoot) {
		t.Fatalf("RunSessionWithInstructions error = %v, want errors.Is(ErrInvalidFilesystemRoot)", err)
	}
	if inferencer.wasConnected() {
		t.Fatal("invalid filesystem root connected a session before returning its error")
	}
}
