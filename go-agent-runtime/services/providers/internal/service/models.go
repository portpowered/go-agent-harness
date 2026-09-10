package service

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/admission"
)

func (s *Service) ValidateSessionModel(provider, model string) error {
	if s == nil {
		return providers.ErrModelCatalogRequired
	}
	return admission.NewService(s.catalog).ValidateSessionModel(provider, model)
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
	return admission.NewService(s.catalog).ResolveRealtimeModel(provider, model, providers.ModelAdmissionOptions{
		TrimProvider: true,
		TrimModel:    true,
	})
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
