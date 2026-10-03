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
	// voiceProvider is the live provider; voiceAPIKey and voiceBaseURL are
	// its resolved key and the endpoint that key was issued for.
	voiceProvider string
	voiceAPIKey   string
	voiceBaseURL  string
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
	if backend.Model == "" && provider != config.ProviderOpenAIChatGPT {
		// Only openai-chatgpt has an account default; a voice model inherited
		// from the provider block was dropped, so name the setting to fix.
		backend.Unconfigured = "no text model for delegation provider " + provider +
			": set session.delegation.model (model." + strings.ReplaceAll(provider, "-", "_") + ".model is unset or a voice model)"
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
		backend.BaseURL, apiKey = block.BaseURL, block.APIKey
		if !isVoiceModel(block.Model) {
			backend.Model = block.Model
		}
	}
	if apiKey == "" && provider == config.ProviderOpenAI && sameOpenAIEndpoint(backend.BaseURL, in.voiceBaseURL) {
		// GPT-Live on an API key authenticates with the OpenAI key too, but a
		// key issued for a custom voice endpoint never goes anywhere else.
		apiKey = in.voiceAPIKey
	}
	if apiKey != "" && in.credentialReference != nil {
		backend.CredentialReference = in.credentialReference(apiKey)
	}
	return backend
}

// isVoiceModel reports a Realtime or GPT-Live model, which cannot serve a
// text backend: model.openai.model often names one for voice sessions.
func isVoiceModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "realtime") || strings.HasPrefix(model, liveModelPrefix)
}

// defaultOpenAIBaseURL is the endpoint an empty OpenAI base URL selects.
const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// sameOpenAIEndpoint compares two OpenAI base URLs, an empty one meaning the
// default endpoint.
func sameOpenAIEndpoint(a, b string) bool {
	normalize := func(value string) string {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			return defaultOpenAIBaseURL
		}
		return strings.ToLower(value)
	}
	return normalize(a) == normalize(b)
}

// delegationGuidance tells a GPT-Live voice model how delegated work comes
// back (gpt-live-provider.md 1.10.1: results are appended to its context
// tagged with the delegation, and it must not guess while it waits).
const delegationGuidance = "## Delegated work\n" +
	"When you delegate a request, tell the user briefly that you are working on it and keep the conversation going. " +
	"Do not guess the result while waiting. The result arrives later as context for that delegation: " +
	"deliver it in your own words, and if it reports a failure, say so and offer to try again."

// codexChannelGuidance describes the gpt-live-1-codex context channels, as
// OpenClaw does: commentary is silent background and speakable is the answer.
const codexChannelGuidance = "Delegation context arrives on two channels. " +
	"Commentary is silent background: use it to stay informed, and never read it aloud. " +
	"Speakable is the answer to deliver: say it to the user in your own words."

// withDelegationGuidance appends the delegation guidance to a GPT-Live
// session's instructions, with the channel description on the codex route.
func withDelegationGuidance(instructions, provider, model string) string {
	if provider != config.ProviderOpenAILive {
		return instructions
	}
	guidance := delegationGuidance
	if strings.TrimSpace(model) == config.OpenAILiveChatGPTModel {
		guidance += "\n" + codexChannelGuidance
	}
	if strings.TrimSpace(instructions) == "" {
		return guidance
	}
	return strings.TrimRight(instructions, "\n") + "\n\n" + guidance
}
