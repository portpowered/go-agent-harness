package filesystem

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
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
	fixture := newEditAtomicityFixture(t)
	runEditAtomicityCases(t, fixture)
}

type editAtomicityFixture struct {
	workspace      string
	target         string
	directory      string
	missing        string
	permissionPath string
	outside        string
	repeated       string
}

func newEditAtomicityFixture(t *testing.T) editAtomicityFixture {
	t.Helper()
	workspace := t.TempDir()
	fixture := editAtomicityFixture{
		workspace:      workspace,
		target:         filepath.Join(workspace, "target.txt"),
		directory:      filepath.Join(workspace, "directory"),
		missing:        filepath.Join(workspace, "missing.txt"),
		permissionPath: filepath.Join(workspace, "permission.txt"),
		outside:        filepath.Join(t.TempDir(), "outside.txt"),
		repeated:       filepath.Join(workspace, "repeated.txt"),
	}
	writeEditFixtureFile(t, fixture.target, "prefix NEEDLE suffix\n")
	if err := os.Mkdir(fixture.directory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEditFixtureFile(t, fixture.permissionPath, "permission NEEDLE\n")
	writeEditFixtureFile(t, fixture.outside, "outside NEEDLE\n")
	writeEditFixtureFile(t, fixture.repeated, "NEEDLE and NEEDLE\n")
	return fixture
}

func writeEditFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type editAtomicityCase struct {
	name          string
	run           func() (string, error)
	wantMessage   string
	assertError   func(*testing.T, error)
	hashPath      string
	skipOnWindows bool
}

func editAtomicityCases(f editAtomicityFixture) []editAtomicityCase {
	return append(editAtomicityMissingCases(f), editAtomicityMutationCases(f)...)
}

func editAtomicityMissingCases(f editAtomicityFixture) []editAtomicityCase {
	return []editAtomicityCase{
		{name: "path does not exist", run: func() (string, error) { return editCase(&hostFs{}, f.missing, "NEEDLE", "changed") }, wantMessage: "failed to read file: file not found", assertError: requireNotExistError},
		{name: "directory where file is expected", run: func() (string, error) { return editCase(&hostFs{}, f.directory, "NEEDLE", "changed") }, wantMessage: "failed to read file:", assertError: requirePathError},
		{name: "permission denied", run: func() (string, error) { return editPermissionCase(f.permissionPath) }, wantMessage: "failed to read file: access denied", assertError: requirePermissionError, hashPath: f.permissionPath, skipOnWindows: true},
		{name: "target outside allowed root", run: func() (string, error) {
			return editCase(&sandboxFs{workspace: f.workspace}, f.outside, "NEEDLE", "changed")
		}, wantMessage: "path escapes workspace: " + f.outside, assertError: requireAnyError, hashPath: f.outside},
	}
}

func editAtomicityMutationCases(f editAtomicityFixture) []editAtomicityCase {
	return []editAtomicityCase{
		{name: "absent match text", run: func() (string, error) { return editCase(&hostFs{}, f.target, "ABSENT", "changed") }, wantMessage: "old_text not found in file. Make sure it matches exactly", assertError: requireAnyError, hashPath: f.target},
		{name: "multiply occurring match text", run: func() (string, error) { return editCase(&hostFs{}, f.repeated, "NEEDLE", "changed") }, wantMessage: "old_text appears 2 times. Please provide more context to make it unique", assertError: requireAnyError, hashPath: f.repeated},
		{name: "empty replacement input", run: func() (string, error) {
			return toolTextResult(NewEditFileTool("", false).Execute(context.Background(), map[string]any{"path": f.target, "old_text": "NEEDLE"}))
		}, wantMessage: "new_text is required", assertError: requireNoError, hashPath: f.target},
	}
}

func editCase(fs fileSystem, path, oldText, newText string) (string, error) {
	err := editFile(fs, path, oldText, newText)
	if err == nil {
		return "", nil
	}
	return err.Error(), err
}

func editPermissionCase(path string) (string, error) {
	if err := os.Chmod(path, 0o000); err != nil {
		return "", err
	}
	err := editFile(&hostFs{}, path, "NEEDLE", "changed")
	restoreErr := os.Chmod(path, 0o644)
	if err == nil {
		err = restoreErr
	}
	if err == nil {
		return "", nil
	}
	return err.Error(), err
}

func requireNotExistError(t *testing.T, err error) {
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error identity = %T %v; want errors.Is(fs.ErrNotExist)", err, err)
	}
}

func requirePathError(t *testing.T, err error) {
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error identity = %T %v; want errors.As(*os.PathError)", err, err)
	}
}

func requirePermissionError(t *testing.T, err error) {
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error identity = %T %v; want errors.Is(fs.ErrPermission)", err, err)
	}
}

func requireAnyError(t *testing.T, err error) {
	if err == nil {
		t.Fatal("edit unexpectedly returned no error")
	}
}

func requireNoError(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("tool returned an unexpected Go error: %v", err)
	}
}

func runEditAtomicityCases(t *testing.T, f editAtomicityFixture) {
	t.Helper()
	for _, tc := range editAtomicityCases(f) {
		t.Run(tc.name, func(t *testing.T) {
			runEditAtomicityCase(t, tc)
		})
	}
}

func runEditAtomicityCase(t *testing.T, tc editAtomicityCase) {
	t.Helper()
	if tc.skipOnWindows && runtime.GOOS == windowsPlatform {
		t.Skip("Windows chmod does not reliably deny reads without ACL changes")
	}
	before := [sha256.Size]byte{}
	if tc.hashPath != "" {
		before = sha256File(t, tc.hashPath)
	}
	gotMessage, err := tc.run()
	if tc.name == "permission denied" && !errors.Is(err, fs.ErrPermission) {
		t.Skipf("chmod did not produce a permission error on %s: %v", runtime.GOOS, err)
	}
	tc.assertError(t, err)
	if !strings.Contains(gotMessage, tc.wantMessage) {
		t.Fatalf("error message = %q, want substring %q", gotMessage, tc.wantMessage)
	}
	if tc.hashPath != "" {
		assertEditHashUnchanged(t, tc.hashPath, before)
	}
}

func assertEditHashUnchanged(t *testing.T, path string, before [sha256.Size]byte) {
	t.Helper()
	after := sha256File(t, path)
	if before != after {
		t.Fatalf("failed edit changed %q: SHA-256 before=%x after=%x", path, before, after)
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
		tool core.Tool
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
