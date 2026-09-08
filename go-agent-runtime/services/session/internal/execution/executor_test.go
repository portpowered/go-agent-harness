package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	session "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// stubInferencer is intentionally small: execution tests inject it through the
// neutral inferencer port instead of constructing a provider or reading host
// configuration.
type stubInferencer struct{}

func (stubInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "ok")}, nil
}

func (stubInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

// resolvedExecutorForTest crosses the same admission boundary as the public
// service: provider, model policy, workspace, and storage are all explicit.
// This helper must not grow a config-directory or environment fallback.
func resolvedExecutorForTest(t *testing.T, inf messages.Inferencer, storage Storage, tool messages.ToolExecutor, defs []messages.ToolDefinition) *Executor {
	t.Helper()
	if inf == nil {
		inf = stubInferencer{}
	}
	if storage == nil {
		storage = session.NewStorage(t.TempDir())
	}
	return NewExecutor(tool, defs, inf, true).WithResolution(RuntimeResolution{
		Resolved:       true,
		Provider:       ProviderConfig{Provider: "test", Model: "test-model"},
		Storage:        storage,
		WorkspaceDir:   storage.WorkspaceDir(),
		PromptResolved: true,
	})
}

func TestExecutorRequiresHostResolvedDependencies(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{}, true)
	if _, err := exec.BuildLoop(context.Background(), &Config{}); err == nil || !strings.Contains(err.Error(), "host-resolved dependencies") {
		t.Fatalf("BuildLoop() error = %v, want host admission error", err)
	}
	if _, err := exec.NewChatSessionID(&Config{}); err == nil || !strings.Contains(err.Error(), "host-resolved dependencies") {
		t.Fatalf("NewChatSessionID() error = %v, want host admission error", err)
	}
}

func TestExecutorResolutionCopiesInvocationValues(t *testing.T) {
	storage := session.NewStorage(t.TempDir())
	allowPaths := []string{"/one", "/two"}
	skillRoots := []runtimeTools.SkillRoot{{Directory: "/skills"}}
	exec := NewExecutor(nil, nil, stubInferencer{}, true).WithResolution(RuntimeResolution{
		Resolved:       true,
		Provider:       ProviderConfig{Provider: "openrouter", Model: "model", Fal: &FalProviderConfig{Model: "fal-model"}},
		ModelCatalog:   ModelCatalog{Models: []ModelInfo{{Name: "model", Aliases: []string{"alias"}}}},
		ModelPolicy:    ModelPolicy{ContinuationNudgeEnabled: true, ContinuationNudgeMessage: "continue", RepetitionPenalty: 1.2},
		Storage:        storage,
		WorkspaceDir:   "/workspace",
		AllowPaths:     allowPaths,
		SkillRoots:     skillRoots,
		PromptResolved: true,
	})
	allowPaths[0] = "mutated"
	skillRoots[0].Directory = "mutated"
	if exec.resolvedWorkspace != "/workspace" || exec.resolvedProvider.Fal == nil || exec.resolvedProvider.Fal.Model != "fal-model" {
		t.Fatalf("resolved provider/workspace = %+v/%q", exec.resolvedProvider, exec.resolvedWorkspace)
	}
	if exec.resolvedAllowPaths[0] != "/one" || len(exec.resolvedSkillRoots) != 1 || exec.resolvedSkillRoots[0].Directory != "/skills" {
		t.Fatalf("resolved slices were not copied: paths=%v skills=%v", exec.resolvedAllowPaths, exec.resolvedSkillRoots)
	}
	if got := exec.resolvedCatalog.Lookup("alias"); got == nil || got.Name != "model" {
		t.Fatalf("catalog alias lookup = %+v, want model", got)
	}
}

func TestBuildInferenceDefaultsForPenalty(t *testing.T) {
	for _, penalty := range []float64{0, 1} {
		if got := buildInferenceDefaultsForPenalty(penalty); got != nil {
			t.Fatalf("penalty %v defaults = %+v, want nil", penalty, got)
		}
	}
	got := buildInferenceDefaultsForPenalty(1.5)
	if got == nil || got.FrequencyPenalty == nil || *got.FrequencyPenalty != 1.5 {
		t.Fatalf("penalty defaults = %+v, want frequency penalty 1.5", got)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "prompt and continuation", cfg: Config{SystemPrompt: "prompt", ContinueLastSession: true}, want: "cannot use system prompt"},
		{name: "session and continuation", cfg: Config{SessionID: "session", ContinueLastSession: true}, want: "cannot use session ID"},
		{name: "session and prompt", cfg: Config{SessionID: "session", SystemPrompt: "prompt"}, want: "cannot use session ID and system prompt"},
		{name: "valid", cfg: Config{SystemPrompt: "prompt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
