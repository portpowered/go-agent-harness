package admission

import (
	"strings"
	"testing"
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
