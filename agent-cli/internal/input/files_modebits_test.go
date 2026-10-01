//go:build !windows

package input

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAskContentPartRejectsUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Errorf("restore attachment permissions: %v", err)
		}
	})

	_, err := LoadAskContentPart(path)
	if err == nil {
		t.Fatal("LoadAskContentPart() error = nil, want unreadable attachment rejection")
	}
	var attachmentErr *AttachmentError
	if !errors.As(err, &attachmentErr) || attachmentErr.Path != path || !strings.Contains(attachmentErr.Reason, AttachmentReasonUnreadable) {
		t.Fatalf("error = %v, want unreadable attachment for %q", err, path)
	}
}
