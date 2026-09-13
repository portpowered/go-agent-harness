package service

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
)

func TestResolvePreservesSelectionSkillOrderAndScope(t *testing.T) {
	loader := &recordingLoader{
		files:   map[string][]byte{"/workspace/AGENTS.md": []byte("agents\n")},
		summary: "skills summary",
	}
	result, err := New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
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

func TestResolveExplicitFileLiteralAndNoneRules(t *testing.T) {
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
			name:      "literal prompt wins over workspace",
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
			result, err := New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
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

func TestResolveDoesNotUseRelativeWorkspaceWhenEmpty(t *testing.T) {
	loader := &recordingLoader{statErrors: map[string]error{"literal": fs.ErrNotExist}}
	result, err := New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt: "literal",
		Loader: loader,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Instructions != "literal" || strings.Join(loader.calls, ",") != "stat:literal" {
		t.Fatalf("result = %q with calls %#v, want literal and stat only", result.Instructions, loader.calls)
	}
}

func TestResolveAttributesLoaderFailuresAndBounds(t *testing.T) {
	readErr := errors.New("permission denied")
	loader := &recordingLoader{
		files:    map[string][]byte{"/workspace/prompt.md": nil},
		readErrs: map[string]error{"/workspace/prompt.md": readErr},
	}
	_, err := New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt:       "/workspace/prompt.md",
		WorkspaceDir: "/workspace",
		Loader:       loader,
	})
	assertResolutionError(t, err, sessioninstructions.PhasePromptRead, readErr)
	if !strings.Contains(err.Error(), "read system prompt /workspace/prompt.md") {
		t.Fatalf("explicit prompt error = %v, want path attribution", err)
	}

	summaryErr := errors.New("skills unavailable")
	loader = &recordingLoader{files: map[string][]byte{"/workspace/AGENTS.md": []byte("agents")}, summaryErr: summaryErr}
	_, err = New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
		WorkspaceDir: "/workspace",
		Loader:       loader,
	})
	assertResolutionError(t, err, sessioninstructions.PhaseSkillsSummary, summaryErr)

	_, err = New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt: strings.Repeat("x", sessioninstructions.MaxInstructionBytes+1),
	})
	assertResolutionError(t, err, sessioninstructions.PhaseValidation, sessioninstructions.ErrInstructionTooLarge)

	_, err = New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
		Prompt: string([]byte{'a', 0, 'b'}),
	})
	assertResolutionError(t, err, sessioninstructions.PhaseValidation, sessioninstructions.ErrMalformedInstruction)

	for _, testCase := range []struct {
		name  string
		data  []byte
		cause error
	}{
		{name: "malformed file", data: []byte{'a', 0, 'b'}, cause: sessioninstructions.ErrMalformedInstruction},
		{name: "oversized file", data: []byte(strings.Repeat("x", sessioninstructions.MaxInstructionBytes+1)), cause: sessioninstructions.ErrInstructionTooLarge},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			loader := &recordingLoader{files: map[string][]byte{"/workspace/prompt.md": testCase.data}}
			_, err := New().Resolve(context.Background(), sessioninstructions.InstructionRequest{
				Prompt:       "/workspace/prompt.md",
				WorkspaceDir: "/workspace",
				Loader:       loader,
			})
			assertResolutionError(t, err, sessioninstructions.PhasePromptRead, testCase.cause)
		})
	}

	_, err = New().Resolve(context.Background(), sessioninstructions.InstructionRequest{WorkspaceDir: "/workspace"})
	assertResolutionError(t, err, sessioninstructions.PhaseValidation, sessioninstructions.ErrLoaderRequired)
}

func TestResolveHonorsContextAwareCancellation(t *testing.T) {
	loader := &contextAwareLoader{readStarted: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := New().Resolve(ctx, sessioninstructions.InstructionRequest{
			WorkspaceDir: "/workspace",
			Loader:       loader,
		})
		resultCh <- err
	}()
	<-loader.readStarted
	cancel()
	err := <-resultCh
	assertResolutionError(t, err, sessioninstructions.PhaseWorkspaceRead, context.Canceled)
}

func TestResolveChecksCancellationAfterLegacyLoaderCalls(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		phase legacyLoaderPhase
		want  sessioninstructions.ResolutionPhase
	}{
		{name: "stat", phase: legacyStatPhase, want: sessioninstructions.PhasePromptStat},
		{name: "read", phase: legacyReadPhase, want: sessioninstructions.PhasePromptRead},
		{name: "skills", phase: legacySkillsPhase, want: sessioninstructions.PhaseSkillsSummary},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			loader := &legacyCancellationLoader{phase: testCase.phase, entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			resultCh := make(chan error, 1)
			go func() {
				request := sessioninstructions.InstructionRequest{Loader: loader, WorkspaceDir: "/workspace"}
				if testCase.phase != legacySkillsPhase {
					request.Prompt = "prompt.md"
				}
				_, err := New().Resolve(ctx, request)
				resultCh <- err
			}()
			<-loader.entered
			cancel()
			close(loader.release)
			assertResolutionError(t, <-resultCh, testCase.want, context.Canceled)
		})
	}
}

