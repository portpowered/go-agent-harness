package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFilesystemPolicyHostilePathBehavior(t *testing.T) {
	primary := t.TempDir()
	outside := t.TempDir()
	policy, err := NewFilesystemPolicy(primary)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}

	readTool := NewReadFileToolWithPolicy(policy)
	listTool := NewListDirToolWithPolicy(policy)
	writeTool := NewWriteFileToolWithPolicy(policy)
	editTool := NewEditFileToolWithPolicy(policy)
	appendTool := NewAppendFileToolWithPolicy(policy)

	t.Run("unicode spaces long names and literal tilde remain usable", func(t *testing.T) {
		longName := strings.Repeat("long-", 28) + "終.txt"
		paths := []string{
			filepath.Join("unicode ✓", "space name.txt"),
			longName,
			filepath.Join("~", "literal tilde.txt"),
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				if got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{
					"path": path, "content": "original",
				}), nil, "File written"); got == "" {
					t.Fatal("write returned an empty result")
				}
				if got := requireToolTextContains(t, mustToolExecute(t, readTool, map[string]any{"path": path}), nil, "original"); got == "" {
					t.Fatal("read returned an empty result")
				}
				requireToolTextContains(t, mustToolExecute(t, editTool, map[string]any{
					"path": path, "old_text": "original", "new_text": "edited",
				}), nil, "File edited")
				requireToolTextContains(t, mustToolExecute(t, appendTool, map[string]any{
					"path": path, "content": "-appended",
				}), nil, "Appended")
				absolute := filepath.Join(primary, path)
				if got, err := os.ReadFile(absolute); err != nil || string(got) != "edited-appended" {
					t.Fatalf("hostile-valid path content = %q, %v; want edited-appended", got, err)
				}
				parent := filepath.Dir(absolute)
				if got := requireToolTextContains(t, mustToolExecute(t, listTool, map[string]any{"path": parent}), nil, filepath.Base(absolute)); got == "" {
					t.Fatal("listing returned an empty result")
				}
			})
		}
	})

	t.Run("traversal is refused without a side effect", func(t *testing.T) {
		traversal := filepath.Join("..", filepath.Base(outside), "not-created", "escape.txt")
		got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{
			"path": traversal, "content": "must not write",
		}), nil, "path escapes workspace")
		if strings.Contains(got, "must not write") || strings.Contains(got, "File written") {
			t.Fatalf("traversal result falsely reports success or echoes content: %q", got)
		}
		if _, err := os.Stat(filepath.Join(outside, "not-created")); !os.IsNotExist(err) {
			t.Fatalf("traversal created outside parent: %v", err)
		}
	})

	t.Run("conflicting file and directory shapes are reported", func(t *testing.T) {
		fileShape := filepath.Join(primary, "already-a-file")
		if err := os.WriteFile(fileShape, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		fileChild := filepath.Join("already-a-file", "child.txt")
		got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{
			"path": fileChild, "content": "must not write",
		}), nil, "failed to create parent directories")
		if strings.Contains(got, "File written") {
			t.Fatalf("file-as-directory result falsely reports success: %q", got)
		}
		if got, err := os.ReadFile(fileShape); err != nil || string(got) != "sentinel" {
			t.Fatalf("file-as-directory changed sentinel = %q, %v", got, err)
		}

		directoryShape := filepath.Join(primary, "already-a-directory")
		if err := os.Mkdir(directoryShape, 0o755); err != nil {
			t.Fatal(err)
		}
		got = requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{
			"path": "already-a-directory", "content": "must not replace directory",
		}), nil, "failed to rename temp file over target")
		if strings.Contains(got, "File written") {
			t.Fatalf("directory-as-file result falsely reports success: %q", got)
		}
		if info, err := os.Stat(directoryShape); err != nil || !info.IsDir() {
			t.Fatalf("directory-as-file replaced directory: info=%v err=%v", info, err)
		}
	})

	t.Run("read-only parent is reported without creating a target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod does not reliably deny directory writes on Windows")
		}
		readOnly := filepath.Join(primary, "read-only")
		if err := os.Mkdir(readOnly, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
		probe := filepath.Join(readOnly, "permission-probe")
		probeFile, probeErr := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if probeErr == nil {
			_ = probeFile.Close()
			_ = os.Remove(probe)
			t.Skip("test process can write a read-only directory")
		}
		if !errors.Is(probeErr, os.ErrPermission) && !os.IsPermission(probeErr) {
			t.Skipf("read-only probe produced a platform-specific error: %v", probeErr)
		}

		target := filepath.Join("read-only", "not-created.txt")
		got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{
			"path": target, "content": "must not write",
		}), nil, "failed to write to temp file")
		if strings.Contains(got, "File written") || strings.Contains(got, "must not write") {
			t.Fatalf("read-only result falsely reports success or echoes content: %q", got)
		}
		if _, err := os.Stat(filepath.Join(primary, target)); !os.IsNotExist(err) {
			t.Fatalf("read-only target exists after refusal: %v", err)
		}
	})
}
