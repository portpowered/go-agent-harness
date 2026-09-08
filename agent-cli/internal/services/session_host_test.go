package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestCLIHostResolvesPromptFilesBeforeRuntimeAdmission(t *testing.T) {
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "instructions.txt")
	for name, content := range map[string]string{
		promptPath:                            "explicit prompt file",
		filepath.Join(workspace, "AGENTS.md"): "workspace instructions",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ name, value, want string }{
		{"workspace default", "", "workspace instructions"},
		{"explicit file", promptPath, "explicit prompt file"},
		{"literal override", "explicit inline prompt", "explicit inline prompt"},
		{"disable discovery", "none", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveCLIPrompt(test.value, workspace)
			if err != nil || got != test.want {
				t.Fatalf("prompt=%q err=%v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := resolveCLIPrompt(workspace, workspace); err == nil {
		t.Fatal("an unreadable prompt source must report an error")
	}
}

func TestCLIHostRejectsInvalidWorkspace(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(file, "missing")} {
		if _, err := cliWorkDir(&flags.GlobalFlags{WorkDirPath: path}); err == nil {
			t.Fatalf("workspace %q was accepted", path)
		}
	}
}

func TestCLIHostPreservesPromptOwnershipWhenResuming(t *testing.T) {
	workspace := t.TempDir()
	// A directory would fail prompt loading, proving resume skips discovery.
	if err := os.Mkdir(filepath.Join(workspace, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, request := range []session.Request{{SessionID: "saved"}, {ContinueLastSession: true}} {
		prompt, err := resolveCLIRequestPrompt(request, workspace)
		if err != nil || prompt != "" {
			t.Fatalf("resume rediscovered prompt %q: %v", prompt, err)
		}
	}
	if _, err := resolveCLIRequestPrompt(session.Request{}, workspace); err == nil {
		t.Fatal("fresh request bypassed prompt discovery")
	}
	prompt, err := resolveCLIRequestPrompt(session.Request{SessionID: "saved", SystemPrompt: "explicit override"}, workspace)
	if err != nil || prompt != "explicit override" {
		t.Fatalf("explicit request lost before validation: %q, %v", prompt, err)
	}
}

func TestCLIHostResolvesAllowedRootsFromEffectiveWorkspace(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	supplied := &flags.GlobalFlags{WorkDirPath: workspace, AllowPathList: []string{"notes", external}}
	roots := globalAllowPaths(supplied, workspace)
	if len(roots) != 2 || roots[0] != filepath.Join(workspace, "notes") || roots[1] != external {
		t.Fatalf("resolved roots = %v", roots)
	}
	if supplied.AllowPathList[0] != "notes" {
		t.Fatal("host mutated caller-owned flag paths")
	}
}