func TestComposePreservesPolicyOrderAndIsIdempotent(t *testing.T) {
	service := New()
	if got := service.Compose(sessioninstructions.InstructionComposition{Instructions: "customer"}); got != "customer" {
		t.Fatalf("no-tools composition = %q, want exact input", got)
	}
	request := sessioninstructions.InstructionComposition{
		Instructions:           "customer",
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}, {Name: "webmcp_list_tabs"}},
		BrowserCapabilityState: sessioninstructions.BrowserCapabilityConnectedUnselected,
		BrowserToolsEnabled:    true,
		PageSightToolID:        "show_page",
	}
	first := service.Compose(request)
	second := service.Compose(sessioninstructions.InstructionComposition{
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
		if index <= last || strings.Count(first, heading) != 1 {
			t.Fatalf("heading %q index=%d last=%d count=%d", heading, index, last, strings.Count(first, heading))
		}
		last = index
	}
}

func TestComposeUsesCustomSightIdentifierAndClonesSnapshot(t *testing.T) {
	definitions := []messages.ToolDefinition{{Name: "custom_sight", Parameters: []messages.ToolParameter{{Name: "url"}}}}
	got := New().Compose(sessioninstructions.InstructionComposition{
		Instructions:        "customer",
		ToolDefinitions:     definitions,
		BrowserToolsEnabled: true,
		PageSightToolID:     "custom_sight",
	})
	definitions[0].Name = "other"
	if !strings.Contains(got, "use custom_sight") || strings.Contains(got, "use show_page") {
		t.Fatalf("custom sight policy = %q", got)
	}
	withoutBrowser := New().Compose(sessioninstructions.InstructionComposition{
		Instructions:           "customer",
		ToolDefinitions:        []messages.ToolDefinition{{Name: "show_page"}},
		BrowserCapabilityState: sessioninstructions.BrowserCapabilityConnectedUnselected,
	})
	if strings.Contains(withoutBrowser, "Sight routing requirements:") || strings.Contains(withoutBrowser, "WebMCP tab selection calibration:") {
		t.Fatalf("browser-only policy leaked: %q", withoutBrowser)
	}
}

func assertResolutionError(t *testing.T, err error, phase sessioninstructions.ResolutionPhase, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("error is nil, want phase %s and cause %v", phase, cause)
	}
	var resolutionErr *sessioninstructions.ResolutionError
	if !errors.As(err, &resolutionErr) {
		t.Fatalf("error %T does not expose ResolutionError: %v", err, err)
	}
	if resolutionErr.Phase != phase {
		t.Fatalf("error phase = %q, want %q", resolutionErr.Phase, phase)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not unwrap %v", err, cause)
	}
}

type recordingLoader struct {
	files      map[string][]byte
	statErrors map[string]error
	readErrs   map[string]error
	summary    string
	summaryErr error
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
	return l.summary, l.summaryErr
}

type contextAwareLoader struct {
	readStarted chan struct{}
}

func (l *contextAwareLoader) Stat(path string) error { return nil }

func (l *contextAwareLoader) ReadFile(path string) ([]byte, error) { return []byte("legacy"), nil }

func (l *contextAwareLoader) SkillsSummary() (string, error) { return "", nil }

func (l *contextAwareLoader) StatContext(context.Context, string) error { return nil }

func (l *contextAwareLoader) ReadFileContext(ctx context.Context, _ string) ([]byte, error) {
	close(l.readStarted)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (l *contextAwareLoader) SkillsSummaryContext(context.Context) (string, error) { return "", nil }

var _ sessioninstructions.InstructionLoader = (*recordingLoader)(nil)
var _ sessioninstructions.ContextAwareInstructionLoader = (*contextAwareLoader)(nil)

type legacyLoaderPhase string

const (
	legacyStatPhase   legacyLoaderPhase = "stat"
	legacyReadPhase   legacyLoaderPhase = "read"
	legacySkillsPhase legacyLoaderPhase = "skills"
)

type legacyCancellationLoader struct {
	phase   legacyLoaderPhase
	entered chan struct{}
	release chan struct{}
}

func (l *legacyCancellationLoader) Stat(string) error {
	if l.phase == legacyStatPhase {
		close(l.entered)
		<-l.release
		return fs.ErrNotExist
	}
	return nil
}

func (l *legacyCancellationLoader) ReadFile(path string) ([]byte, error) {
	if l.phase == legacyReadPhase {
		close(l.entered)
		<-l.release
		return nil, errors.New("read completed after cancellation")
	}
	if path == "/workspace/AGENTS.md" {
		return []byte("agents"), nil
	}
	return nil, fs.ErrNotExist
}

func (l *legacyCancellationLoader) SkillsSummary() (string, error) {
	if l.phase == legacySkillsPhase {
		close(l.entered)
		<-l.release
		return "summary", errors.New("summary completed after cancellation")
	}
	return "", nil
}

var _ sessioninstructions.InstructionLoader = (*legacyCancellationLoader)(nil)
