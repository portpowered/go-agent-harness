package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestFilesystemPolicyReturnsStableRefusalForEveryFilesystemOperation(t *testing.T) {
	primary := t.TempDir()
	outside := t.TempDir()
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("SENTINEL-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := NewFilesystemPolicy(primary)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}

	deniedParent := filepath.Join(outside, "not-created", "nested")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	cases := []struct {
		name      string
		operation string
		tool      core.Tool
		path      string
		args      map[string]any
	}{
		{name: "read", operation: "read_file", tool: NewReadFileToolWithPolicy(policy), path: sentinelPath, args: map[string]any{"path": sentinelPath}},
		{name: "list", operation: "list_dir", tool: NewListDirToolWithPolicy(policy), path: outside, args: map[string]any{"path": outside}},
		{name: "write", operation: "write_file", tool: NewWriteFileToolWithPolicy(policy), path: deniedTarget, args: map[string]any{"path": deniedTarget, "content": "MUST-NOT-WRITE"}},
		{name: "edit", operation: "edit_file", tool: NewEditFileToolWithPolicy(policy), path: sentinelPath, args: map[string]any{"path": sentinelPath, "old_text": "SENTINEL", "new_text": "changed"}},
		{name: "append", operation: "append_file", tool: NewAppendFileToolWithPolicy(policy), path: sentinelPath, args: map[string]any{"path": sentinelPath, "content": "MUST-NOT-APPEND"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFilesystemRefusalCase(t, tc, policy)
		})
	}

	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "SENTINEL-CONTENT" {
		t.Fatalf("outside sentinel = %q, %v; want unchanged", got, err)
	}
	if _, err := os.Stat(deniedParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied parent = %v, want absent", err)
	}
}

func assertFilesystemRefusalCase(t *testing.T, tc struct {
	name      string
	operation string
	tool      core.Tool
	path      string
	args      map[string]any
}, policy *FilesystemPolicy) {
	t.Helper()
	msgs, err := tc.tool.Execute(context.Background(), tc.args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != messages.RoleTool {
		t.Fatalf("messages = %#v, want one tool message", msgs)
	}
	refusal, err := DecodeFilesystemRefusal([]byte(msgs[0].TextContent()))
	if err != nil {
		t.Fatalf("decode refusal: %v; content=%q", err, msgs[0].TextContent())
	}
	assertFilesystemRefusalIdentity(t, refusal, tc, policy)
}

func assertFilesystemRefusalIdentity(t *testing.T, refusal FilesystemRefusal, tc struct {
	name      string
	operation string
	tool      core.Tool
	path      string
	args      map[string]any
}, policy *FilesystemPolicy) {
	t.Helper()
	if refusal.Operation != tc.operation || refusal.Path != tc.path || refusal.WorkDir != policy.PrimaryRoot() {
		t.Fatalf("refusal identity = %#v, want operation=%q path=%q workdir=%q", refusal, tc.operation, tc.path, policy.PrimaryRoot())
	}
	if refusal.Reason != FilesystemRefusalOutsidePermittedRoots || refusal.OK || refusal.Status != FilesystemRefusalStatus {
		t.Fatalf("refusal classification = %#v, want outside-permitted-roots refusal", refusal)
	}
	if refusal.Message != "path escapes workspace: "+tc.path || !strings.Contains(refusal.Remediation, "--allow-path") {
		t.Fatalf("refusal guidance = %#v, want safe path message and allow-path remediation", refusal)
	}
	if strings.Contains(refusal.Message, "MUST-NOT") || strings.Contains(refusal.Message, "SENTINEL-CONTENT") {
		t.Fatalf("refusal leaked request or protected content: %#v", refusal)
	}
}

func TestFilesystemPolicyReturnsSensitiveRefusalWithoutProtectedPathOrContent(t *testing.T) {
	primary := t.TempDir()
	systemRoot, systemFile := protectedSystemFixture(t)
	policy, err := NewFilesystemPolicy(primary, systemRoot)
	if err != nil {
		t.Fatalf("NewFilesystemPolicy: %v", err)
	}

	msgs, err := NewReadFileToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": systemFile})
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	refusal, err := DecodeFilesystemRefusal([]byte(msgs[0].TextContent()))
	if err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Operation != "read_file" || refusal.Reason != FilesystemRefusalSensitiveRead || refusal.Path != filesystemProtectedPath {
		t.Fatalf("sensitive refusal = %#v, want redacted protected-read identity", refusal)
	}
	if strings.Contains(msgs[0].TextContent(), filepath.Base(systemFile)) || strings.Contains(msgs[0].TextContent(), "secret") {
		t.Fatalf("sensitive refusal leaked protected pathname/content: %q", msgs[0].TextContent())
	}
	if strings.Contains(refusal.Remediation, "--allow-path") && !strings.Contains(refusal.Remediation, "cannot authorize") {
		t.Fatalf("sensitive remediation incorrectly suggests allow-path can authorize the read: %q", refusal.Remediation)
	}

	listMsgs, err := NewListDirToolWithPolicy(policy).Execute(context.Background(), map[string]any{"path": systemRoot})
	if err != nil {
		t.Fatalf("list Execute: %v", err)
	}
	listRefusal, err := DecodeFilesystemRefusal([]byte(listMsgs[0].TextContent()))
	if err != nil || listRefusal.Reason != FilesystemRefusalSensitiveRead {
		t.Fatalf("sensitive list refusal = %#v, %v", listRefusal, err)
	}
}

func TestFilesystemPolicyReturnsInvalidScopeRefusalWhenPolicyIsMissing(t *testing.T) {
	const path = "should-not-exist.txt"
	msgs, err := NewWriteFileToolWithPolicy(nil).Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "MUST-NOT-WRITE",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one refusal", msgs)
	}
	refusal, err := DecodeFilesystemRefusal([]byte(msgs[0].TextContent()))
	if err != nil {
		t.Fatalf("decode refusal: %v; content=%q", err, msgs[0].TextContent())
	}
	if refusal.Operation != "write_file" || refusal.Path != path || refusal.Reason != FilesystemRefusalInvalidScope || refusal.WorkDir == "" {
		t.Fatalf("invalid-scope refusal = %#v", refusal)
	}
	if strings.Contains(msgs[0].TextContent(), "MUST-NOT-WRITE") {
		t.Fatalf("invalid-scope refusal leaked mutation content: %q", msgs[0].TextContent())
	}
}

func TestPolicyBackedReadImageWithoutPolicyRefusesBeforePreparer(t *testing.T) {
	called := false
	tool := NewReadImageToolWithPolicy(nil, func([]string) ([]messages.ImagePart, error) {
		called = true
		return []messages.ImagePart{{Bytes: []byte("MUST-NOT-READ"), MediaType: imagePNGMediaType}}, nil
	})
	msgs, err := tool.Execute(context.Background(), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called {
		t.Fatal("invalid-scope read_image invoked the preparer")
	}
	refusal, ok := FilesystemRefusalFromContent(msgs[0].TextContent())
	if !ok || refusal.Operation != ReadImageToolID || refusal.Reason != FilesystemRefusalInvalidScope {
		t.Fatalf("invalid-scope read_image refusal = %#v, recognized=%v", refusal, ok)
	}
	if strings.Contains(msgs[0].TextContent(), "MUST-NOT-READ") {
		t.Fatalf("invalid-scope read_image leaked preparer content: %q", msgs[0].TextContent())
	}
}

func TestFilesystemRefusalContentRecognizesReadImageWrapper(t *testing.T) {
	refusal := newFilesystemRefusal("read_image", "[protected path]", "/work", FilesystemRefusalSensitiveRead)
	encoded, err := json.Marshal(ReadImageResult{
		Version: ReadImageResultVersion,
		Status:  ReadImageResultStatusError,
		Error:   ErrFilesystemAccessDenied.Error(),
		Refusal: &refusal,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := FilesystemRefusalFromContent(string(encoded))
	if !ok || got != refusal {
		t.Fatalf("recognized refusal = %#v, %v; want %#v", got, ok, refusal)
	}
}

func TestFilesystemPolicyWriteRootsConstrainAllWriteLikeTools(t *testing.T) {
	fixture := newWriteRootsFixture(t)
	tools := newWriteRootTools(fixture.policy)
	assertAdditionalRootWrites(t, fixture, tools)
	assertPrimaryRelativeWrite(t, fixture, tools.write)
	assertOutsideRootWrites(t, fixture, tools)
	assertTraversalWrite(t, fixture, tools.write)
	assertExternalSymlinkWrite(t, fixture, tools.write)
}

type writeRootsFixture struct {
	primary, additional, outside string
	policy                       *FilesystemPolicy
}

func newWriteRootsFixture(t *testing.T) writeRootsFixture {
	t.Helper()
	fixture := writeRootsFixture{primary: t.TempDir(), additional: t.TempDir(), outside: t.TempDir()}
	var err error
	fixture.policy, err = NewFilesystemPolicy(fixture.primary, fixture.additional)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	return fixture
}

type writeRootTools struct {
	write, edit, append core.Tool
}

func newWriteRootTools(policy *FilesystemPolicy) writeRootTools {
	return writeRootTools{
		write:  NewWriteFileToolWithPolicy(policy),
		edit:   NewEditFileToolWithPolicy(policy),
		append: NewAppendFileToolWithPolicy(policy),
	}
}

func assertAdditionalRootWrites(t *testing.T, f writeRootsFixture, tools writeRootTools) {
	t.Helper()
	target := filepath.Join(f.additional, "nested", "allowed.txt")
	msgs, err := tools.write.Execute(context.Background(), map[string]any{"path": target, "content": "initial"})
	requireToolText(t, msgs, err, "File written: "+target)
	msgs, err = tools.edit.Execute(context.Background(), map[string]any{"path": target, "old_text": "initial", "new_text": "edited"})
	requireToolText(t, msgs, err, "File edited: "+target)
	msgs, err = tools.append.Execute(context.Background(), map[string]any{"path": target, "content": "-appended"})
	requireToolText(t, msgs, err, "Appended to "+target)
	if got, err := os.ReadFile(target); err != nil || string(got) != editedAppendedContent {
		t.Fatalf("additional-root content = %q, %v", got, err)
	}
}

func assertPrimaryRelativeWrite(t *testing.T, f writeRootsFixture, writeTool core.Tool) {
	t.Helper()
	path := filepath.Join(f.primary, "nested", "relative.txt")
	msgs, err := writeTool.Execute(context.Background(), map[string]any{"path": "nested/relative.txt", "content": "relative"})
	requireToolText(t, msgs, err, "File written: nested/relative.txt")
	if got, err := os.ReadFile(path); err != nil || string(got) != "relative" {
		t.Fatalf("relative-primary content = %q, %v", got, err)
	}
}

type writeRootCase struct {
	name string
	tool core.Tool
	args map[string]any
}

func assertOutsideRootWrites(t *testing.T, f writeRootsFixture, tools writeRootTools) {
	t.Helper()
	deniedParent := filepath.Join(f.outside, "missing", "tree")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	for _, tc := range []writeRootCase{
		{name: "write", tool: tools.write, args: map[string]any{"path": deniedTarget, "content": "must not write"}},
		{name: "edit", tool: tools.edit, args: map[string]any{"path": deniedTarget, "old_text": "outside", "new_text": "changed"}},
		{name: "append", tool: tools.append, args: map[string]any{"path": deniedTarget, "content": "must not append"}},
	} {
		t.Run(tc.name+" absolute outside root", func(t *testing.T) {
			assertDeniedWriteRoot(t, tc)
			if _, statErr := os.Stat(deniedParent); !os.IsNotExist(statErr) {
				t.Fatalf("denied parent = %v, want absent", statErr)
			}
		})
	}
}

func assertDeniedWriteRoot(t *testing.T, tc writeRootCase) {
	t.Helper()
	msgs, err := tc.tool.Execute(context.Background(), tc.args)
	got := requireToolTextContains(t, msgs, err, "path escapes workspace")
	if strings.Contains(got, "must not") {
		t.Fatalf("denial leaked requested content: %q", got)
	}
}

func assertTraversalWrite(t *testing.T, f writeRootsFixture, writeTool core.Tool) {
	t.Helper()
	path := filepath.Join(f.primary, "..", filepath.Base(f.outside), "traversal.txt")
	msgs, err := writeTool.Execute(context.Background(), map[string]any{"path": path, "content": "must not write"})
	requireToolTextContains(t, msgs, err, "path escapes workspace")
	if _, statErr := os.Stat(filepath.Join(f.outside, "traversal.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal target = %v, want absent", statErr)
	}
}

func assertExternalSymlinkWrite(t *testing.T, f writeRootsFixture, writeTool core.Tool) {
	t.Helper()
	linkParent := filepath.Join(f.primary, "external")
	if err := os.Symlink(f.outside, linkParent); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	path := filepath.Join(linkParent, "created.txt")
	msgs, err := writeTool.Execute(context.Background(), map[string]any{"path": path, "content": "must not write"})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("symlink denial result = msgs:%#v err:%v, want one tool message", msgs, err)
	}
	got := msgs[0].TextContent()
	if !strings.Contains(got, "path escapes workspace") && !strings.Contains(got, "access denied") {
		t.Fatalf("symlink denial = %q, want a confinement refusal", got)
	}
	if strings.Contains(got, "must not write") {
		t.Fatalf("symlink denial leaked requested content: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.outside, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target = %v, want absent", statErr)
	}
}
