package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func writeExecutorConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func validOpenRouterConfig(apiKey string) string {
	return "model:\n" +
		"  provider: openrouter\n" +
		"  openrouter:\n" +
		"    model: gpt-4o\n" +
		"    api_key: " + apiKey + "\n"
}

func TestExecutorRequest_S4ConfigAssemblyAndErrorBranches(t *testing.T) {
	t.Run("overrides are assembled and validated", func(t *testing.T) {
		dir := t.TempDir()
		exec := NewExecutor(nil, nil, nil)
		loaded, err := exec.loadConfig(&Config{
			ConfigDir: dir,
			APIKey:    "override-key",
			Model:     "override-model",
			Provider:  "openrouter",
			BaseURL:   "https://example.test/v1",
		})
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}
		if loaded.Model.Provider != "openrouter" || loaded.Model.OpenRouter == nil {
			t.Fatalf("loaded provider config = %+v, want openrouter config", loaded.Model)
		}
		got := loaded.Model.OpenRouter
		if got.APIKey != "override-key" || got.Model != "override-model" || got.BaseURL != "https://example.test/v1" {
			t.Fatalf("overrides = %+v, want all CLI values", got)
		}
	})

	t.Run("validation can be disabled explicitly", func(t *testing.T) {
		exec := NewExecutor(nil, nil, nil)
		if _, err := exec.loadConfigWithOptions(&Config{ConfigDir: t.TempDir()}, false); err != nil {
			t.Fatalf("loadConfigWithOptions(validate=false) error = %v", err)
		}
	})

	t.Run("relaxed validation accepts missing credentials", func(t *testing.T) {
		exec := NewExecutor(nil, nil, nil, true)
		if _, err := exec.loadConfig(&Config{ConfigDir: t.TempDir()}); err != nil {
			t.Fatalf("relaxed loadConfig() error = %v", err)
		}
	})

	t.Run("load failure preserves context", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config-parent-is-a-file")
		if err := os.WriteFile(configPath, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		exec := NewExecutor(nil, nil, nil)
		_, err := exec.loadConfig(&Config{ConfigDir: configPath})
		if err == nil || !strings.Contains(err.Error(), "failed to load config") {
			t.Fatalf("loadConfig() error = %v, want load context", err)
		}
	})
}

