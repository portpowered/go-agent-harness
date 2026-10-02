package catalog

import (
	"strings"

	providers "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

// Catalog owns the built-in model metadata. It has no mutable package state;
// each method returns a fresh value for request isolation.
type Catalog struct{}

func New() *Catalog { return &Catalog{} }

// RealtimeModels lists the built-in models of provider. An empty provider
// means "openai"; a provider with no catalog has no models.
func (*Catalog) RealtimeModels(provider string) []providers.RealtimeModel {
	provider = strings.TrimSpace(provider)
	switch {
	case provider == "" || strings.EqualFold(provider, "openai"):
		return openAIRealtimeModels()
	case strings.EqualFold(provider, providers.OpenAILiveProvider):
		return openAILiveModels()
	default:
		return nil
	}
}

func openAIRealtimeModels() []providers.RealtimeModel {
	return []providers.RealtimeModel{
		{ID: providers.OpenAIRealtimeLegacyModel, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: providers.OpenAIRealtimeDefaultModel, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: providers.OpenAIRealtime21Model, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true, SupportsReasoning: true},
	}
}

// openAILiveModels is the GPT-Live catalog. Both models take audio and text
// only, never call a tool themselves, and reach tools through client
// delegation, so they report no function calling of their own.
// gpt-live-1-codex is the same model family on the ChatGPT login.
func openAILiveModels() []providers.RealtimeModel {
	return []providers.RealtimeModel{
		{ID: providers.OpenAILive1Model, SupportsAudio: true, Duplex: true, Delegation: providers.RealtimeDelegationClient},
		{ID: providers.OpenAILiveCodexModel, SupportsAudio: true, Duplex: true, Delegation: providers.RealtimeDelegationClient},
	}
}

func (c *Catalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	for _, supported := range c.RealtimeModels(provider) {
		if supported.ID == model {
			return supported, true
		}
	}
	return providers.RealtimeModel{}, false
}

func (c *Catalog) SupportedRealtimeModelIDs(provider string) []string {
	models := c.RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}
