package embedding_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

func TestEmbeddedPromptIsLiteralAndDoesNotDiscoverHostFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "prompt.txt")
	for _, name := range []string{path, filepath.Join(directory, "AGENTS.md")} {
		if err := os.WriteFile(name, []byte("host file must not become the system prompt"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(directory)
	for _, prompt := range []string{"", path} {
		t.Run(prompt, func(t *testing.T) {
			requests := make(chan messages.InferenceRequest, 1)
			model := &hostInferencer{response: "response", requests: requests}
			host := sessionwire.NewService(sessionwire.Dependencies{Inferencer: model, RelaxValidation: true})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := host.Run(ctx, session.Request{
				Input: agentloop.ExecuteInput{Message: "hello"}, SystemPrompt: prompt,
			}); err != nil {
				t.Fatal(err)
			}
			request := readInference(t, ctx, requests)
			assertLiteralSystemPrompt(t, request, prompt)
		})
	}
}

func assertLiteralSystemPrompt(t *testing.T, request messages.InferenceRequest, prompt string) {
	t.Helper()
	var system []string
	for _, message := range request.Messages {
		if message.Role == messages.RoleSystem {
			system = append(system, message.TextContent())
		}
	}
	if prompt == "" && len(system) == 0 {
		return
	}
	if len(system) != 1 || system[0] != prompt {
		t.Fatalf("system messages = %q; want literal prompt %q", system, prompt)
	}
}
