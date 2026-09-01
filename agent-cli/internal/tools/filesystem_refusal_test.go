package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		tool      Tool
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
		})
	}

	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "SENTINEL-CONTENT" {
		t.Fatalf("outside sentinel = %q, %v; want unchanged", got, err)
	}
	if _, err := os.Stat(deniedParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied parent = %v, want absent", err)
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
		return []messages.ImagePart{{Bytes: []byte("MUST-NOT-READ"), MediaType: "image/png"}}, nil
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
