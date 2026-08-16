package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func toolTextResult(msgs []messages.Message, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if len(msgs) != 1 {
		return "", fmt.Errorf("expected one tool message, got %d", len(msgs))
	}
	return msgs[0].TextContent(), nil
}

func requireToolText(t *testing.T, msgs []messages.Message, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("tool returned error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected one tool message, got %d", len(msgs))
	}
	if got := msgs[0].TextContent(); got != want {
		t.Fatalf("tool message = %q, want %q", got, want)
	}
	if got := msgs[0].TextContent(); got == "" {
		t.Fatal("tool message must not be empty")
	}
}

func sha256File(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q for hash: %v", path, err)
	}
	return sha256.Sum256(content)
}

func TestS4_EditErrorPathsAndAtomicity(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.txt")
	if err := os.WriteFile(target, []byte("prefix NEEDLE suffix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(workspace, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(workspace, "missing.txt")
	permissionPath := filepath.Join(workspace, "permission.txt")
	if err := os.WriteFile(permissionPath, []byte("permission NEEDLE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside NEEDLE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repeated := filepath.Join(workspace, "repeated.txt")
	if err := os.WriteFile(repeated, []byte("NEEDLE and NEEDLE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	newTextMissing := func() (string, error) {
		tool := NewEditFileTool("", false)
		msgs, err := tool.Execute(context.Background(), map[string]any{
			"path":     target,
			"old_text": "NEEDLE",
		})
		return toolTextResult(msgs, err)
	}

	tests := []struct {
		name          string
		run           func() (string, error)
		wantMessage   string
		assertError   func(*testing.T, error)
		hashPath      string
		skipOnWindows bool
	}{
		{
			name: "path does not exist",
			run: func() (string, error) {
				err := editFile(&hostFs{}, missing, "NEEDLE", "changed")
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "failed to read file: file not found",
			assertError: func(t *testing.T, err error) {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("error identity = %T %v; want errors.Is(fs.ErrNotExist)", err, err)
				}
			},
		},
		{
			name: "directory where file is expected",
			run: func() (string, error) {
				err := editFile(&hostFs{}, directory, "NEEDLE", "changed")
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "failed to read file:",
			assertError: func(t *testing.T, err error) {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("error identity = %T %v; want errors.As(*os.PathError)", err, err)
				}
			},
		},
		{
			name: "permission denied",
			run: func() (string, error) {
				if err := os.Chmod(permissionPath, 0o000); err != nil {
					return "", err
				}
				err := editFile(&hostFs{}, permissionPath, "NEEDLE", "changed")
				restoreErr := os.Chmod(permissionPath, 0o644)
				if err == nil {
					err = restoreErr
				}
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "failed to read file: access denied",
			assertError: func(t *testing.T, err error) {
				if !errors.Is(err, fs.ErrPermission) {
					t.Fatalf("error identity = %T %v; want errors.Is(fs.ErrPermission)", err, err)
				}
			},
			hashPath:      permissionPath,
			skipOnWindows: true,
		},
		{
			name: "target outside allowed root",
			run: func() (string, error) {
				err := editFile(&sandboxFs{workspace: workspace}, outside, "NEEDLE", "changed")
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "path escapes workspace: " + outside,
			assertError: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("outside-root edit unexpectedly returned no error")
				}
			},
			hashPath: outside,
		},
		{
			name: "absent match text",
			run: func() (string, error) {
				err := editFile(&hostFs{}, target, "ABSENT", "changed")
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "old_text not found in file. Make sure it matches exactly",
			assertError: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("absent-match edit unexpectedly returned no error")
				}
			},
			hashPath: target,
		},
		{
			name: "multiply occurring match text",
			run: func() (string, error) {
				err := editFile(&hostFs{}, repeated, "NEEDLE", "changed")
				if err == nil {
					return "", nil
				}
				return err.Error(), err
			},
			wantMessage: "old_text appears 2 times. Please provide more context to make it unique",
			assertError: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("repeated-match edit unexpectedly returned no error")
				}
			},
			hashPath: repeated,
		},
		{
			name:        "empty replacement input",
			run:         newTextMissing,
			wantMessage: "new_text is required",
			assertError: func(t *testing.T, err error) {
				if err != nil {
					t.Fatalf("tool returned an unexpected Go error: %v", err)
				}
			},
			hashPath: target,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipOnWindows && runtime.GOOS == "windows" {
				t.Skip("Windows chmod does not reliably deny reads without ACL changes")
			}
			before := [sha256.Size]byte{}
			if tt.hashPath != "" {
				before = sha256File(t, tt.hashPath)
			}

			gotMessage, err := tt.run()
			if tt.name == "permission denied" && !errors.Is(err, fs.ErrPermission) {
				t.Skipf("chmod did not produce a permission error on %s: %v", runtime.GOOS, err)
			}
			tt.assertError(t, err)
			if !strings.Contains(gotMessage, tt.wantMessage) {
				t.Fatalf("error message = %q, want substring %q", gotMessage, tt.wantMessage)
			}
			if tt.hashPath != "" {
				after := sha256File(t, tt.hashPath)
				if before != after {
					t.Fatalf("failed edit changed %q: SHA-256 before=%x after=%x", tt.hashPath, before, after)
				}
			}
		})
	}
}

func TestEditAndAppendTools_SuccessAndArgumentContracts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "document.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	edit := NewEditFileTool("", false)
	msgs, err := edit.Execute(ctx, map[string]any{
		"path":     path,
		"old_text": "before",
		"new_text": "after",
	})
	requireToolText(t, msgs, err, "File edited: "+path)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "after\n" {
		t.Fatalf("edited content = %q, want %q", got, "after\n")
	}

	appendTool := NewAppendFileTool("", false)
	msgs, err = appendTool.Execute(ctx, map[string]any{"path": path, "content": "appended"})
	requireToolText(t, msgs, err, "Appended to "+path)
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "after\nappended" {
		t.Fatalf("appended content = %q, want %q", got, "after\nappended")
	}

	created := filepath.Join(dir, "created.txt")
	msgs, err = appendTool.Execute(ctx, map[string]any{"path": created, "content": "created"})
	requireToolText(t, msgs, err, "Appended to "+created)
	content, err = os.ReadFile(created)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "created" {
		t.Fatalf("newly appended content = %q, want %q", got, "created")
	}

	argumentCases := []struct {
		name string
		tool Tool
		args map[string]any
		want string
	}{
		{name: "edit path", tool: edit, args: map[string]any{}, want: "path is required"},
		{name: "edit old text", tool: edit, args: map[string]any{"path": path}, want: "old_text is required"},
		{name: "edit new text", tool: edit, args: map[string]any{"path": path, "old_text": "after"}, want: "new_text is required"},
		{name: "append path", tool: appendTool, args: map[string]any{}, want: "path is required"},
		{name: "append content", tool: appendTool, args: map[string]any{"path": path}, want: "content is required"},
	}
	for _, tt := range argumentCases {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := tt.tool.Execute(ctx, tt.args)
			requireToolText(t, msgs, err, tt.want)
		})
	}

}
