package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestHostedProviderAdmissionRejectsMissingCredential(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
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
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
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

func TestSessionDialerRequiresExplicitProviderCaptureRole(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	_, _, err := service.sessionDialer(providers.SessionConfig{RecordPath: "capture.json"}, "local", "local", clock.Real{})
	if err == nil || !strings.Contains(err.Error(), "provider capture service is required") {
		t.Fatalf("sessionDialer error = %v, want explicit provider capture role error", err)
	}
}

func TestSessionDialerUsesExplicitBoundedProviderCaptureRole(t *testing.T) {
	captures := &providerCaptureStub{sink: &providerCaptureSinkStub{}}
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), captures)
	service.recording = recordingServiceStub{}
	dialer, writer, err := service.sessionDialer(providers.SessionConfig{RecordPath: "capture.json"}, "local", "local", clock.Real{})
	if err != nil {
		t.Fatalf("sessionDialer error = %v", err)
	}
	if captures.options.Destination != "capture.json" {
		t.Fatalf("capture destination = %q, want capture.json", captures.options.Destination)
	}
	if _, ok := dialer.(*gatewaytesting.StreamingRecordingWebSocketDialer); !ok {
		t.Fatalf("dialer = %T, want bounded streaming recorder", dialer)
	}
	if writer == nil {
		t.Fatal("session writer is nil")
	}
}

func TestSessionDialerRejectsNilProviderCaptureSink(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), &providerCaptureStub{})
	service.recording = recordingServiceStub{}
	_, _, err := service.sessionDialer(providers.SessionConfig{RecordPath: "capture.json"}, "local", "local", clock.Real{})
	if err == nil || !strings.Contains(err.Error(), "provider capture service returned a nil sink") {
		t.Fatalf("sessionDialer error = %v, want nil sink error", err)
	}
}

type recordingServiceStub struct{ recording.Service }

type providerCaptureStub struct {
	options recording.ProviderCaptureOptions
	sink    recording.ProviderCaptureSink
}

func (s *providerCaptureStub) OpenProviderCapture(options recording.ProviderCaptureOptions) (recording.ProviderCaptureSink, error) {
	s.options = options
	return s.sink, nil
}

type providerCaptureSinkStub struct{}

func (*providerCaptureSinkStub) Append(gatewaytesting.CapturedSessionEvent) error { return nil }
func (*providerCaptureSinkStub) Commit(int) error                                 { return nil }
func (*providerCaptureSinkStub) Discard(int) error                                { return nil }
func (*providerCaptureSinkStub) FlushToFile(string, gatewaytesting.SessionCapture) error {
	return nil
}
func (*providerCaptureSinkStub) Abort() error { return nil }

func TestProviderAdmissionRejectsNilContext(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	//lint:ignore SA1012 Exercise the public admission contract's nil-context rejection.
	if _, err := service.Build(nil, providers.Config{}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestProviderModelAdmissionRejectsNonRealtimeOpenAIModel(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
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
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
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
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	if err := service.ValidateSessionModel("  OpenAI ", "  "+providers.OpenAIRealtimeLegacyModel+" "); err != nil {
		t.Fatalf("ValidateSessionModel whitespace = %v", err)
	}
}

func TestProviderModelAdmissionLeavesCustomProvidersUnrestricted(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	if err := service.ValidateSessionModel("custom", "my-realtime-model"); err != nil {
		t.Fatalf("custom model admission = %v", err)
	}
}

func TestProviderModelAdmissionDistinguishesMissingCatalog(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, nil, nil)
	err := service.ValidateSessionModel("openai", providers.OpenAIRealtimeLegacyModel)
	if !errors.Is(err, providers.ErrModelCatalogRequired) || errors.Is(err, providers.ErrUnsupportedRealtimeModel) {
		t.Fatalf("missing catalog admission = %v, want dependency error", err)
	}
	_, err = service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: "openai", Model: providers.OpenAIRealtimeLegacyModel, APIKey: "test-key",
	})
	if !errors.Is(err, providers.ErrModelCatalogRequired) {
		t.Fatalf("BuildSession missing catalog = %v, want dependency error", err)
	}
}

func TestProviderModelCatalogReturnsIndependentValues(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
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
