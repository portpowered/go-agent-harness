package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestResolveZeroRequestIsInert(t *testing.T) {
	capability, err := New().Resolve(context.Background(), public.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if capability.Executor != nil || len(capability.Definitions) != 0 || capability.Invoker != nil || capability.Handle != nil {
		t.Fatalf("zero request produced an active capability: %#v", capability)
	}
	if capability.WorkspaceDir != "" || len(capability.AdditionalDirs) != 0 {
		t.Fatalf("zero request acquired host scope: %q %q", capability.WorkspaceDir, capability.AdditionalDirs)
	}
}

func TestResolveRejectsDefinitionsWithoutExecutor(t *testing.T) {
	_, err := New().Resolve(context.Background(), public.Request{
		Definitions: []messages.ToolDefinition{{Name: "host_tool"}},
	})
	if err == nil || err.Error() != "tool definitions require an execution route" {
		t.Fatalf("Resolve returned %v, want the missing execution route error", err)
	}
}

func TestResolveDefaultSurfaceRequiresHostWorkspace(t *testing.T) {
	_, err := New().Resolve(context.Background(), public.Request{UseDefaultTool: true})
	if err == nil || err.Error() != "resolve filesystem scope: workdir is required for the default tool surface" {
		t.Fatalf("Resolve returned %v, want the explicit workspace error", err)
	}
}

func TestBuildSkillsSummaryUsesExplicitRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(root, "summary-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: summary-skill\ndescription: Summary test skill\n---\nInstructions.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	summary, err := New().BuildSkillsSummary(context.Background(), public.SkillSummaryRequest{
		SkillRoots: []public.SkillRoot{{Directory: root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "summary-skill") || !strings.Contains(summary, "Summary test skill") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestDefaultCapabilityBindsSkillRootsPerRequest(t *testing.T) {
	workspace := t.TempDir()
	firstRoot := filepath.Join(t.TempDir(), "first-skills")
	secondRoot := filepath.Join(t.TempDir(), "second-skills")
	writeSkill := func(root, body string) {
		t.Helper()
		dir := filepath.Join(root, "same-skill")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: same-skill\ndescription: Bound request skill\n---\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(firstRoot, "first request body")
	writeSkill(secondRoot, "second request body")

	resolve := func(root string) string {
		t.Helper()
		capability, err := New().Resolve(context.Background(), public.Request{
			WorkDir:        workspace,
			SkillRoots:     []public.SkillRoot{{Directory: root}},
			UseDefaultTool: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		arguments, err := json.Marshal(map[string]string{"skill_name": "same-skill"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := capability.Invoker.Invoke(context.Background(), public.Invocation{
			Name:      "load_skill",
			Arguments: string(arguments),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Content
	}
	if got := resolve(firstRoot); !strings.Contains(got, "first request body") {
		t.Fatalf("first bound skill = %q", got)
	}
	if got := resolve(secondRoot); !strings.Contains(got, "second request body") {
		t.Fatalf("second bound skill = %q", got)
	}
}

func TestDefaultCapabilityInjectsAudioCodecIntoReadFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.wav")
	if err := os.WriteFile(path, []byte("encoded audio"), 0644); err != nil {
		t.Fatal(err)
	}
	codec := &serviceTestAudioCodec{}
	capability, err := NewWithAudioCodec(codec).Resolve(context.Background(), public.Request{
		WorkDir:        workspace,
		UseDefaultTool: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := capability.Invoker.Invoke(context.Background(), public.Invocation{
		ID:        "audio-call",
		Name:      "read_file",
		Arguments: string(arguments),
	})
	if err != nil {
		t.Fatal(err)
	}
	if codec.calls.Load() != 1 {
		t.Fatalf("codec calls = %d, want one injected conversion", codec.calls.Load())
	}
	if len(result.ContentParts) != 1 {
		t.Fatalf("content parts = %#v, want one audio part", result.ContentParts)
	}
	audio, ok := result.ContentParts[0].(messages.AudioPart)
	if !ok || audio.MediaType != "audio/pcm" || len(audio.Bytes) == 0 {
		t.Fatalf("audio result = %#v, want non-empty audio/pcm", result.ContentParts[0])
	}
}

type serviceTestAudioCodec struct {
	calls atomic.Int32
}

func (c *serviceTestAudioCodec) Convert(ctx context.Context, request audiocodec.Request) (audiocodec.Result, error) {
	c.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return audiocodec.Result{}, err
	}
	if len(request.Input) == 0 || request.Limits.MaxInputBytes != audiocodec.DefaultMaxInputBytes {
		return audiocodec.Result{}, errors.New("filesystem adapter did not supply codec bounds")
	}
	return audiocodec.Result{
		PCM16:       []byte{0, 0, 1, 0},
		InputFormat: audiocodec.FormatWAV,
		SampleRate:  audiocodec.PCM16SampleRate,
		Channels:    audiocodec.PCM16Channels,
		Encoding:    audiocodec.PCM16Encoding,
	}, nil
}

type testExecutor struct{}

func (testExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: "call-1", Content: "ok"}, nil
}

func TestResolveInjectedExecutorKeepsHostScopeExplicit(t *testing.T) {
	capability, err := New().Resolve(context.Background(), public.Request{Executor: testExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	if capability.WorkspaceDir != "" || len(capability.AdditionalDirs) != 0 {
		t.Fatalf("injected executor acquired implicit host scope: %q %q", capability.WorkspaceDir, capability.AdditionalDirs)
	}
	if capability.Handle == nil || capability.Invoker == nil {
		t.Fatal("injected executor did not receive lifecycle and invocation handles")
	}
	result, err := capability.Invoker.Invoke(context.Background(), public.Invocation{ID: "call-1", Name: "host_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "call-1" || result.Content != "ok" {
		t.Fatalf("invocation result = %#v", result)
	}
	if err := capability.Handle.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	definitions, err := capability.Handle.RefreshDefinitions(context.Background())
	if err != nil || len(definitions) != 0 {
		t.Fatalf("refresh = %#v, %v", definitions, err)
	}
	if err := capability.Handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.Handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityHandleHonorsContext(t *testing.T) {
	capability, err := New().Resolve(context.Background(), public.Request{Executor: testExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := capability.Handle.Initialize(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Initialize returned %v, want context cancellation", err)
	}
	if _, err := capability.Handle.RefreshDefinitions(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("RefreshDefinitions returned %v, want context cancellation", err)
	}
}

func TestCapabilityHandleConvertsCleanupPanic(t *testing.T) {
	capability, err := New().Resolve(context.Background(), public.Request{
		Executor: testExecutor{},
		Browser:  &public.BrowserSurface{Close: func() error { panic("browser close") }},
	})
	if err != nil {
		t.Fatal(err)
	}
	closeErr := capability.Handle.Close()
	if !errors.Is(closeErr, public.ErrCapabilityClosePanic) {
		t.Fatalf("Close returned %v, want cleanup panic", closeErr)
	}
	if repeatErr := capability.Handle.Close(); !errors.Is(repeatErr, public.ErrCapabilityClosePanic) {
		t.Fatalf("repeated Close returned %v, want the recorded cleanup panic", repeatErr)
	}
}

func TestCapabilityHandleBoundsCleanupAndStillAllowsTheHookToFinish(t *testing.T) {
	release := make(chan struct{})
	capability, err := New().Resolve(context.Background(), public.Request{
		Executor: testExecutor{},
		Browser: &public.BrowserSurface{
			CloseTimeout: 5 * time.Millisecond,
			Close: func() error {
				<-release
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	closeErr := capability.Handle.Close()
	if !errors.Is(closeErr, public.ErrCapabilityCloseTimeout) {
		t.Fatalf("Close returned %v, want cleanup timeout", closeErr)
	}
	close(release)
}

type browserTestExecutor struct {
	calls atomic.Int32
	last  atomic.Value
}

func (e *browserTestExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls.Add(1)
	e.last.Store(call)
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: "browser"}, nil
}

func TestResolveComposesBrowserSurfaceAndOwnsLifecycle(t *testing.T) {
	browserExecutor := &browserTestExecutor{}
	var initializes, closes atomic.Int32
	refresh := func(context.Context) ([]messages.ToolDefinition, error) {
		return []messages.ToolDefinition{{Name: "page_tool"}}, nil
	}
	capability, err := New().Resolve(context.Background(), public.Request{
		Executor:    testExecutor{},
		Definitions: []messages.ToolDefinition{{Name: "show"}},
		Browser: &public.BrowserSurface{
			Executor: browserExecutor, Definitions: []messages.ToolDefinition{{Name: "show_page"}},
			RefreshDefinitions: refresh,
			Initialize:         func(context.Context) error { initializes.Add(1); return nil },
			Close:              func() error { closes.Add(1); return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if findDefinition(capability.Definitions, "show") || !findDefinition(capability.Definitions, "show_screen") {
		t.Fatalf("composed definitions = %#v, want page alias and host display split", capability.Definitions)
	}
	if _, err := capability.Executor.Execute(context.Background(), messages.ToolCall{ID: "page", Name: "show"}); err != nil {
		t.Fatal(err)
	}
	if browserExecutor.calls.Load() != 1 {
		t.Fatalf("browser calls = %d, want one through legacy page alias", browserExecutor.calls.Load())
	}
	if err := capability.Handle.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed, err := capability.Handle.RefreshDefinitions(context.Background())
	if err != nil || !findDefinition(refreshed, "page_tool") {
		t.Fatalf("refreshed definitions = %#v, err = %v", refreshed, err)
	}
	if err := capability.Handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.Handle.Close(); err != nil {
		t.Fatal(err)
	}
	if initializes.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("lifecycle calls = initialize:%d close:%d, want one each", initializes.Load(), closes.Load())
	}
}

func findDefinition(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}
