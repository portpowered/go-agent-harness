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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
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

// TestBuildSessionConnectsTheLiveProviderWithTheAPIKey builds an admitted
// openai-live session and connects it to the fake GPT-Live server, which
// requires the API-key bearer on the default endpoint.
func TestBuildSessionConnectsTheLiveProviderWithTheAPIKey(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	fake := fakelive.New(fakelive.WithAPIKey("test-key"))
	inferencer, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILive1Model, APIKey: "test-key",
		WebSocketDialer: fake.Dialer(),
	})
	if err != nil {
		t.Fatalf("BuildSession(openai-live): %v", err)
	}
	session, err := inferencer.ConnectSession(t.Context())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	calls := fake.DialCalls()
	if len(calls) != 1 || calls[0].Endpoint != openailive.DefaultEndpoint {
		t.Fatalf("dial calls = %+v, want one dial of the default live endpoint", calls)
	}
	if _, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: "gpt-realtime", APIKey: "test-key",
	}); !errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("BuildSession(openai-live, gpt-realtime) = %v, want admission rejection first", err)
	}
}

// TestBuildSessionRefusesTheLiveProviderWithoutAnAPIKey pins that gpt-live-1
// takes only an API key: with none, the build fails before any dial.
func TestBuildSessionRefusesTheLiveProviderWithoutAnAPIKey(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	fake := fakelive.New()
	_, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILive1Model, WebSocketDialer: fake.Dialer(),
	})
	if err == nil || !strings.Contains(err.Error(), "requires an OpenAI API key") {
		t.Fatalf("BuildSession without a key = %v, want the API-key refusal", err)
	}
	if calls := fake.DialCalls(); len(calls) != 0 {
		t.Fatalf("BuildSession dialed %d times, want none", len(calls))
	}
}

func TestLiveSessionsEndpointFollowsTheConfiguredURL(t *testing.T) {
	for _, tc := range []struct{ realtime, base, want string }{
		{want: openailive.DefaultEndpoint},
		{base: "https://api.openai.com/v1", want: openailive.DefaultEndpoint},
		{base: "http://127.0.0.1:8080/v1/", want: "ws://127.0.0.1:8080/v1/live/sessions"},
		{realtime: "wss://example.test/v1/live/sessions", base: "https://ignored.test/v1", want: "wss://example.test/v1/live/sessions"},
		{realtime: "wss://example.openai.azure.com/openai/v1", want: "wss://example.openai.azure.com/openai/v1/live/sessions"},
		{base: "not a url", want: "not a url"},
	} {
		if got := liveSessionsEndpoint(providers.SessionConfig{RealtimeURL: tc.realtime, BaseURL: tc.base}); got != tc.want {
			t.Errorf("liveSessionsEndpoint(%q, %q) = %q, want %q", tc.realtime, tc.base, got, tc.want)
		}
	}
}
