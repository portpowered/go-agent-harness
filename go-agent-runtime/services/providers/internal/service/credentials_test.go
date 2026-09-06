package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestHostedProviderAdmissionRejectsMissingCredential(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	for _, endpoint := range []string{"", "https://api.openai.com/v1", "https://openrouter.ai/api/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := service.Build(t.Context(), providers.Config{Provider: "openai", BaseURL: endpoint}); err == nil || !strings.Contains(err.Error(), "API key") {
				t.Fatalf("Build missing credential: %v", err)
			}
			if _, err := service.BuildSession(t.Context(), providers.SessionConfig{Provider: "openai", Model: providers.OpenAIRealtimeLegacyModel, BaseURL: endpoint}); err == nil || !strings.Contains(err.Error(), "openai realtime api key is missing") {
				t.Fatalf("BuildSession missing credential: %v", err)
			}
		})
	}
}

func TestProviderAdmissionAllowsExplicitAnonymousEndpoint(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	if _, err := service.Build(t.Context(), providers.Config{Provider: "local", BaseURL: "http://localhost:8080/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildSession(t.Context(), providers.SessionConfig{Provider: "local", Model: "local", RealtimeURL: "ws://localhost:8080/realtime"}); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionCredential(providers.SessionConfig{ReplayPath: "offline-capture.json"}, "openai"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAdmissionRejectsNilContext(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	//lint:ignore SA1012 Exercise the public admission contract's nil-context rejection.
	if _, err := service.Build(nil, providers.Config{}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestProviderModelAdmissionRejectsNonRealtimeOpenAIModel(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	err := service.ValidateSessionModel("openai", "gpt-4o")
	if err == nil || !strings.Contains(err.Error(), "not realtime-capable") {
		t.Fatalf("ValidateSessionModel error = %v", err)
	}
	var unsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("ValidateSessionModel error = %v, want typed unsupported model", err)
	}
}

func TestBuildSessionNormalizesBlankProviderBeforeModelAdmission(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	_, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: "  ", Model: "gpt-4o", APIKey: "test-key",
	})
	if err == nil || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("BuildSession blank provider error = %v, want unsupported model", err)
	}
	var unsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) || unsupported.Provider != "OpenAI" {
		t.Fatalf("BuildSession blank provider error = %v, want typed OpenAI model error", err)
	}
}

func TestProviderModelAdmissionTrimsProviderAndModel(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	if err := service.ValidateSessionModel("  OpenAI ", "  "+providers.OpenAIRealtimeLegacyModel+" "); err != nil {
		t.Fatalf("ValidateSessionModel whitespace = %v", err)
	}
}

func TestProviderModelAdmissionLeavesCustomProvidersUnrestricted(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	if err := service.ValidateSessionModel("custom", "my-realtime-model"); err != nil {
		t.Fatalf("custom model admission = %v", err)
	}
}

func TestProviderModelCatalogReturnsIndependentValues(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New())
	models := service.RealtimeModels("openai")
	if len(models) != 3 || models[0].ID != providers.OpenAIRealtimeLegacyModel {
		t.Fatalf("realtime catalog = %+v", models)
	}
	models[0].ID = "mutated"
	model, ok := service.LookupRealtimeModel("openai", providers.OpenAIRealtimeLegacyModel)
	if !ok || model.ID != providers.OpenAIRealtimeLegacyModel {
		t.Fatalf("catalog was mutated through returned values: %+v, %v", model, ok)
	}
	ids := service.SupportedRealtimeModelIDs("openai")
	if len(ids) != 3 || ids[0] != providers.OpenAIRealtimeLegacyModel {
		t.Fatalf("realtime model IDs = %v", ids)
	}
}
