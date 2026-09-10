package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/fal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

type providerRoundTripper struct {
	status int
	body   string
	seen   *http.Request
	data   []byte
}

func (r *providerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.seen = request.Clone(request.Context())
	if request.Body != nil {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		r.data = data
	}
	return &http.Response{
		StatusCode: r.status,
		Status:     http.StatusText(r.status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    request,
	}, nil
}

func TestBuildOpenAIUsesConfigModelEndpointAndCredential(t *testing.T) {
	transport := &providerRoundTripper{
		status: http.StatusOK,
		body:   `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`,
	}
	service := New(&http.Client{Transport: transport}, nil, clock.Real{}, nil, catalog.New(), nil)
	built, err := service.Build(t.Context(), runtimeproviders.Config{
		Provider: "openai",
		Model:    "model-from-config",
		APIKey:   "secret-from-config",
		BaseURL:  "https://provider.example/v1",
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if _, ok := built.(*openai.OpenAIProvider); !ok {
		t.Fatalf("Build() type = %T, want *openai.OpenAIProvider", built)
	}

	response, err := built.Infer(t.Context(), llmproviders.InferenceRequest{
		Messages: []models.Message{models.NewTextMessage(models.RoleUser, "hello")},
	})
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}
	if response.Message.TextContent() != "ok" {
		t.Fatalf("response text = %q, want ok", response.Message.TextContent())
	}
	if transport.seen == nil {
		t.Fatal("HTTP client did not receive a request")
	}
	if transport.seen.URL.String() != "https://provider.example/v1/chat/completions" {
		t.Fatalf("request URL = %q", transport.seen.URL)
	}
	if got := transport.seen.Header.Get("Authorization"); got != "Bearer secret-from-config" {
		t.Fatalf("authorization = %q", got)
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(transport.data, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload.Model != "model-from-config" {
		t.Fatalf("request model = %q", payload.Model)
	}
}

func TestBuildUsesOpenAICompatibleProviderForNamedEndpoint(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	built, err := service.Build(t.Context(), runtimeproviders.Config{
		Provider: "openrouter",
		APIKey:   "configured-test-key",
		Model:    "compatible-model",
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if built.Name() != "openai" {
		t.Fatalf("provider name = %q, want openai-compatible implementation", built.Name())
	}
}

func TestBuildSelectsFalAndRequiresProviderConfiguration(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	if _, err := service.Build(t.Context(), runtimeproviders.Config{Provider: "fal", Model: fal.ModelQwenTTS}); err == nil {
		t.Fatal("Build() accepted fal without its provider configuration")
	}

	built, err := service.Build(t.Context(), runtimeproviders.Config{
		Provider: "fal",
		Model:    fal.ModelQwenTTS,
		Fal:      &runtimeproviders.FalConfig{APIKey: "fal-secret", BaseURL: "https://fal.example"},
	})
	if err != nil {
		t.Fatalf("Build() configured fal error = %v", err)
	}
	if _, ok := built.(*fal.FalProvider); !ok {
		t.Fatalf("Build() type = %T, want *fal.FalProvider", built)
	}
}

func TestBuildHonorsCanceledAdmission(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := service.Build(ctx, runtimeproviders.Config{Provider: "openai", Model: "model"})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("Build() error = %v, want canceled admission", err)
	}
}

type injectedModelCatalog struct {
	models []runtimeproviders.RealtimeModel
}

func (c injectedModelCatalog) RealtimeModels(provider string) []runtimeproviders.RealtimeModel {
	if !strings.EqualFold(strings.TrimSpace(provider), "openai") {
		return nil
	}
	return append([]runtimeproviders.RealtimeModel(nil), c.models...)
}

func (c injectedModelCatalog) LookupRealtimeModel(provider, model string) (runtimeproviders.RealtimeModel, bool) {
	for _, candidate := range c.RealtimeModels(provider) {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return runtimeproviders.RealtimeModel{}, false
}

func (c injectedModelCatalog) SupportedRealtimeModelIDs(provider string) []string {
	models := c.RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestProviderServiceSharesInjectedAdmissionDecision(t *testing.T) {
	injected := injectedModelCatalog{models: []runtimeproviders.RealtimeModel{{ID: "custom-only", SupportsAudio: true}}}

	service := New(nil, nil, clock.Real{}, nil, injected, nil)
	if err := service.ValidateSessionModel(" OpenAI ", " custom-only "); err != nil {
		t.Fatalf("provider service custom model = %v", err)
	}

	if err := service.ValidateSessionModel("openai", "custom-only"); err != nil {
		t.Fatalf("custom model admission = %v", err)
	}
	err := service.ValidateSessionModel("openai", runtimeproviders.OpenAIRealtimeLegacyModel)
	if err == nil || !errors.Is(err, runtimeproviders.ErrUnsupportedRealtimeModel) {
		t.Fatalf("built-in model error = %v, want typed rejection", err)
	}
	var unsupported *runtimeproviders.UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) || unsupported.Provider != "OpenAI" || len(unsupported.SupportedModels) != 1 || unsupported.SupportedModels[0] != "custom-only" {
		t.Fatalf("built-in model error = %v, want injected catalog snapshot", err)
	}
	unsupported.SupportedModels[0] = "mutated"
	retryErr := service.ValidateSessionModel("openai", runtimeproviders.OpenAIRealtimeLegacyModel)
	var retryUnsupported *runtimeproviders.UnsupportedRealtimeModelError
	if !errors.As(retryErr, &retryUnsupported) || retryUnsupported.SupportedModels[0] != "custom-only" {
		t.Fatalf("retry error snapshot = %v, want independent catalog values", retryErr)
	}
	if err := service.ValidateSessionModel("non-openai", "anything"); err != nil {
		t.Fatalf("non-OpenAI admission = %v", err)
	}
}

func TestProviderAdmissionNilCatalogRemainsTypedDependencyFailure(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, nil, nil)
	err := service.ValidateSessionModel("openai", runtimeproviders.OpenAIRealtimeLegacyModel)
	if !errors.Is(err, runtimeproviders.ErrModelCatalogRequired) || errors.Is(err, runtimeproviders.ErrUnsupportedRealtimeModel) {
		t.Fatalf("nil catalog error = %v, want only catalog-required", err)
	}
}
