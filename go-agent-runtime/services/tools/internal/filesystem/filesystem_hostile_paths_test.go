package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
)

const editedAppendedContent = "edited-appended"

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
		assertHostileValidPaths(t, primary, writeTool, readTool, editTool, appendTool, listTool)
	})
	t.Run("traversal is refused without a side effect", func(t *testing.T) {
		assertHostileTraversal(t, outside, writeTool)
	})
	t.Run("conflicting file and directory shapes are reported", func(t *testing.T) {
		assertHostileShapes(t, primary, writeTool)
	})
	if permissionBitsDenyAccess() {
		t.Run("read-only parent is reported without creating a target", func(t *testing.T) {
			assertHostileReadOnlyParent(t, primary, writeTool)
		})
	}
}

func assertHostileValidPaths(t *testing.T, primary string, writeTool, readTool, editTool, appendTool, listTool core.Tool) {
	t.Helper()
	paths := []string{filepath.Join("unicode ✓", "space name.txt"), strings.Repeat("long-", 28) + "終.txt", filepath.Join("~", "literal tilde.txt")}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			assertHostilePathLifecycle(t, primary, path, writeTool, readTool, editTool, appendTool, listTool)
		})
	}
}

func assertHostilePathLifecycle(t *testing.T, primary, path string, writeTool, readTool, editTool, appendTool, listTool core.Tool) {
	t.Helper()
	if got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": path, "content": "original"}), nil, "File written"); got == "" {
		t.Fatal("write returned an empty result")
	}
	if got := requireToolTextContains(t, mustToolExecute(t, readTool, map[string]any{"path": path}), nil, "original"); got == "" {
		t.Fatal("read returned an empty result")
	}
	requireToolTextContains(t, mustToolExecute(t, editTool, map[string]any{"path": path, "old_text": "original", "new_text": "edited"}), nil, "File edited")
	requireToolTextContains(t, mustToolExecute(t, appendTool, map[string]any{"path": path, "content": "-appended"}), nil, "Appended")
	absolute := filepath.Join(primary, path)
	if got, err := os.ReadFile(absolute); err != nil || string(got) != editedAppendedContent {
		t.Fatalf("hostile-valid path content = %q, %v; want edited-appended", got, err)
	}
	requireToolTextContains(t, mustToolExecute(t, listTool, map[string]any{"path": filepath.Dir(absolute)}), nil, filepath.Base(absolute))
}

func assertHostileTraversal(t *testing.T, outside string, writeTool core.Tool) {
	t.Helper()
	traversal := filepath.Join("..", filepath.Base(outside), "not-created", "escape.txt")
	got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": traversal, "content": "must not write"}), nil, "path escapes workspace")
	if strings.Contains(got, "must not write") || strings.Contains(got, "File written") {
		t.Fatalf("traversal result falsely reports success or echoes content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "not-created")); !os.IsNotExist(err) {
		t.Fatalf("traversal created outside parent: %v", err)
	}
}

func assertHostileShapes(t *testing.T, primary string, writeTool core.Tool) {
	t.Helper()
	fileShape := filepath.Join(primary, "already-a-file")
	if err := os.WriteFile(fileShape, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": filepath.Join("already-a-file", "child.txt"), "content": "must not write"}), nil, "failed to create parent directories")
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
	got = requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": "already-a-directory", "content": "must not replace directory"}), nil, "failed to rename temp file over target")
	if strings.Contains(got, "File written") {
		t.Fatalf("directory-as-file result falsely reports success: %q", got)
	}
	if info, err := os.Stat(directoryShape); err != nil || !info.IsDir() {
		t.Fatalf("directory-as-file replaced directory: info=%v err=%v", info, err)
	}
}

func assertHostileReadOnlyParent(t *testing.T, primary string, writeTool core.Tool) {
	t.Helper()
	readOnly := filepath.Join(primary, "read-only")
	if err := os.Mkdir(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(readOnly, 0o755); err != nil {
			t.Errorf("restore read-only directory permissions: %v", err)
		}
	})
	target := filepath.Join("read-only", "not-created.txt")
	got := requireToolTextContains(t, mustToolExecute(t, writeTool, map[string]any{"path": target, "content": "must not write"}), nil, "failed to write to temp file")
	if strings.Contains(got, "File written") || strings.Contains(got, "must not write") {
		t.Fatalf("read-only result falsely reports success or echoes content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(primary, target)); !os.IsNotExist(err) {
		t.Fatalf("read-only target exists after refusal: %v", err)
	}
}
