package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

type stubInferencer struct{}

func (stubInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, "ok"),
	}, nil
}

func (stubInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

func TestLoadConfig_WithInferencerOverrideSkipsCredentialValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	cfg, err := exec.loadConfig(&Config{})
	if err != nil {
		t.Fatalf("loadConfig() with inferencer override returned error: %v", err)
	}

	if cfg.Model.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter default", cfg.Model.Provider)
	}
	if cfg.Model.OpenRouter == nil {
		t.Fatal("expected default openrouter config")
	}
}

func TestLoadSystemPromptWithDetails_DefaultAgentsMDReportsFilesystemSideEffects(t *testing.T) {
	workspaceDir := t.TempDir()
	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
		NoSystemInformation: true,
	}, workspaceDir, []messages.ToolDefinition{{
		Name:        "read_file",
		Description: "Read a file.",
	}})
	if err != nil {
		t.Fatalf("LoadSystemPromptWithDetails() error = %v", err)
	}
	if !strings.Contains(prompt, "read_file") {
		t.Fatalf("prompt missing generated AGENTS.md tool content: %s", prompt)
	}

	agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
	if _, err := os.Stat(agentsPath); err != nil {
		t.Fatalf("AGENTS.md was not created: %v", err)
	}
	assertPromptSource(t, details, PromptSourceKindAgentsMD, agentsPath)
	assertPromptSideEffect(t, details, PromptSideEffectCreateAgentsMD)
	assertPromptSideEffect(t, details, PromptSideEffectReadAgentsMD)
	assertPromptSideEffect(t, details, PromptSideEffectReadSkillsMetadata)
	assertNoPromptSideEffect(t, details, PromptSideEffectLoadConfig)
	assertNoPromptSideEffect(t, details, PromptSideEffectCollectSystemInfo)
}

func TestLoadSystemPromptWithDetails_PromptFileReportsReadWithoutAgentsMD(t *testing.T) {
	workspaceDir := t.TempDir()
	promptPath := filepath.Join(workspaceDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("file prompt"), 0644); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
		SystemPrompt:        promptPath,
		NoSystemInformation: true,
	}, workspaceDir, nil)
	if err != nil {
		t.Fatalf("LoadSystemPromptWithDetails() error = %v", err)
	}
	if prompt != "file prompt" {
		t.Fatalf("prompt = %q, want file prompt", prompt)
	}

	assertPromptSource(t, details, PromptSourceKindPromptFile, promptPath)
	assertPromptSideEffect(t, details, PromptSideEffectReadPromptFile)
	assertNoPromptSideEffect(t, details, PromptSideEffectCreateAgentsMD)
	assertNoPromptSideEffect(t, details, PromptSideEffectReadAgentsMD)
}

func TestLoadSystemPromptWithDetails_SkillsSummaryReportsSkillSources(t *testing.T) {
	workspaceDir := t.TempDir()
	skillDir := filepath.Join(workspaceDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: test-skill
description: A test skill.
---
# Test skill
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0644); err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(nil, nil, stubInferencer{}, true)

	prompt, details, err := exec.LoadSystemPromptWithDetails(&Config{
		SystemPrompt:        "base prompt",
		NoSystemInformation: true,
	}, workspaceDir, nil)
	if err != nil {
		t.Fatalf("LoadSystemPromptWithDetails() error = %v", err)
	}
	if !strings.Contains(prompt, "test-skill") || !strings.Contains(prompt, "A test skill.") {
		t.Fatalf("prompt missing skills summary: %s", prompt)
	}

	assertPromptSource(t, details, PromptSourceKindLiteralPrompt, "")
	assertPromptSource(t, details, PromptSourceKindSkills, filepath.Join(workspaceDir, "skills"))
	assertPromptSideEffect(t, details, PromptSideEffectReadSkillsMetadata)
}

func assertPromptSource(t *testing.T, details PromptResolutionDetails, kind, path string) {
	t.Helper()
	for _, source := range details.Sources {
		if source.Kind == kind && source.Path == path {
			return
		}
	}
	t.Fatalf("missing prompt source kind=%q path=%q in %+v", kind, path, details.Sources)
}

func assertPromptSideEffect(t *testing.T, details PromptResolutionDetails, effect PromptResolutionSideEffect) {
	t.Helper()
	for _, got := range details.SideEffects {
		if got == effect {
			return
		}
	}
	t.Fatalf("missing prompt side effect %q in %+v", effect, details.SideEffects)
}

func assertNoPromptSideEffect(t *testing.T, details PromptResolutionDetails, effect PromptResolutionSideEffect) {
	t.Helper()
	for _, got := range details.SideEffects {
		if got == effect {
			t.Fatalf("unexpected prompt side effect %q in %+v", effect, details.SideEffects)
		}
	}
}
