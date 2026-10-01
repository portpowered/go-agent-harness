//go:build !windows

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
)

func TestAskCommandRejectsUnreadableAttachmentBeforeInference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, func() error { return os.Chmod(path, 0o600) })

	inf := &askTestInferencer{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	err := runAskTestCommand(t, []string{"describe this", path}, strings.NewReader(""), inf, stdout, stderr)
	if err == nil {
		t.Fatal("expected unreadable attachment error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), input.AttachmentReasonUnreadable) {
		t.Fatalf("error = %q, want supplied path and unreadable-file reason", err)
	}
	if strings.Count(err.Error(), path) != 1 {
		t.Errorf("error = %q, want supplied path exactly once", err)
	}
	inf.mu.Lock()
	calls := inf.inferCalls
	inf.mu.Unlock()
	if calls != 0 {
		t.Fatalf("inference calls = %d, want zero for rejected attachment", calls)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout/stderr = %q/%q, want no command output before final rendering", stdout, stderr)
	}
}
