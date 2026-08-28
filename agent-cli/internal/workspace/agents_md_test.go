package workspace

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var updateGolden = flag.Bool("update", false, "update workspace package golden files")

// TestAgentsMDWorkspace_FilesystemSandbox is the S6 filesystem suite. The
// skipped subtests document contracts that are not exposed by this package on
// the current production head; they must not be replaced with test-only
// discovery or parsing logic.
func TestAgentsMDWorkspace_FilesystemSandbox(t *testing.T) {
	t.Run("missing workspace creates no file and returns a typed filesystem error", func(t *testing.T) {
		workspaceDir := filepath.Join(t.TempDir(), "does-not-exist")

		err := EnsureAgentsMD(workspaceDir, nil)
		if err == nil {
			t.Fatal("EnsureAgentsMD returned nil for a workspace with a missing parent")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("EnsureAgentsMD error = %v, want errors.Is(..., fs.ErrNotExist)", err)
		}
	})

	t.Run("nested workspace creates the exact zero-tool document", func(t *testing.T) {
		workspaceDir := filepath.Join(t.TempDir(), "repository", "packages", "agent", "nested")
		if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
			t.Fatalf("create nested workspace: %v", err)
		}

		if err := EnsureAgentsMD(workspaceDir, nil); err != nil {
			t.Fatalf("EnsureAgentsMD: %v", err)
		}
		got := readAgentsMD(t, workspaceDir)
		if !strings.Contains(got, "No tools are currently registered.\n") {
			t.Fatalf("generated AGENTS.md does not record the zero-tool state:\n%s", got)
		}
		assertGolden(t, "agents_md_zero_tools.golden", normalizeAgentsMD(got, workspaceDir))
	})

	t.Run("existing file is preserved byte-for-byte", func(t *testing.T) {
		workspaceDir := t.TempDir()
		want := "# Maintainer instructions\r\nkeep this exact content\r\n"
		writeAgentsMD(t, workspaceDir, want)

		if err := EnsureAgentsMD(workspaceDir, representativeToolDefinitions()); err != nil {
			t.Fatalf("EnsureAgentsMD: %v", err)
		}
		if got := readAgentsMD(t, workspaceDir); got != want {
			t.Fatalf("existing AGENTS.md changed:\n got: %q\nwant: %q", got, want)
		}
	})

	t.Run("empty and whitespace-only files are preserved", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
		}{
			{name: "empty", content: ""},
			{name: "whitespace-only", content: " \t\r\n\n\t "},
		} {
			t.Run(tc.name, func(t *testing.T) {
				workspaceDir := t.TempDir()
				writeAgentsMD(t, workspaceDir, tc.content)

				if err := EnsureAgentsMD(workspaceDir, representativeToolDefinitions()); err != nil {
					t.Fatalf("EnsureAgentsMD: %v", err)
				}
				if got := readAgentsMD(t, workspaceDir); got != tc.content {
					t.Fatalf("existing %s AGENTS.md changed: got %q, want %q", tc.name, got, tc.content)
				}
				info, err := os.Stat(filepath.Join(workspaceDir, AgentsMDFileName))
				if err != nil {
					t.Fatalf("stat preserved %s AGENTS.md: %v", tc.name, err)
				}
				if info.Size() != int64(len(tc.content)) {
					t.Fatalf("preserved %s AGENTS.md size = %d, want %d", tc.name, info.Size(), len(tc.content))
				}
			})
		}
	})

	t.Run("large existing file is preserved byte-for-byte", func(t *testing.T) {
		workspaceDir := t.TempDir()
		want := strings.Repeat("deterministic instruction payload\n", 8192) + "END-OF-LARGE-AGENTS-MD\n"
		writeAgentsMD(t, workspaceDir, want)

		if err := EnsureAgentsMD(workspaceDir, representativeToolDefinitions()); err != nil {
			t.Fatalf("EnsureAgentsMD: %v", err)
		}

		got := readAgentsMD(t, workspaceDir)
		if got != want {
			t.Fatalf("large AGENTS.md changed: got %d bytes, want %d", len(got), len(want))
		}
		if len(got) != len(want) || !strings.HasSuffix(got, "END-OF-LARGE-AGENTS-MD\n") {
			t.Fatalf("large AGENTS.md lost bytes or terminal marker: length=%d, want=%d", len(got), len(want))
		}
	})

	t.Run("multiple depths and declared boundary", func(t *testing.T) {
		root := t.TempDir()
		boundary := filepath.Join(root, "declared-workspace")
		nested := filepath.Join(boundary, "src", "component", "child")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("create nested fixture: %v", err)
		}
		writeFile(t, filepath.Join(root, AgentsMDFileName), "above-boundary sentinel\n")
		writeFile(t, filepath.Join(boundary, AgentsMDFileName), "boundary instructions\n")
		writeFile(t, filepath.Join(boundary, "src", AgentsMDFileName), "src instructions\n")
		writeFile(t, filepath.Join(boundary, "src", "component", AgentsMDFileName), "component instructions\n")

		t.Skip("AGENTS.md upward discovery and parsed/resolved precedence are not exposed by agent-cli/internal/workspace on this head")
	})

	t.Run("filesystem-root termination", func(t *testing.T) {
		t.Skip("AGENTS.md upward-walk boundary is not exposed by agent-cli/internal/workspace on this head")
	})

	t.Run("unreadable file", func(t *testing.T) {
		t.Skip("the package has no AGENTS.md reader or typed unreadable-file contract to exercise; no coverage is claimed")
	})

	t.Run("large file is complete or typed-rejected", func(t *testing.T) {
		t.Skip("the package has no AGENTS.md loader or typed oversized-file contract to exercise; no coverage is claimed")
	})
}

