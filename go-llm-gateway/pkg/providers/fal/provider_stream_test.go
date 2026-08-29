package fal

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestFalProvider_InferStream_ReturnsUnsupportedFeatureError(t *testing.T) {
	transport := &mockTransport{statusCode: 200, body: "{}"}
	p := New(WithHTTPClient(&http.Client{Transport: transport}))
	ctx := context.Background()
	req := providers.InferenceRequest{
		Model: ModelLTXAudioToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "p"},
				models.AudioPart{URL: "https://example.com/a.mp3"},
			},
		}},
	}

	ch, err := p.InferStream(ctx, req)
	if ch != nil {
		t.Fatalf("InferStream() channel = %#v, want nil", ch)
	}
	var unsupported *providers.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("InferStream() error = %v, want UnsupportedFeatureError", err)
	}
	if unsupported.Provider != "fal" {
		t.Fatalf("Provider = %q, want fal", unsupported.Provider)
	}
	if unsupported.Feature != capabilities.FeatureStreaming {
		t.Fatalf("Feature = %q, want streaming", unsupported.Feature)
	}
	if unsupported.RequestedMode != capabilities.RequestedModeStatelessStream {
		t.Fatalf("RequestedMode = %q, want stateless_stream", unsupported.RequestedMode)
	}
	if unsupported.Capability.State != capabilities.CapabilityStateUnsupported {
		t.Fatalf("Capability.State = %q, want unsupported", unsupported.Capability.State)
	}
	if unsupported.Capability.Detail == "" {
		t.Fatal("Capability.Detail is empty")
	}
	if transport.lastReq != nil {
		t.Fatal("InferStream() attempted HTTP request for unsupported streaming")
	}
}
