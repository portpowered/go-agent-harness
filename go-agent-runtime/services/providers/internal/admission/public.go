package admission

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

// Service is the provider-owned admission façade. Its public methods are
// reached through a providers.ModelAdmission value returned by Wire; the
// decision itself remains in Admission.Decide.
type Service struct {
	delegate *Admission
}

type modelCatalogAdapter struct {
	catalog providers.ModelCatalog
}

func (a modelCatalogAdapter) LookupRealtimeModel(provider, model string) (Model, bool) {
	if a.catalog == nil {
		return Model{}, false
	}
	modelValue, ok := a.catalog.LookupRealtimeModel(provider, model)
	if !ok {
		return Model{}, false
	}
	return Model{
		ID:                      modelValue.ID,
		SupportsAudio:           modelValue.SupportsAudio,
		SupportsImageInput:      modelValue.SupportsImageInput,
		SupportsFunctionCalling: modelValue.SupportsFunctionCalling,
		SupportsReasoning:       modelValue.SupportsReasoning,
	}, true
}

func (a modelCatalogAdapter) SupportedRealtimeModelIDs(provider string) []string {
	if a.catalog == nil {
		return nil
	}
	return a.catalog.SupportedRealtimeModelIDs(provider)
}

// NewService composes one provider-owned admission implementation around the
// supplied catalog. A nil catalog remains a dependency failure.
func NewService(catalog providers.ModelCatalog) *Service {
	if catalog == nil {
		return &Service{delegate: New(nil)}
	}
	return &Service{delegate: New(modelCatalogAdapter{catalog: catalog})}
}

// ValidateSessionModel applies the canonical provider-service normalization.
func (s *Service) ValidateSessionModel(provider, model string) error {
	return s.ValidateRealtimeModel(provider, model, providers.ModelAdmissionOptions{TrimProvider: true, TrimModel: true})
}

// ValidateRealtimeModel applies the shared decision with explicit host-edge
// normalization and legacy error-presentation controls.
func (s *Service) ValidateRealtimeModel(provider, model string, options providers.ModelAdmissionOptions) error {
	if s == nil || s.delegate == nil {
		return providers.ErrModelCatalogRequired
	}
	decision := s.delegate.Decide(provider, model, Options{TrimProvider: options.TrimProvider, TrimModel: options.TrimModel})
	return decisionError(decision, options.PreserveModelInError)
}

// ResolveRealtimeModel returns metadata only for an admitted OpenAI realtime
// model. Non-OpenAI providers remain unrestricted for validation but have no
// catalog metadata on this surface.
func (s *Service) ResolveRealtimeModel(provider, model string, options providers.ModelAdmissionOptions) (providers.RealtimeModel, bool) {
	if s == nil || s.delegate == nil {
		return providers.RealtimeModel{}, false
	}
	decision := s.delegate.Decide(provider, model, Options{TrimProvider: options.TrimProvider, TrimModel: options.TrimModel})
	if !decision.Allowed || !decision.Matched {
		return providers.RealtimeModel{}, false
	}
	return providers.RealtimeModel{
		ID:                      decision.Model.ID,
		SupportsAudio:           decision.Model.SupportsAudio,
		SupportsImageInput:      decision.Model.SupportsImageInput,
		SupportsFunctionCalling: decision.Model.SupportsFunctionCalling,
		SupportsReasoning:       decision.Model.SupportsReasoning,
	}, true
}

func decisionError(decision Decision, preserveModel bool) error {
	if decision.MissingCatalog {
		return providers.ErrModelCatalogRequired
	}
	if decision.Allowed {
		return nil
	}
	model := decision.NormalizedModel
	if preserveModel {
		model = decision.RequestedModel
	}
	return &providers.UnsupportedRealtimeModelError{
		Provider:        "OpenAI",
		Model:           model,
		SupportedModels: append([]string(nil), decision.SupportedModels...),
	}
}

var _ providers.ModelAdmission = (*Service)(nil)
var _ providers.ModelAdmissionResolver = (*Service)(nil)