func TestExecutorRequest_SessionStorageAndInitialHistoryBranches(t *testing.T) {
	workspace := t.TempDir()
	storage := session.NewStorage(workspace)
	wantHistory := []messages.Message{messages.NewTextMessage(messages.RoleUser, "saved")}
	if err := storage.Save("saved", wantHistory); err != nil {
		t.Fatalf("save session: %v", err)
	}

	exec := NewExecutor(nil, nil, stubInferencer{}, true)
	history, id, err := exec.getInitialHistory(&Config{SessionID: "saved"}, storage)
	if err != nil {
		t.Fatalf("session-id history error = %v", err)
	}
	if id != "saved" || len(history) != 1 || history[0].Role != messages.RoleUser || history[0].TextContent() != "saved" {
		t.Fatalf("session-id result = (%q, %#v), want saved user message", id, history)
	}

	latestHistory, latestID, err := exec.getInitialHistory(&Config{ContinueLastSession: true}, storage)
	if err != nil {
		t.Fatalf("continue-last history error = %v", err)
	}
	if latestID != "saved" || len(latestHistory) != 1 || latestHistory[0].Role != messages.RoleUser || latestHistory[0].TextContent() != "saved" {
		t.Fatalf("latest result = (%q, %#v), want saved user message", latestID, latestHistory)
	}

	initial := []messages.Message{messages.NewTextMessage(messages.RoleUser, "initial")}
	initialHistory, initialID, err := exec.getInitialHistory(&Config{InitialHistory: initial}, storage)
	if err != nil {
		t.Fatalf("provided history error = %v", err)
	}
	if len(initialHistory) != 1 || initialHistory[0].Role != messages.RoleUser || initialHistory[0].TextContent() != "initial" || initialID == "" {
		t.Fatalf("provided history result = (%q, %#v), want non-empty ID and exact history", initialID, initialHistory)
	}
	oversized := make([]messages.Message, 256)
	for i := range oversized {
		oversized[i] = messages.NewTextMessage(messages.RoleUser, "message")
	}
	oversizedHistory, oversizedID, err := exec.getInitialHistory(&Config{InitialHistory: oversized}, storage)
	if err != nil || oversizedID == "" || len(oversizedHistory) != len(oversized) || oversizedHistory[255].TextContent() != "message" {
		t.Fatalf("oversized history result = (%q, %d, %v), want all 256 messages", oversizedID, len(oversizedHistory), err)
	}

	emptyHistory, emptyID, err := exec.getInitialHistory(&Config{}, storage)
	if err != nil || emptyHistory != nil || emptyID == "" {
		t.Fatalf("empty history result = (%q, %#v, %v), want new ID and nil history", emptyID, emptyHistory, err)
	}

	noSessions := session.NewStorage(t.TempDir())
	_, _, err = exec.getInitialHistory(&Config{ContinueLastSession: true}, noSessions)
	if err == nil || !strings.Contains(err.Error(), "no previous session to continue") {
		t.Fatalf("empty continue error = %v, want no-previous-session context", err)
	}

	badWorkspace := t.TempDir()
	badSessionsDir := filepath.Join(badWorkspace, "sessions")
	if err := os.MkdirAll(badSessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badSessionsDir, "session-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = exec.getInitialHistory(&Config{SessionID: "bad"}, session.NewStorage(badWorkspace))
	if err == nil || !strings.Contains(err.Error(), "load session bad") {
		t.Fatalf("malformed session error = %v, want session context", err)
	}

	latestBrokenRoot := t.TempDir()
	latestBrokenSessions := filepath.Join(latestBrokenRoot, "sessions")
	if err := os.MkdirAll(latestBrokenSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(latestBrokenSessions, "session-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = exec.getInitialHistory(&Config{ContinueLastSession: true}, session.NewStorage(latestBrokenRoot))
	if err == nil || !strings.Contains(err.Error(), "load latest session") {
		t.Fatalf("malformed latest error = %v, want latest-session context", err)
	}
}

func TestExecutorRequest_StorageExportsUseConfiguredWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "configured")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(nil, nil, stubInferencer{}, true)
	storage, err := exec.GetSessionStorage(&Config{ConfigDir: dir, WorkDir: dir})
	if err != nil {
		t.Fatalf("GetSessionStorage() error = %v", err)
	}
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if storage.WorkspaceDir() != absDir {
		t.Fatalf("workspace = %q, want %q", storage.WorkspaceDir(), absDir)
	}

	newID, err := exec.NewChatSessionID(&Config{ConfigDir: dir, WorkDir: dir})
	if err != nil || newID == "" {
		t.Fatalf("NewChatSessionID() = %q, %v; want non-empty ID", newID, err)
	}
}

func TestExecutorRequest_SystemPromptErrorAndSideEffectBranches(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	t.Run("directory prompt reports read error", func(t *testing.T) {
		_, details, err := exec.LoadSystemPromptWithDetails(&Config{
			SystemPrompt:        t.TempDir(),
			NoSystemInformation: true,
		}, t.TempDir(), nil)
		if err == nil || !strings.Contains(err.Error(), "read system prompt") {
			t.Fatalf("directory prompt error = %v, want read context", err)
		}
		if len(details.Sources) != 1 || details.Sources[0].Kind != PromptSourceKindPromptFile {
			t.Fatalf("prompt details = %+v, want prompt-file source before read failure", details)
		}
	})

	t.Run("long prose remains literal after failed stat", func(t *testing.T) {
		longPrompt := strings.Repeat("Preserve this literal: spaces, punctuation !?; path-like fragments /tmp/not-a-file.md and ./missing-prompt.txt.\n", 16)
		if len(longPrompt) < 1024 || len(longPrompt) > 2048 {
			t.Fatalf("long prompt length = %d, want 1-2 KB", len(longPrompt))
		}
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			SystemPrompt:        longPrompt,
			NoSystemInformation: true,
		}, t.TempDir(), nil)
		if err != nil || prompt != longPrompt {
			t.Fatalf("long literal result = %q, %v; want exact literal", prompt, err)
		}
		assertPromptSource(t, details, PromptSourceKindLiteralPrompt, "")
		assertNoPromptSideEffect(t, details, PromptSideEffectReadPromptFile)
	})

	t.Run("invalid prompt syntax remains literal", func(t *testing.T) {
		invalidPrompt := "invalid\x00prompt"
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			SystemPrompt:        invalidPrompt,
			NoSystemInformation: true,
		}, t.TempDir(), nil)
		if err != nil || prompt != invalidPrompt {
			t.Fatalf("invalid literal result = %q, %v; want exact literal", prompt, err)
		}
		assertPromptSource(t, details, PromptSourceKindLiteralPrompt, "")
		assertNoPromptSideEffect(t, details, PromptSideEffectReadPromptFile)
	})

	t.Run("default workspace failure is wrapped", func(t *testing.T) {
		workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
		if err := os.WriteFile(workspaceFile, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := exec.LoadSystemPromptWithDetails(&Config{
			NoSystemInformation: true,
		}, workspaceFile, nil)
		if err == nil || !strings.Contains(err.Error(), "read AGENTS.md") {
			t.Fatalf("default workspace error = %v, want read context", err)
		}
	})

	t.Run("existing agents file is reused", func(t *testing.T) {
		workspaceDir := t.TempDir()
		agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
		if err := os.WriteFile(agentsPath, []byte("existing agents"), 0o644); err != nil {
			t.Fatal(err)
		}
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			NoSystemInformation: true,
		}, workspaceDir, nil)
		if err != nil || prompt != "existing agents" {
			t.Fatalf("existing agents result = %q, %v", prompt, err)
		}
		for _, effect := range details.SideEffects {
			if effect == PromptSideEffectCreateAgentsMD {
				t.Fatal("existing AGENTS.md unexpectedly reported create side effect")
			}
		}
	})

	t.Run("none prompt and suffix handle empty base", func(t *testing.T) {
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			SystemPrompt:        "none",
			NoSystemInformation: true,
			SystemPromptSuffix:  "suffix only",
		}, "", nil)
		if err != nil || prompt != "suffix only" {
			t.Fatalf("none/suffix result = %q, %v", prompt, err)
		}
		assertPromptSource(t, details, PromptSourceKindSuffix, "")
		assertPromptSideEffect(t, details, PromptSideEffectAppendPromptSuffix)
	})

	t.Run("nil details remains safe", func(t *testing.T) {
		prompt, err := exec.loadSystemPrompt(&Config{
			SystemPrompt:        "literal prompt",
			NoSystemInformation: true,
		}, "", nil, nil)
		if err != nil || prompt != "literal prompt" {
			t.Fatalf("nil-details result = %q, %v", prompt, err)
		}
	})

	t.Run("system information does not manufacture an absent prompt", func(t *testing.T) {
		dir := t.TempDir()
		writeExecutorConfig(t, dir, "model:\n  provider: fal\n  fal:\n    model: fal-test-model\n    api_key: test-key\n")
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			ConfigDir: dir,
		}, t.TempDir(), nil)
		if err != nil || prompt != "" {
			t.Fatalf("absent prompt = %q, %v; want empty", prompt, err)
		}
		assertNoPromptSideEffect(t, details, PromptSideEffectCollectSystemInfo)
	})

	t.Run("config skills source is recorded", func(t *testing.T) {
		workspaceDir := t.TempDir()
		configDir := t.TempDir()
		prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
			ConfigDir:           configDir,
			SystemPrompt:        "base",
			NoSystemInformation: true,
		}, workspaceDir, nil)
		if err != nil || prompt != "base" {
			t.Fatalf("config skills prompt = %q, %v", prompt, err)
		}
		want := filepath.Join(configDir, "skills")
		assertPromptSource(t, details, PromptSourceKindSkills, want)
	})
}
