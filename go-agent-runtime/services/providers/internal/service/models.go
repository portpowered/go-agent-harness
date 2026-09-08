package service

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

func (s *Service) ValidateSessionModel(provider, model string) error {
	if s == nil || s.catalog == nil {
		return providers.ErrModelCatalogRequired
	}
	if strings.EqualFold(strings.TrimSpace(provider), "openai") {
		provider = strings.TrimSpace(provider)
		model = strings.TrimSpace(model)
		if _, ok := s.catalog.LookupRealtimeModel(provider, model); !ok {
			return &providers.UnsupportedRealtimeModelError{
				Provider: "OpenAI", Model: model, SupportedModels: s.catalog.SupportedRealtimeModelIDs(provider),
			}
		}
	}
	return nil
}

func (s *Service) RealtimeModels(provider string) []providers.RealtimeModel {
	if s == nil || s.catalog == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(provider), "openai") {
		return s.catalog.RealtimeModels(strings.TrimSpace(provider))
	}
	return nil
}

func (s *Service) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	if s == nil || s.catalog == nil {
		return providers.RealtimeModel{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "openai") {
		return providers.RealtimeModel{}, false
	}
	return s.catalog.LookupRealtimeModel(strings.TrimSpace(provider), strings.TrimSpace(model))
}

func (s *Service) SupportedRealtimeModelIDs(provider string) []string {
	if s == nil || s.catalog == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "openai") {
		return nil
	}
	return s.catalog.SupportedRealtimeModelIDs(strings.TrimSpace(provider))
}
