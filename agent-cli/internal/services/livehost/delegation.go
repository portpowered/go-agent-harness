package livehost

import (
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

// delegationInputs are the host values a delegation policy is built from.
type delegationInputs struct {
	// loaded is the configuration before command-line overrides, whose
	// --model and --base-url name the voice session, not the backend.
	loaded *config.Config
	// configDir locates the `yui auth chatgpt` store.
	configDir string
	// voiceProvider is the live provider; voiceAPIKey is its resolved key.
	voiceProvider string
	voiceAPIKey   string
	// credentialReference registers a raw key and returns its opaque
	// reference, so no secret enters the live request.
	credentialReference func(string) string
}

// delegationPolicy resolves the backend that answers a GPT-Live session's
// client delegations (gpt-live-provider.md Q8, chatgpt-oauth.md 3.5): an
// explicit session.delegation.provider; otherwise openai-chatgpt with the
// account's default model when a ChatGPT login exists; otherwise
// model.provider, or openai when model.provider is a voice-only provider.
// Only openai-live delegates, so other providers get no policy.
func delegationPolicy(in delegationInputs) *livedelegation.Policy {
	if in.voiceProvider != config.ProviderOpenAILive || in.loaded == nil {
		return nil
	}
	var settings config.SessionDelegationConfig
	if in.loaded.Session != nil && in.loaded.Session.Delegation != nil {
		settings = *in.loaded.Session.Delegation
	}
	provider := strings.ToLower(strings.TrimSpace(settings.Provider))
	if provider == "" {
		provider = defaultDelegationProvider(in)
	}
	backend := delegationBackend(in, provider)
	if model := strings.TrimSpace(settings.Model); model != "" {
		backend.Model = model
	}
	return &livedelegation.Policy{
		Backend: backend,
		Limits: livedelegation.Limits{
			Concurrency: settings.MaxConcurrency,
			MaxTurns:    settings.MaxTurns,
			MaxDuration: time.Duration(settings.MaxDurationSeconds) * time.Second,
			MaxTokens:   settings.MaxTokens,
		},
	}
}

func defaultDelegationProvider(in delegationInputs) string {
	if chatGPTLoginExists(in.configDir) {
		return config.ProviderOpenAIChatGPT
	}
	switch provider := strings.ToLower(strings.TrimSpace(in.loaded.Model.Provider)); provider {
	case config.ProviderOpenAI, config.ProviderOpenRouter, config.ProviderLocal, config.ProviderOpenAIChatGPT:
		return provider
	default:
		// A voice-only provider (openai-live, grok) cannot reason in text.
		return config.ProviderOpenAI
	}
}

func chatGPTLoginExists(configDir string) bool {
	if strings.TrimSpace(configDir) == "" {
		return false
	}
	info, err := os.Stat(config.ChatGPTAuthStorePath(configDir))
	return err == nil && info.Mode().IsRegular()
}

// delegationBackend fills the provider's model, endpoint and credential from
// its configuration block.
func delegationBackend(in delegationInputs, provider string) livedelegation.Backend {
	backend := livedelegation.Backend{Provider: provider}
	model := in.loaded.Model
	var block *config.OpenAIConfig
	switch provider {
	case config.ProviderOpenAIChatGPT:
		if model.OpenAIChatGPT != nil {
			backend.Model, backend.BaseURL = model.OpenAIChatGPT.Model, model.OpenAIChatGPT.BaseURL
		}
		backend.ChatGPTAuthPath = config.ChatGPTAuthStorePath(in.configDir)
		return backend
	case config.ProviderOpenAI:
		block = model.OpenAI
	case config.ProviderOpenRouter:
		block = model.OpenRouter
	case config.ProviderLocal:
		block = model.Local
	}
	apiKey := ""
	if block != nil {
		backend.Model, backend.BaseURL, apiKey = block.Model, block.BaseURL, block.APIKey
	}
	if apiKey == "" && provider == config.ProviderOpenAI {
		// GPT-Live on an API key authenticates with the OpenAI key too.
		apiKey = in.voiceAPIKey
	}
	if apiKey != "" && in.credentialReference != nil {
		backend.CredentialReference = in.credentialReference(apiKey)
	}
	return backend
}
