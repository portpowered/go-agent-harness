package admission

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

type testCatalog struct {
	models []Model
}

func (c testCatalog) LookupRealtimeModel(provider, model string) (Model, bool) {
	if !strings.EqualFold(provider, openAIProvider) {
		return Model{}, false
	}
	for _, candidate := range c.models {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return Model{}, false
}

func (c testCatalog) SupportedRealtimeModelIDs(provider string) []string {
	if !strings.EqualFold(provider, openAIProvider) {
		return nil
	}
	ids := make([]string, 0, len(c.models))
	for _, model := range c.models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestDecidePreservesProviderPolicyAndBoundaryNormalization(t *testing.T) {
	catalog := testCatalog{models: []Model{{ID: "custom-only", SupportsAudio: true}}}
	service := New(catalog)

	known := service.Decide(" OpenAI ", " custom-only ", Options{TrimProvider: true, TrimModel: true})
	if !known.Allowed || !known.Matched || known.Model.ID != "custom-only" {
		t.Fatalf("known decision = %+v, want admitted custom model", known)
	}

	unknown := service.Decide("openai", "missing", Options{})
	if unknown.Allowed || unknown.Matched || unknown.NormalizedModel != "missing" {
		t.Fatalf("unknown decision = %+v, want rejection", unknown)
	}
	if len(unknown.SupportedModels) != 1 || unknown.SupportedModels[0] != "custom-only" {
		t.Fatalf("unknown supported models = %v", unknown.SupportedModels)
	}

	customProvider := service.Decide("custom", "anything", Options{})
	if !customProvider.Allowed || customProvider.Matched {
		t.Fatalf("custom provider decision = %+v, want unrestricted admission", customProvider)
	}

	raw := service.Decide("openai", " custom-only ", Options{})
	if raw.Allowed || raw.NormalizedModel != " custom-only " {
		t.Fatalf("raw decision = %+v, want exact model lookup", raw)
	}
}

func TestDecideCopiesSupportedModelSnapshot(t *testing.T) {
	service := New(testCatalog{models: []Model{{ID: "first"}, {ID: "second"}}})
	first := service.Decide("openai", "missing", Options{})
	first.SupportedModels[0] = "mutated"
	second := service.Decide("openai", "missing", Options{})
	if second.SupportedModels[0] != "first" {
		t.Fatalf("supported model snapshot was shared: %v", second.SupportedModels)
	}
}

func TestDecideFailsClosedForNilAdmissionAndCatalog(t *testing.T) {
	var service *Admission
	if decision := service.Decide("openai", "model", Options{}); !decision.MissingCatalog {
		t.Fatalf("nil receiver decision = %+v, want missing catalog", decision)
	}
	if decision := New(nil).Decide("openai", "model", Options{}); !decision.MissingCatalog {
		t.Fatalf("nil catalog decision = %+v, want missing catalog", decision)
	}
}

type providerCatalog struct {
	models []providers.RealtimeModel
}

func (c providerCatalog) RealtimeModels(provider string) []providers.RealtimeModel {
	if !strings.EqualFold(provider, openAIProvider) {
		return nil
	}
	return append([]providers.RealtimeModel(nil), c.models...)
}

func (c providerCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	for _, candidate := range c.RealtimeModels(provider) {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return providers.RealtimeModel{}, false
}

func (c providerCatalog) SupportedRealtimeModelIDs(provider string) []string {
	models := c.RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestPublicServiceMapsCatalogAndAdmissionDecisions(t *testing.T) {
	catalog := providerCatalog{models: []providers.RealtimeModel{{
		ID:                      "custom-only",
		SupportsAudio:           true,
		SupportsImageInput:      true,
		SupportsFunctionCalling: true,
		SupportsReasoning:       true,
	}}}
	service := NewService(catalog)

	if err := service.ValidateSessionModel(" OpenAI ", " custom-only "); err != nil {
		t.Fatalf("ValidateSessionModel() = %v, want admitted custom model", err)
	}

	preserved := service.ValidateRealtimeModel("openai", " missing ", providers.ModelAdmissionOptions{TrimModel: true, PreserveModelInError: true})
	var preservedUnsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(preserved, &preservedUnsupported) || preservedUnsupported.Model != " missing " || preservedUnsupported.SupportedModels[0] != "custom-only" {
		t.Fatalf("preserved rejection = %v, want raw model and catalog snapshot", preserved)
	}
	preservedUnsupported.SupportedModels[0] = "mutated"

	normalized := service.ValidateRealtimeModel("openai", " missing ", providers.ModelAdmissionOptions{TrimModel: true})
	var normalizedUnsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(normalized, &normalizedUnsupported) || normalizedUnsupported.Model != "missing" || normalizedUnsupported.SupportedModels[0] != "custom-only" {
		t.Fatalf("normalized rejection = %v, want normalized model and independent snapshot", normalized)
	}
	if err := service.ValidateRealtimeModel("anthropic", "anything", providers.ModelAdmissionOptions{}); err != nil {
		t.Fatalf("non-OpenAI validation = %v, want unrestricted admission", err)
	}

	resolved, ok := service.ResolveRealtimeModel(" OPENAI ", " custom-only ", providers.ModelAdmissionOptions{TrimProvider: true, TrimModel: true})
	if !ok || resolved.ID != "custom-only" || !resolved.SupportsAudio || !resolved.SupportsImageInput || !resolved.SupportsFunctionCalling || !resolved.SupportsReasoning {
		t.Fatalf("resolved model = %+v, %v, want complete custom metadata", resolved, ok)
	}
	if _, ok := service.ResolveRealtimeModel("openai", "missing", providers.ModelAdmissionOptions{}); ok {
		t.Fatal("unknown model resolved successfully")
	}
	if _, ok := service.ResolveRealtimeModel("anthropic", "anything", providers.ModelAdmissionOptions{}); ok {
		t.Fatal("non-OpenAI model returned catalog metadata")
	}
}

func TestPublicServiceHandlesNilDependenciesAndAdapterBranches(t *testing.T) {
	var nilService *Service
	if err := nilService.ValidateRealtimeModel("openai", "model", providers.ModelAdmissionOptions{}); !errors.Is(err, providers.ErrModelCatalogRequired) {
		t.Fatalf("nil service validation = %v, want catalog-required", err)
	}
	if _, ok := nilService.ResolveRealtimeModel("openai", "model", providers.ModelAdmissionOptions{}); ok {
		t.Fatal("nil service resolved a model")
	}
	if err := (&Service{}).ValidateRealtimeModel("openai", "model", providers.ModelAdmissionOptions{}); !errors.Is(err, providers.ErrModelCatalogRequired) {
		t.Fatalf("nil delegate validation = %v, want catalog-required", err)
	}

	var nilAdapter modelCatalogAdapter
	if _, ok := nilAdapter.LookupRealtimeModel("openai", "model"); ok {
		t.Fatal("nil adapter resolved a model")
	}
	if got := nilAdapter.SupportedRealtimeModelIDs("openai"); got != nil {
		t.Fatalf("nil adapter IDs = %v, want nil", got)
	}

	catalog := providerCatalog{models: []providers.RealtimeModel{{
		ID: "mapped", SupportsAudio: true, SupportsImageInput: true,
		SupportsFunctionCalling: true, SupportsReasoning: true,
	}}}
	adapter := modelCatalogAdapter{catalog: catalog}
	if _, ok := adapter.LookupRealtimeModel("openai", "missing"); ok {
		t.Fatal("adapter resolved an unknown model")
	}
	mapped, ok := adapter.LookupRealtimeModel("openai", "mapped")
	if !ok || mapped.ID != "mapped" || !mapped.SupportsAudio || !mapped.SupportsImageInput || !mapped.SupportsFunctionCalling || !mapped.SupportsReasoning {
		t.Fatalf("mapped model = %+v, %v, want all copied fields", mapped, ok)
	}
	if got := adapter.SupportedRealtimeModelIDs("openai"); len(got) != 1 || got[0] != "mapped" {
		t.Fatalf("adapter IDs = %v, want mapped", got)
	}
}

func TestPublicServiceDecisionErrorPolicies(t *testing.T) {
	if err := decisionError(Decision{MissingCatalog: true}, false); !errors.Is(err, providers.ErrModelCatalogRequired) {
		t.Fatalf("missing decision error = %v, want catalog-required", err)
	}
	if err := decisionError(Decision{Allowed: true}, false); err != nil {
		t.Fatalf("allowed decision error = %v, want nil", err)
	}

	decision := Decision{
		RequestedModel:  " raw ",
		NormalizedModel: "raw",
		SupportedModels: []string{"first", "second"},
	}
	rawErr := decisionError(decision, true)
	var rawUnsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(rawErr, &rawUnsupported) || rawUnsupported.Provider != "OpenAI" || rawUnsupported.Model != " raw " {
		t.Fatalf("raw decision error = %v, want preserved request", rawErr)
	}
	rawUnsupported.SupportedModels[0] = "changed"
	normalizedErr := decisionError(decision, false)
	var normalizedUnsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(normalizedErr, &normalizedUnsupported) || normalizedUnsupported.Model != "raw" || normalizedUnsupported.SupportedModels[0] != "first" {
		t.Fatalf("normalized decision error = %v, want normalized independent snapshot", normalizedErr)
	}
}
