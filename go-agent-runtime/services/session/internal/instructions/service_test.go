package instructions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestServiceResolvePreservesPromptSelectionSkillsAndScopeOrder(t *testing.T) {
	loader := &recordingLoader{
		files:   map[string][]byte{"/workspace/AGENTS.md": []byte("agents\n")},
		summary: "skills summary",
	}
	service := New()
	result, err := service.Resolve(context.Background(), session.InstructionRequest{
		WorkspaceDir:               "/workspace",
		FilesystemScopeDescription: "root=/workspace",
		FilesystemScopeSet:         true,
		Loader:                     loader,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := "agents\n\n\n---\n\nskills summary\n\nFilesystem scope: root=/workspace. Relative filesystem-tool paths resolve from this workdir."
	if result.Instructions != want {
		t.Fatalf("instructions = %q, want %q", result.Instructions, want)
	}
	if got := strings.Join(loader.calls, ","); got != "read:/workspace/AGENTS.md,skills" {
		t.Fatalf("loader calls = %q, want AGENTS.md then skills summary", got)
	}
}

func TestServiceResolveExplicitFileAndLiteralPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		want       string
		wantCalls  string
		statErrors map[string]error
		files      map[string][]byte
	}{
		{
			name:      "explicit file wins over workspace",
			prompt:    "/workspace/prompt.md",
			want:      "explicit file\n\n---\n\nskills summary",
			wantCalls: "stat:/workspace/prompt.md,read:/workspace/prompt.md,skills",
			files:     map[string][]byte{"/workspace/prompt.md": []byte("explicit file"), "/workspace/AGENTS.md": []byte("workspace")},
		},
		{
			name:       "missing explicit path remains literal",
			prompt:     "/workspace/missing.md",
			want:       "/workspace/missing.md\n\n---\n\nskills summary",
			wantCalls:  "stat:/workspace/missing.md,skills",
			statErrors: map[string]error{"/workspace/missing.md": fs.ErrNotExist},
			files:      map[string][]byte{"/workspace/AGENTS.md": []byte("workspace")},
		},
		{
			name:      "raw text wins over workspace",
			prompt:    "raw customer instructions",
			want:      "raw customer instructions\n\n---\n\nskills summary",
			wantCalls: "stat:raw customer instructions,skills",
			statErrors: map[string]error{
				"raw customer instructions": fs.ErrNotExist,
			},
			files: map[string][]byte{"/workspace/AGENTS.md": []byte("workspace")},
		},
		{
			name:      "none disables all prompt additions",
			prompt:    "none",
			wantCalls: "",
			files:     map[string][]byte{"/workspace/AGENTS.md": []byte("workspace")},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			loader := &recordingLoader{files: testCase.files, statErrors: testCase.statErrors, summary: "skills summary"}
			result, err := New().Resolve(context.Background(), session.InstructionRequest{
				Prompt:       testCase.prompt,
				WorkspaceDir: "/workspace",
				Loader:       loader,
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if result.Instructions != testCase.want {
				t.Fatalf("instructions = %q, want %q", result.Instructions, testCase.want)
			}
			if got := strings.Join(loader.calls, ","); got != testCase.wantCalls {
				t.Fatalf("loader calls = %q, want %q", got, testCase.wantCalls)
			}
		})
	}
}

func TestServiceResolvePropagatesReadErrorsWithPath(t *testing.T) {
	readErr := errors.New("permission denied")
	loader := &recordingLoader{
		files:    map[string][]byte{"/workspace/prompt.md": nil},
		readErrs: map[string]error{"/workspace/prompt.md": readErr},
	}
	_, err := New().Resolve(context.Background(), session.InstructionRequest{
		Prompt:       "/workspace/prompt.md",
		WorkspaceDir: "/workspace",
		Loader:       loader,
	})
	if err == nil || !strings.Contains(err.Error(), "read system prompt /workspace/prompt.md") || !errors.Is(err, readErr) {
		t.Fatalf("explicit prompt error = %v, want wrapped path and cause", err)
	}

	agentsErr := errors.New("AGENTS read failed")
	loader = &recordingLoader{readErrs: map[string]error{"/workspace/AGENTS.md": agentsErr}}
	_, err = New().Resolve(context.Background(), session.InstructionRequest{
		WorkspaceDir: "/workspace",
		Loader:       loader,
	})
	if err == nil || !strings.Contains(err.Error(), "read AGENTS.md /workspace/AGENTS.md") || !errors.Is(err, agentsErr) {
		t.Fatalf("AGENTS error = %v, want wrapped path and cause", err)
	}
}

func TestServiceComposeIsIdentityWithoutToolsAndIdempotentWithEveryPolicy(t *testing.T) {
	service := New()
	if got := service.Compose(session.InstructionComposition{Instructions: "customer"}); got != "customer" {
		t.Fatalf("no-tools composition = %q, want exact input", got)
	}

	request := session.InstructionComposition{
		Instructions:           "customer",
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}, {Name: "webmcp_list_tabs"}},
		BrowserCapabilityState: session.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		PageSightToolID:        "show_page",
	}
	first := service.Compose(request)
	second := service.Compose(session.InstructionComposition{
		Instructions:           first,
		ToolDefinitions:        request.ToolDefinitions,
		BrowserCapabilityState: request.BrowserCapabilityState,
		BrowserToolsEnabled:    request.BrowserToolsEnabled,
		PageSightToolID:        request.PageSightToolID,
	})
	if second != first {
		t.Fatalf("composition changed on second pass:\nfirst=%q\nsecond=%q", first, second)
	}
	ordered := []string{
		"WebMCP browser selection:",
		"Tool-grounding requirements:",
		"WebMCP tab selection calibration:",
		"WebMCP ambiguity recovery:",
		"Sight routing requirements:",
	}
	last := -1
	for _, heading := range ordered {
		index := strings.Index(first, heading)
		if index <= last {
			t.Fatalf("heading %q index=%d after %d in %q", heading, index, last, first)
		}
		if strings.Count(first, heading) != 1 {
			t.Fatalf("heading %q count=%d, want 1", heading, strings.Count(first, heading))
		}
		last = index
	}
}

