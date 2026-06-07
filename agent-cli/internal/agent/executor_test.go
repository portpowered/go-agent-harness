package agent

import (
	"context"
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

	exec := NewExecutor(nil, nil, stubInferencer{})

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
