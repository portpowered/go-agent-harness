package agentruntime_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
)

// TestC110InstructionResolutionRejectsMalformedAndOversizedFilesBeforePlan
// pins the causal boundary: the CLI adapter must surface canonical resolution
// failures before planning can construct or run a provider session.
func TestC110InstructionResolutionRejectsMalformedAndOversizedFilesBeforePlan(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		cause error
	}{
		{name: "malformed NUL", data: []byte{'a', 0, 'b'}, cause: sessioninstructions.ErrMalformedInstruction},
		{name: "oversized", data: []byte(strings.Repeat("x", sessioninstructions.MaxInstructionBytes+1)), cause: sessioninstructions.ErrInstructionTooLarge},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			workspaceDir := t.TempDir()
			promptPath := filepath.Join(workspaceDir, "prompt.md")
			if err := os.WriteFile(promptPath, testCase.data, 0o600); err != nil {
				t.Fatalf("write prompt: %v", err)
			}
			err := agentruntime.RunSessionWithInstructions(context.Background(), io.Discard, agentruntime.SessionRunOptions{
				ConfigDir:    workspaceDir,
				ModelCatalog: testModelCatalog(),
			}, promptPath)
			if err == nil || !errors.Is(err, testCase.cause) {
				t.Fatalf("resolveSessionInstructions() = %v, want cause %v", err, testCase.cause)
			}
			var resolutionErr *sessioninstructions.ResolutionError
			if !errors.As(err, &resolutionErr) || resolutionErr.Phase != sessioninstructions.PhasePromptRead {
				t.Fatalf("resolution error = %T/%v, want prompt-read attribution", err, err)
			}
		})
	}
}

func TestC110LegacyInstructionFileIsThinDeprecatedAdapter(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	legacyPath := filepath.Join(filepath.Dir(sourcePath), "session_instructions.go")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy adapter: %v", err)
	}
	if lines := len(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")); lines > 87 {
		t.Fatalf("legacy session_instructions.go has %d lines, want <= 87", lines)
	}
	if !strings.Contains(string(data), "sessioninstructionswire.NewInstructionService") {
		t.Fatal("legacy adapter does not route through the dedicated instruction Wire")
	}
	if strings.Contains(string(data), "Tool-grounding requirements:") || strings.Contains(string(data), "os.ReadFile") {
		t.Fatal("legacy adapter retains policy text or unbounded file loading")
	}
}
