package service

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// Provider labels of UnsupportedRealtimeModelError.
const (
	openAIProviderLabel     = "OpenAI"
	openAILiveProviderLabel = "OpenAI Live"
)

func TestOpenAILiveAdmitsOnlyCataloguedModels(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	for _, provider := range []string{providers.OpenAILiveProvider, " OpenAI-Live "} {
		if err := service.ValidateSessionModel(provider, " gpt-live-1 "); err != nil {
			t.Fatalf("ValidateSessionModel(%q, gpt-live-1) = %v, want admitted", provider, err)
		}
	}
	for _, model := range []string{providers.OpenAIRealtimeDefaultModel, "gpt-live-2", "anything"} {
		err := service.ValidateSessionModel(providers.OpenAILiveProvider, model)
		var unsupported *providers.UnsupportedRealtimeModelError
		if !errors.As(err, &unsupported) || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
			t.Fatalf("ValidateSessionModel(openai-live, %q) = %v, want a typed rejection", model, err)
		}
		if unsupported.Provider != openAILiveProviderLabel || !reflect.DeepEqual(unsupported.SupportedModels, []string{providers.OpenAILive1Model}) {
			t.Fatalf("rejection = %+v, want the OpenAI Live catalog", unsupported)
		}
	}
}

func TestOpenAIRealtimeDoesNotAdmitTheLiveModel(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	err := service.ValidateSessionModel("openai", providers.OpenAILive1Model)
	var unsupported *providers.UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) || unsupported.Provider != openAIProviderLabel || slices.Contains(unsupported.SupportedModels, providers.OpenAILive1Model) {
		t.Fatalf("ValidateSessionModel(openai, gpt-live-1) = %v, want rejection by the Realtime catalog", err)
	}
}

func TestOpenAILiveCatalogDescribesADuplexClientDelegationModel(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	model, ok := service.LookupRealtimeModel(" openai-live ", providers.OpenAILive1Model)
	want := providers.RealtimeModel{
		ID: providers.OpenAILive1Model, SupportsAudio: true, Duplex: true, Delegation: providers.RealtimeDelegationClient,
	}
	if !ok || model != want {
		t.Fatalf("LookupRealtimeModel = %+v, %v; want %+v", model, ok, want)
	}
	if models := service.RealtimeModels(providers.OpenAILiveProvider); len(models) != 1 || models[0] != want {
		t.Fatalf("RealtimeModels(openai-live) = %+v", models)
	}
	if ids := service.SupportedRealtimeModelIDs(providers.OpenAILiveProvider); !reflect.DeepEqual(ids, []string{providers.OpenAILive1Model}) {
		t.Fatalf("SupportedRealtimeModelIDs(openai-live) = %v", ids)
	}
	for _, realtime := range service.RealtimeModels("openai") {
		if realtime.Duplex || realtime.Delegation != "" {
			t.Fatalf("Realtime model %+v is marked duplex", realtime)
		}
	}
	if models := service.RealtimeModels("grok"); models != nil {
		t.Fatalf("RealtimeModels(grok) = %+v, want no catalog", models)
	}
}

// TestBuildSessionDoesNotReachTheLiveProviderYet pins that an admitted
// openai-live session is still refused before any connection is dialed.
func TestBuildSessionDoesNotReachTheLiveProviderYet(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	fake := fakelive.New()
	_, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILive1Model, APIKey: "test-key",
		WebSocketDialer: fake.Dialer(),
	})
	if err == nil || !strings.Contains(err.Error(), `do not support provider "openai-live"`) {
		t.Fatalf("BuildSession(openai-live) = %v, want the unsupported-provider refusal", err)
	}
	if calls := fake.DialCalls(); len(calls) != 0 {
		t.Fatalf("BuildSession dialed %d times, want none", len(calls))
	}
	if _, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: "gpt-realtime", APIKey: "test-key",
	}); !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("BuildSession(openai-live, gpt-realtime) = %v, want admission rejection first", err)
	}
}