func TestServiceComposeUsesNormalizedToolIdentifierAndBrowserState(t *testing.T) {
	service := New()
	withCustomSight := service.Compose(session.InstructionComposition{
		Instructions:        "customer",
		ToolDefinitions:     []messages.ToolDefinition{{Name: "custom_sight"}},
		BrowserToolsEnabled: true,
		PageSightToolID:     "custom_sight",
	})
	if !strings.Contains(withCustomSight, "Sight routing requirements:") {
		t.Fatalf("custom page-sight identifier did not enable sight policy: %q", withCustomSight)
	}
	if !strings.Contains(withCustomSight, "use custom_sight") || strings.Contains(withCustomSight, "use show_page") {
		t.Fatalf("custom page-sight identifier was not substituted in policy: %q", withCustomSight)
	}
	withoutBrowser := service.Compose(session.InstructionComposition{
		Instructions:           "customer",
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}},
		BrowserCapabilityState: session.BrowserCapabilityConnectedUnselected,
	})
	if strings.Contains(withoutBrowser, "WebMCP tab selection calibration:") || strings.Contains(withoutBrowser, "Sight routing requirements:") {
		t.Fatalf("browser-only policy leaked when browser capability is disabled: %q", withoutBrowser)
	}
	if !strings.Contains(withoutBrowser, "WebMCP browser selection:") {
		t.Fatalf("connected-unselected state was not preserved independently of browser-enabled flag: %q", withoutBrowser)
	}
}

func TestServiceResolveDoesNotReadUnselectedSources(t *testing.T) {
	loader := &recordingLoader{statErrors: map[string]error{"literal": fs.ErrNotExist}, summary: "must not be read"}
	result, err := New().Resolve(context.Background(), session.InstructionRequest{Prompt: "none", WorkspaceDir: "/workspace", Loader: loader})
	if err != nil {
		t.Fatalf("Resolve none: %v", err)
	}
	if result.Instructions != "" || len(loader.calls) != 0 {
		t.Fatalf("none resolution = %q with calls %#v, want empty and no I/O", result.Instructions, loader.calls)
	}

	loader = &recordingLoader{statErrors: map[string]error{"literal": fs.ErrNotExist}}
	result, err = New().Resolve(context.Background(), session.InstructionRequest{Prompt: "literal", WorkspaceDir: "", Loader: loader})
	if err != nil {
		t.Fatalf("Resolve literal: %v", err)
	}
	if result.Instructions != "literal" || strings.Join(loader.calls, ",") != "stat:literal" {
		t.Fatalf("literal resolution = %q with calls %#v, want stat only", result.Instructions, loader.calls)
	}
}

type recordingLoader struct {
	files      map[string][]byte
	statErrors map[string]error
	readErrs   map[string]error
	summary    string
	calls      []string
}

func (l *recordingLoader) Stat(path string) error {
	l.calls = append(l.calls, "stat:"+path)
	if err, ok := l.statErrors[path]; ok {
		return err
	}
	if _, ok := l.files[path]; ok {
		return nil
	}
	return fs.ErrNotExist
}

func (l *recordingLoader) ReadFile(path string) ([]byte, error) {
	l.calls = append(l.calls, "read:"+path)
	if err, ok := l.readErrs[path]; ok {
		return nil, err
	}
	if data, ok := l.files[path]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, fs.ErrNotExist
}

func (l *recordingLoader) SkillsSummary() (string, error) {
	l.calls = append(l.calls, "skills")
	return l.summary, nil
}

var _ session.InstructionLoader = (*recordingLoader)(nil)

func (l *recordingLoader) String() string { return fmt.Sprint(l.calls) }
