package livehost

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessioninstructionswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions/wire"
)

func TestAssembleLiveRequestWaitForCloseOverridesFiniteAudioPolicy(t *testing.T) {
	inputs := requestInputs{
		effective: config.Config{},
		provider:  "openai",
		model:     "gpt-realtime",
	}
	for _, test := range []struct {
		name          string
		waitForClose  bool
		audioInput    bool
		promptPresent bool
		wantFinite    bool
		wantResponses int
	}{
		{name: "finite audio", audioInput: true, wantFinite: true, wantResponses: 1},
		{name: "finite prompt", promptPresent: true, wantFinite: true},
		{name: "provider owns close", waitForClose: true, audioInput: true, wantFinite: false, wantResponses: 1},
		{name: "provider owns prompted close", waitForClose: true, promptPresent: true, wantFinite: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := serviceSession.Request{
				WaitForClose: test.waitForClose,
				AudioInput:   serviceSession.AudioInput{Present: test.audioInput},
			}
			caseInputs := inputs
			caseInputs.promptPresent = test.promptPresent
			got := assembleLiveRequest(request, caseInputs)
			if got.FinishAfterResponse != test.wantFinite {
				t.Fatalf("FinishAfterResponse = %t, want %t", got.FinishAfterResponse, test.wantFinite)
			}
			if got.ExpectedResponses != test.wantResponses {
				t.Fatalf("ExpectedResponses = %d, want %d", got.ExpectedResponses, test.wantResponses)
			}
		})
	}
}

func TestBuildRequestUsesWireInstructionCompositionBeforeProviderStartup(t *testing.T) {
	workspace := t.TempDir()
	request := serviceSession.Request{
		Provider: "openai", Model: "gpt-realtime-2.1", APIKey: "test-key",
		ProviderProvided: true, ModelProvided: true, WorkDir: workspace,
		SystemPrompt: "literal prompt", LoadedConfig: &config.Config{},
	}
	got, err := BuildRequest(context.Background(), request, nil, RequestDependencies{
		InstructionService: sessioninstructionswire.NewInstructionService(),
		PageSightToolID:    "show_page",
		Capabilities: func(*config.Config) (*runtimeSession.LiveCapabilities, error) {
			return &runtimeSession.LiveCapabilities{Definitions: []messages.ToolDefinition{{Name: "read_file"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !strings.HasPrefix(got.Instructions, "literal prompt\n\nFilesystem scope: ") {
		t.Fatalf("instructions = %q, want resolved prompt and scope", got.Instructions)
	}
	if !strings.Contains(got.Instructions, "Tool-grounding requirements:") {
		t.Fatalf("instructions = %q, want runtime tool policy", got.Instructions)
	}
	if strings.Contains(got.Instructions, "Sight routing requirements:") {
		t.Fatalf("non-browser request received sight policy: %q", got.Instructions)
	}
}

func TestBuildRequestPreservesEmptyAndRejectsInvalidWorkspace(t *testing.T) {
	capabilities := func(*config.Config) (*runtimeSession.LiveCapabilities, error) {
		return nil, nil
	}
	empty, err := BuildRequest(context.Background(), serviceSession.Request{
		Provider: "openai", Model: "gpt-realtime-2.1", APIKey: "test-key", ProviderProvided: true,
		ModelProvided: true, LoadedConfig: &config.Config{},
	}, nil, RequestDependencies{Capabilities: capabilities})
	if err != nil {
		t.Fatalf("empty prompt BuildRequest: %v", err)
	}
	if empty.Instructions != "" {
		t.Fatalf("empty prompt instructions = %q, want empty", empty.Instructions)
	}
	invalid := filepath.Join(t.TempDir(), "missing")
	_, err = BuildRequest(context.Background(), serviceSession.Request{
		Provider: "openai", Model: "gpt-realtime-2.1", APIKey: "test-key", ProviderProvided: true,
		ModelProvided: true, WorkDir: invalid, LoadedConfig: &config.Config{},
	}, nil, RequestDependencies{Capabilities: capabilities})
	if err == nil || !strings.Contains(err.Error(), "resolve live filesystem scope") {
		t.Fatalf("invalid workspace error = %v, want scoped configuration error", err)
	}
}
