package bareadmission

import "strings"

// RealtimeModels returns an independent copy of the entries for provider.
func (c ModelCatalog) RealtimeModels(provider string) []Model {
	models := make([]Model, 0, len(c.Models))
	for _, model := range c.Models {
		if strings.EqualFold(strings.TrimSpace(model.Provider), strings.TrimSpace(provider)) {
			models = append(models, model)
		}
	}
	return models
}

// LookupRealtimeModel returns an independent value for an exact model ID.
// Provider names are normalized at this value boundary; model IDs retain
// their case-sensitive provider semantics.
func (c ModelCatalog) LookupRealtimeModel(provider, model string) (Model, bool) {
	for _, candidate := range c.Models {
		if strings.EqualFold(strings.TrimSpace(candidate.Provider), strings.TrimSpace(provider)) && candidate.ID == model {
			return candidate, true
		}
	}
	return Model{}, false
}

// SupportedRealtimeModelIDs returns a fresh, ordered list for provider.
func (c ModelCatalog) SupportedRealtimeModelIDs(provider string) []string {
	models := c.RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}