// TestAgentsMDWorkspace_Golden is the S3 rendered-form suite. The parsed or
// resolved representation portion remains a documented skip because no such
// production representation exists on the current head.
func TestAgentsMDWorkspace_Golden(t *testing.T) {
	t.Run("rendered zero-tool form", func(t *testing.T) {
		workspaceDir := filepath.Join("<workspace>", "zero-tools")
		got := normalizeAgentsMD(generateAgentsMD(workspaceDir, nil), workspaceDir)
		assertGolden(t, "agents_md_zero_tools.golden", got)
	})

	t.Run("rendered representative tool form", func(t *testing.T) {
		workspaceDir := filepath.Join("<workspace>", "representative-tools")
		got := normalizeAgentsMD(generateAgentsMD(workspaceDir, representativeToolDefinitions()), workspaceDir)
		assertGolden(t, "agents_md_tools.golden", got)
	})

	t.Run("parsed and resolved representation", func(t *testing.T) {
		t.Skip("the package has no parsed/resolved AGENTS.md representation to compare against a golden")
	})
}

func TestEnsureAgentsMD_ReconcilesAvailableToolsSection(t *testing.T) {
	readFile := messages.ToolDefinition{
		Name:        "read_file",
		Description: "Read a UTF-8 file from the workspace.",
	}
	writeFile := messages.ToolDefinition{
		Name:        "write_file",
		Description: "Write text to a UTF-8 workspace file.",
	}

	t.Run("stale section becomes current and preserves surrounding content", func(t *testing.T) {
		workspaceDir := t.TempDir()
		stale := "customer before\n" +
			"## Available Tools\n\n" +
			"No tools are currently registered.\n" +
			"### `write_file`\nold description\n" +
			"## Customer Notes\ncustomer after\n"
		writeAgentsMD(t, workspaceDir, stale)

		if err := EnsureAgentsMD(workspaceDir, []messages.ToolDefinition{readFile}); err != nil {
			t.Fatalf("EnsureAgentsMD: %v", err)
		}
		got := readAgentsMD(t, workspaceDir)
		if !strings.HasPrefix(got, "customer before\n") {
			t.Fatalf("content before managed section changed: %q", got)
		}
		if !strings.HasSuffix(got, "## Customer Notes\ncustomer after\n") {
			t.Fatalf("content after managed section changed: %q", got)
		}
		if !strings.Contains(got, "### `read_file`\nRead a UTF-8 file from the workspace.") {
			t.Fatalf("current tool definition missing: %q", got)
		}
		if strings.Contains(got, "No tools are currently registered.") || strings.Contains(got, "write_file") {
			t.Fatalf("stale tool content remained: %q", got)
		}
		if strings.Count(got, availableToolsHeading) != 1 || strings.Count(got, availableToolsStartMarker) != 1 || strings.Count(got, availableToolsEndMarker) != 1 {
			t.Fatalf("managed section markers are not unique: %q", got)
		}
	})

	t.Run("same definitions are idempotent", func(t *testing.T) {
		workspaceDir := t.TempDir()
		writeAgentsMD(t, workspaceDir, generateAgentsMD(workspaceDir, []messages.ToolDefinition{readFile, writeFile}))

		if err := EnsureAgentsMD(workspaceDir, []messages.ToolDefinition{readFile, writeFile}); err != nil {
			t.Fatalf("first EnsureAgentsMD: %v", err)
		}
		first := readAgentsMD(t, workspaceDir)
		if err := EnsureAgentsMD(workspaceDir, []messages.ToolDefinition{readFile, writeFile}); err != nil {
			t.Fatalf("second EnsureAgentsMD: %v", err)
		}
		if got := readAgentsMD(t, workspaceDir); got != first {
			t.Fatalf("same definitions changed AGENTS.md on repeat:\nfirst:\n%s\nsecond:\n%s", first, got)
		}
		if strings.Count(first, availableToolsHeading) != 1 || strings.Count(first, availableToolsStartMarker) != 1 || strings.Count(first, availableToolsEndMarker) != 1 {
			t.Fatalf("repeated reconciliation duplicated managed section: %q", first)
		}
	})

	t.Run("stale tool definitions are removed when no tools remain", func(t *testing.T) {
		workspaceDir := t.TempDir()
		writeAgentsMD(t, workspaceDir, generateAgentsMD(workspaceDir, []messages.ToolDefinition{readFile, writeFile}))

		if err := EnsureAgentsMD(workspaceDir, nil); err != nil {
			t.Fatalf("EnsureAgentsMD: %v", err)
		}
		got := readAgentsMD(t, workspaceDir)
		if !strings.Contains(got, "No tools are currently registered.") || strings.Contains(got, "read_file") || strings.Contains(got, "write_file") {
			t.Fatalf("zero-tool section is not current: %q", got)
		}
	})
}

