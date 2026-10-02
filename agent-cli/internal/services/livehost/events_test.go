package livehost

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
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

// A configured session.model names a Realtime model; openai-live ignores it
// and keeps gpt-live-1, but takes a GPT-Live model from it.
func TestOpenAILiveIgnoresARealtimeSessionModel(t *testing.T) {
	for sessionModel, want := range map[string]string{"gpt-realtime-2.1": "gpt-live-1", "gpt-live-2": "gpt-live-2"} {
		cfg := openAIConfig(config.ProviderOpenAILive)
		cfg.Session = &config.SessionConfig{Model: sessionModel}
		_, model, _, _, err := ProviderValues(cfg, serviceSession.Request{}, nil)
		if err != nil || model != want {
			t.Fatalf("session.model %q: model = %q, %v; want %q", sessionModel, model, err, want)
		}
	}
	cfg := openAIConfig(config.ProviderOpenAI)
	cfg.Session = &config.SessionConfig{Model: "gpt-realtime-2.1-mini"}
	if _, model, _, _, err := ProviderValues(cfg, serviceSession.Request{}, nil); err != nil || model != "gpt-realtime-2.1-mini" {
		t.Fatalf("openai session.model = %q, %v; want it honoured", model, err)
	}
}

// With a ChatGPT login and no --model, openai-live runs gpt-live-1-codex on
// the login, ahead of a configured API key, and the live request names the
// store; an explicit --model gpt-live-1 still takes the API key.
func TestProviderValuesPickTheCodexModelOnAChatGPTLogin(t *testing.T) {
	configDir := t.TempDir()
	if err := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(configDir)).Save(chatgptauth.Credential{
		AccessToken: "token", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := openAIConfig(config.ProviderOpenAILive)
	provider, model, apiKey, _, err := ProviderValues(cfg, serviceSession.Request{ConfigDir: configDir}, nil)
	if err != nil || provider != config.ProviderOpenAILive || model != config.OpenAILiveChatGPTModel || apiKey != "" {
		t.Fatalf("ProviderValues with a login = %q %q %q, %v; want gpt-live-1-codex without the API key", provider, model, apiKey, err)
	}
	if path := chatGPTAuthPath(provider, model, configDir); path != config.ChatGPTAuthStorePath(configDir) {
		t.Fatalf("ChatGPT store = %q", path)
	}
	explicit := serviceSession.Request{ConfigDir: configDir, Model: config.OpenAILiveAPIKeyModel, ModelProvided: true}
	if _, model, apiKey, _, err := ProviderValues(cfg, explicit, nil); err != nil || model != config.OpenAILiveAPIKeyModel || apiKey != "sk-openai" {
		t.Fatalf("ProviderValues --model gpt-live-1 = %q %q, %v; want the API key", model, apiKey, err)
	}
	if path := chatGPTAuthPath(config.ProviderOpenAILive, config.OpenAILiveAPIKeyModel, configDir); path != "" {
		t.Fatalf("an API-key model names the store %q", path)
	}
	withoutLogin := serviceSession.Request{ConfigDir: t.TempDir(), Model: config.OpenAILiveChatGPTModel, ModelProvided: true}
	if _, _, _, _, err := ProviderValues(cfg, withoutLogin, nil); err == nil || !strings.Contains(err.Error(), "yui auth chatgpt") {
		t.Fatalf("ProviderValues --model gpt-live-1-codex without a login = %v", err)
	}
}
