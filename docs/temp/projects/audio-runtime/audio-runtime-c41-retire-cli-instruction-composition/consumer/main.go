// Command instruction-consumer exercises the public session instruction
// contract from a separate Go module. It deliberately imports no CLI package,
// flags, terminal state, provider credential, or private runtime package.
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

//go:embed expected.json
var expectedFS embed.FS

type expected struct {
	Resolved         string   `json:"resolved"`
	NoTools          string   `json:"no_tools"`
	RequiredHeadings []string `json:"required_headings"`
	ComposedSHA256   string   `json:"composed_sha256"`
}

type loader struct {
	files   map[string][]byte
	summary string
	calls   []string
}

func (l *loader) Stat(path string) error {
	l.calls = append(l.calls, "stat:"+path)
	if _, ok := l.files[path]; ok {
		return nil
	}
	return fs.ErrNotExist
}

func (l *loader) ReadFile(path string) ([]byte, error) {
	l.calls = append(l.calls, "read:"+path)
	data, ok := l.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (l *loader) SkillsSummary() (string, error) {
	l.calls = append(l.calls, "skills")
	return l.summary, nil
}

var _ session.InstructionLoader = (*loader)(nil)

func main() {
	mode := "consumer"
	if len(os.Args) == 3 && os.Args[1] == "--mode" {
		mode = os.Args[2]
	}
	if mode == "cleanup-child" {
		fmt.Println(`{"status":"started","purpose":"bounded cleanup negative control"}`)
		select {}
	}
	if len(os.Args) != 1 && len(os.Args) != 3 {
		writeError("usage: instruction-consumer [--mode consumer|public-session|consumer-negative|regression|print|cleanup-child]")
		os.Exit(2)
	}

	if mode == "consumer-negative" {
		_ = os.Setenv("C41_MUTATE_EXPECTED", "1")
	}
	if mode == "cleanup-negative" {
		writeError("cleanup-negative is driven by verify.py")
		os.Exit(2)
	}
	if mode != "consumer" && mode != "public-session" && mode != "consumer-negative" && mode != "regression" && mode != "print" {
		writeError("unknown mode: " + mode)
		os.Exit(2)
	}

	result, err := run(mode)
	if err != nil {
		writeError(err.Error())
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		writeError(err.Error())
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func run(mode string) (map[string]any, error) {
	want, err := readExpected()
	if err != nil {
		return nil, err
	}
	service := sessionwire.NewInstructionService()
	loader := &loader{files: map[string][]byte{"/workspace/prompt.md": []byte("literal prompt")}, summary: "skill summary"}
	resolved, err := service.Resolve(context.Background(), session.InstructionRequest{
		Prompt:                     "/workspace/prompt.md",
		WorkspaceDir:               "/workspace",
		FilesystemScopeDescription: "/workspace",
		FilesystemScopeSet:         true,
		Loader:                     loader,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve public instruction contract: %w", err)
	}
	if mode != "print" {
		if resolved.Instructions != want.Resolved {
			return nil, fmt.Errorf("resolved oracle mismatch: got %q want %q", resolved.Instructions, want.Resolved)
		}
		if got := strings.Join(loader.calls, ","); got != "stat:/workspace/prompt.md,read:/workspace/prompt.md,skills" {
			return nil, fmt.Errorf("loader call order mismatch: %s", got)
		}
	}

	noTools := service.Compose(session.InstructionComposition{Instructions: want.NoTools})
	composition := service.Compose(session.InstructionComposition{
		Instructions:           "customer instructions",
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}, {Name: "webmcp_list_tabs"}, {Name: "webmcp_select_tab"}},
		BrowserCapabilityState: session.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		PageSightToolID:        "show_page",
	})
	digest := sha256.Sum256([]byte(composition))
	composedSHA256 := hex.EncodeToString(digest[:])
	if mode == "print" {
		return map[string]any{
			"mode":            mode,
			"resolved":        resolved.Instructions,
			"no_tools":        noTools,
			"composition":     composition,
			"composed_sha256": composedSHA256,
			"loader_calls":    loader.calls,
		}, nil
	}
	expectedDigest := want.ComposedSHA256
	if os.Getenv("C41_MUTATE_EXPECTED") == "1" {
		expectedDigest = strings.Repeat("0", len(composedSHA256))
	}
	if resolved.Instructions != want.Resolved {
		return nil, fmt.Errorf("resolved oracle mismatch: got %q want %q", resolved.Instructions, want.Resolved)
	}
	if noTools != want.NoTools {
		return nil, fmt.Errorf("no-tools oracle mismatch: got %q want %q", noTools, want.NoTools)
	}
	if composedSHA256 != expectedDigest {
		return nil, fmt.Errorf("policy oracle mismatch: composed sha256=%s expected=%s", composedSHA256, expectedDigest)
	}
	for _, heading := range want.RequiredHeadings {
		if strings.Count(composition, heading) != 1 {
			return nil, fmt.Errorf("policy heading %q count=%d", heading, strings.Count(composition, heading))
		}
	}
	if mode == "regression" {
		selected := service.Compose(session.InstructionComposition{Instructions: "customer", ToolDefinitions: []messages.ToolDefinition{{Name: "show_page"}}})
		if strings.Contains(selected, "WebMCP browser selection:") || strings.Contains(selected, "WebMCP tab selection calibration:") {
			return nil, errors.New("browser policy leaked into a non-browser session")
		}
	}
	return map[string]any{
		"mode":             mode,
		"status":           "accepted",
		"resolved":         resolved.Instructions,
		"loader_calls":     loader.calls,
		"no_tools":         noTools,
		"composed_sha256":  composedSHA256,
		"policy_headings":  want.RequiredHeadings,
		"cli_imports":      false,
		"private_imports":  false,
		"credentials":      false,
		"hidden_global_io": false,
	}, nil
}

func readExpected() (expected, error) {
	data, err := expectedFS.ReadFile("expected.json")
	if err != nil {
		return expected{}, err
	}
	var result expected
	if err := json.Unmarshal(data, &result); err != nil {
		return expected{}, err
	}
	return result, nil
}

func writeError(message string) {
	encoded, _ := json.Marshal(map[string]any{"status": "rejected", "error": message})
	fmt.Println(string(encoded))
}