func representativeToolDefinitions() []messages.ToolDefinition {
	return []messages.ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 file from the workspace.",
		},
		{
			Name:        "write_file",
			Description: "Write text to a UTF-8 workspace file.",
			Parameters: []messages.ToolParameter{
				{Name: "path", Type: "string", Description: "Workspace-relative path.", Required: true},
				{Name: "content", Type: "string", Description: "Bytes to write.", Required: false},
			},
		},
	}
}

func normalizeAgentsMD(content, workspaceDir string) string {
	content = strings.ReplaceAll(content, workspaceDir, "<workspace>")
	content = strings.ReplaceAll(content, fmt.Sprintf("- **OS**: %s", runtime.GOOS), "- **OS**: <os>")
	content = strings.ReplaceAll(content, fmt.Sprintf("- **Architecture**: %s", runtime.GOARCH), "- **Architecture**: <architecture>")
	return content
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", path, err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Fatalf("golden %s differs:\n got:\n%s\nwant:\n%s", path, got, want)
	}
}

func writeAgentsMD(t *testing.T, workspaceDir, content string) {
	t.Helper()
	writeFile(t, filepath.Join(workspaceDir, AgentsMDFileName), content)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAgentsMD(t *testing.T, workspaceDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(workspaceDir, AgentsMDFileName))
	if err != nil {
		t.Fatalf("read generated AGENTS.md: %v", err)
	}
	return string(content)
}
