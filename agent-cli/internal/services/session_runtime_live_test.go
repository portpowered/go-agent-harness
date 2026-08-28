package services

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestPlanSessionRuntime_BrowserToolsUsesUnrecordedLiveRuntime(t *testing.T) {
	dialer := &stubRuntimeDialer{id: "browser-live"}
	var gotConfig config.GrokConfig
	var gotDefinitions []messages.ToolDefinition
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return dialer },
		newGrokSessionWithTools: func(cfg config.GrokConfig, gotDialer transport.Dialer, definitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
			if gotDialer != dialer {
				t.Fatalf("browser live inferencer dialer = %v, want factory-owned dialer", gotDialer)
			}
			gotConfig = cfg
			gotDefinitions = append([]messages.ToolDefinition(nil), definitions...)
			return &scriptedSessionInferencer{}, nil
		},
	}
	loaded := &config.Config{
		Model: config.ModelConfig{
			Provider: config.ProviderGrok,
			Grok:     &config.GrokConfig{Model: "grok-browser-test", APIKey: "test-key"},
		},
	}
	definitions := []messages.ToolDefinition{{Name: "browser_test"}}
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		Provider:            config.ProviderGrok,
		BrowserToolsEnabled: true,
		LoadedConfig:        loaded,
		ToolDefinitions:     definitions,
	}, factory)
	if err != nil {
		t.Fatalf("plan browser live runtime: %v", err)
	}
	if plan.mode != sessionRuntimeModeInjectedLive || plan.provider != config.ProviderGrok || plan.model != "grok-browser-test" {
		t.Fatalf("browser live plan identity = mode:%q provider:%q model:%q", plan.mode, plan.provider, plan.model)
	}
	if plan.capturePath != "" || plan.flushCapture != nil || plan.finalize != nil {
		t.Fatalf("browser live plan unexpectedly owns capture lifecycle: %+v", plan)
	}
	if plan.inferencer == nil || plan.loop.CloseAfterOpen == false || plan.loop.AdvertiseToolDefinitions == false {
		t.Fatalf("browser live plan lifecycle = %+v", plan.loop)
	}
	if gotConfig.Model != "grok-browser-test" || gotConfig.APIKey != "test-key" || len(gotDefinitions) != 1 || gotDefinitions[0].Name != "browser_test" {
		t.Fatalf("browser live provider inputs = %+v/%v", gotConfig, gotDefinitions)
	}
}

func TestPlanSessionRuntime_BrowserToolsRejectsUnsupportedProvider(t *testing.T) {
	_, err := planSessionRuntimeWithFactory(SessionRunOptions{
		Provider:            config.ProviderOpenRouter,
		BrowserToolsEnabled: true,
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return &stubRuntimeDialer{id: "unused"} },
	})
	if err == nil || !strings.Contains(err.Error(), "--browser-tools") || !strings.Contains(err.Error(), config.ProviderGrok) || !strings.Contains(err.Error(), config.ProviderOpenAI) {
		t.Fatalf("unsupported browser provider error = %v", err)
	}
}
