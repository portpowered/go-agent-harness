package wire

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

const customProvider = "openai"

// customCatalog is an independently supplied catalog with one OpenAI realtime model.
type customCatalog struct{}

func (customCatalog) RealtimeModels(provider string) []providers.RealtimeModel {
	if provider != customProvider {
		return nil
	}
	return []providers.RealtimeModel{{ID: "custom-rt", SupportsAudio: true}}
}

func (c customCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	for _, candidate := range c.RealtimeModels(provider) {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return providers.RealtimeModel{}, false
}

func (c customCatalog) SupportedRealtimeModelIDs(provider string) []string {
	ids := []string{}
	for _, candidate := range c.RealtimeModels(provider) {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func TestNewModelAdmissionUsesSuppliedCatalog(t *testing.T) {
	admission := NewModelAdmission(customCatalog{})
	if err := admission.ValidateSessionModel(customProvider, "custom-rt"); err != nil {
		t.Fatalf("registered custom model rejected: %v", err)
	}
	err := admission.ValidateSessionModel(customProvider, "missing")
	var unsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("unregistered model error = %v, want UnsupportedRealtimeModelError", err)
	}
	if len(unsupported.SupportedModels) != 1 || unsupported.SupportedModels[0] != "custom-rt" {
		t.Fatalf("supported models = %v, want the custom catalog", unsupported.SupportedModels)
	}
	model, ok := admission.ResolveRealtimeModel(customProvider, "custom-rt", providers.ModelAdmissionOptions{})
	if !ok || !model.SupportsAudio {
		t.Fatalf("resolved model = %+v/%v, want the custom catalog entry", model, ok)
	}
}
