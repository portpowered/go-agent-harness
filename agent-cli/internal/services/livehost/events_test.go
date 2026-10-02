package livehost

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
)

func openAIConfig(provider string) config.Config {
	return config.Config{Model: config.ModelConfig{
		Provider: provider,
		OpenAI:   &config.OpenAIConfig{Model: "gpt-realtime-2.1", APIKey: "sk-openai", BaseURL: "https://api.openai.com/v1"},
	}}
}

// GPT-Live keeps its own provider name, defaults to gpt-live-1 rather than
// model.openai.model, uses the OpenAI key, and its endpoint is
// /v1/live/sessions, never /realtime.
func TestProviderValuesKeepOpenAILiveWithItsDefaultModelAndEndpoint(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     config.Config
		request serviceSession.Request
	}{
		"flag":            {cfg: openAIConfig(config.ProviderOpenAI), request: serviceSession.Request{Provider: "openai-live", ProviderProvided: true}},
		"config provider": {cfg: openAIConfig(config.ProviderOpenAILive)},
	} {
		t.Run(name, func(t *testing.T) {
			provider, model, apiKey, baseURL, err := ProviderValues(tc.cfg, tc.request, nil)
			if err != nil {
				t.Fatalf("ProviderValues: %v", err)
			}
			if provider != config.ProviderOpenAILive || model != "gpt-live-1" || apiKey != "sk-openai" {
				t.Fatalf("ProviderValues = %q %q %q, want openai-live gpt-live-1 with the OpenAI key", provider, model, apiKey)
			}
			if got := realtimeEndpoint(provider, baseURL); got != "wss://api.openai.com/v1/live/sessions" {
				t.Fatalf("endpoint = %q, want the live sessions path", got)
			}
		})
	}
	if got := realtimeEndpoint(config.ProviderOpenAILive, "http://127.0.0.1:9/v1/live/sessions/"); got != "ws://127.0.0.1:9/v1/live/sessions/" {
		t.Fatalf("explicit live endpoint = %q, want it unchanged apart from the scheme", got)
	}
	if got := realtimeEndpoint(config.ProviderOpenAI, "https://api.openai.com/v1"); got != "wss://api.openai.com/v1/realtime" {
		t.Fatalf("OpenAI Realtime endpoint = %q, want the unchanged /realtime rule", got)
	}
}

// With no API key, even with only a ChatGPT sign-in, openai-live fails before
// any session is built, naming the key it needs.
func TestProviderValuesRefuseOpenAILiveWithoutAnAPIKey(t *testing.T) {
	cfg := config.Config{Model: config.ModelConfig{Provider: config.ProviderOpenAILive}}
	_, _, _, _, err := ProviderValues(cfg, serviceSession.Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires an OpenAI API key") || !strings.Contains(err.Error(), "AGENT_MODEL__OPENAI__API_KEY") {
		t.Fatalf("ProviderValues without a key = %v, want the API-key refusal", err)
	}
	withFlag := serviceSession.Request{Provider: config.ProviderOpenAILive, APIKey: "sk-flag", Model: "gpt-live-1", ModelProvided: true}
	if _, model, apiKey, _, err := ProviderValues(cfg, withFlag, nil); err != nil || apiKey != "sk-flag" || model != "gpt-live-1" {
		t.Fatalf("ProviderValues with --api-key = %q %q %v", model, apiKey, err)
	}
}
