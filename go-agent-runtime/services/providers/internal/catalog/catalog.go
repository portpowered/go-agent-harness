package catalog

import (
	"strings"

	providers "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

// Catalog owns the built-in model metadata. It has no mutable package state;
// each method returns a fresh value for request isolation.
type Catalog struct{}

func New() *Catalog { return &Catalog{} }

func (*Catalog) RealtimeModels(provider string) []providers.RealtimeModel {
	provider = strings.TrimSpace(provider)
	if provider != "" && !strings.EqualFold(provider, "openai") {
		return nil
	}
	return []providers.RealtimeModel{
		{ID: providers.OpenAIRealtimeLegacyModel, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: providers.OpenAIRealtimeDefaultModel, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: providers.OpenAIRealtime21Model, SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true, SupportsReasoning: true},
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
