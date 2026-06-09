package inference

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// captureGateway records the InferenceRequest it receives so tests can inspect it.
type captureGateway struct {
	captured gateway.InferenceRequest
}

func (g *captureGateway) Infer(_ context.Context, req gateway.InferenceRequest) (gateway.InferenceResponse, error) {
	g.captured = req
	return gateway.InferenceResponse{
		Message: models.Message{Role: "assistant"},
		Usage:   models.TokenUsage{TotalTokens: 1},
	}, nil
}

func (g *captureGateway) InferStream(_ context.Context, req gateway.InferenceRequest) (<-chan messages.StreamMessage, error) {
	g.captured = req
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

func (g *captureGateway) Interact(_ context.Context, _ gateway.InteractionRequest) (<-chan gateway.InteractionEvent, error) {
	ch := make(chan gateway.InteractionEvent)
	close(ch)
	return ch, nil
}

type capabilityProvider struct {
	name       string
	caps       gateway.ProviderCapabilities
	inferCalls int
}

func (p *capabilityProvider) Name() string {
	return p.name
}

func (p *capabilityProvider) Capabilities() gateway.ProviderCapabilities {
	return p.caps
}

func (p *capabilityProvider) Infer(context.Context, providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{
		Message: models.NewTextMessage(models.RoleAssistant, "should not call"),
	}, nil
}

func (p *capabilityProvider) InferStream(context.Context, providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.inferCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }

func TestInfer_PassthroughMaxTokens(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	req := messages.InferenceRequest{MaxTokens: intPtr(1024)}
	_, err := gi.Infer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gw.captured.MaxTokens == nil || *gw.captured.MaxTokens != 1024 {
		t.Errorf("MaxTokens: got %v, want 1024", gw.captured.MaxTokens)
	}
}

func TestInfer_PassthroughTemperature(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	req := messages.InferenceRequest{Temperature: float64Ptr(0.7)}
	_, err := gi.Infer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gw.captured.Temperature == nil || *gw.captured.Temperature != 0.7 {
		t.Errorf("Temperature: got %v, want 0.7", gw.captured.Temperature)
	}
}

func TestInfer_PassthroughStopSequences(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	req := messages.InferenceRequest{StopSequences: []string{"STOP", "END"}}
	_, err := gi.Infer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(gw.captured.StopSequences) != 2 || gw.captured.StopSequences[0] != "STOP" || gw.captured.StopSequences[1] != "END" {
		t.Errorf("StopSequences: got %v, want [STOP END]", gw.captured.StopSequences)
	}
}

func TestInfer_NilFieldsPassedAsNil(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	req := messages.InferenceRequest{}
	_, err := gi.Infer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gw.captured.MaxTokens != nil {
		t.Errorf("MaxTokens should be nil, got %v", *gw.captured.MaxTokens)
	}
	if gw.captured.Temperature != nil {
		t.Errorf("Temperature should be nil, got %v", *gw.captured.Temperature)
	}
	if gw.captured.StopSequences != nil {
		t.Errorf("StopSequences should be nil, got %v", gw.captured.StopSequences)
	}
}

func TestInferStream_PassthroughAllFields(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	req := messages.InferenceRequest{
		MaxTokens:     intPtr(2048),
		Temperature:   float64Ptr(0.5),
		StopSequences: []string{"<|end|>"},
	}
	ch, err := gi.InferStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Drain channel to avoid leak.
	for range ch {
	}

	if gw.captured.MaxTokens == nil || *gw.captured.MaxTokens != 2048 {
		t.Errorf("MaxTokens: got %v, want 2048", gw.captured.MaxTokens)
	}
	if gw.captured.Temperature == nil || *gw.captured.Temperature != 0.5 {
		t.Errorf("Temperature: got %v, want 0.5", gw.captured.Temperature)
	}
	if len(gw.captured.StopSequences) != 1 || gw.captured.StopSequences[0] != "<|end|>" {
		t.Errorf("StopSequences: got %v, want [<|end|>]", gw.captured.StopSequences)
	}
}

func TestInfer_DefaultsOverriddenByPerRequestValues(t *testing.T) {
	gw := &captureGateway{}
	gi := NewGatewayInferencer(gw)

	// Simulate defaults applied by the coordinator via NewInferenceRequest.
	defaults := &messages.InferenceDefaults{
		MaxTokens:     intPtr(512),
		Temperature:   float64Ptr(0.3),
		StopSequences: []string{"DEFAULT_STOP"},
	}
	// Build request with defaults, then override specific fields.
	req := messages.NewInferenceRequest(nil, nil, 0, defaults)
	// Override MaxTokens and Temperature; StopSequences keeps the default.
	req.MaxTokens = intPtr(4096)
	req.Temperature = float64Ptr(1.0)

	_, err := gi.Infer(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Per-request values should win.
	if *gw.captured.MaxTokens != 4096 {
		t.Errorf("MaxTokens: got %d, want 4096", *gw.captured.MaxTokens)
	}
	if *gw.captured.Temperature != 1.0 {
		t.Errorf("Temperature: got %f, want 1.0", *gw.captured.Temperature)
	}
	// StopSequences was not overridden, so default should flow through.
	if len(gw.captured.StopSequences) != 1 || gw.captured.StopSequences[0] != "DEFAULT_STOP" {
		t.Errorf("StopSequences: got %v, want [DEFAULT_STOP]", gw.captured.StopSequences)
	}
}

func TestGatewayInferencer_ImplementsLoopOwnedContractAtRuntime(t *testing.T) {
	gw := &captureGateway{}
	var inferencer messages.Inferencer = NewGatewayInferencer(gw)

	result, err := inferencer.Infer(context.Background(), messages.InferenceRequest{})
	if err != nil {
		t.Fatalf("Infer via messages.Inferencer: %v", err)
	}
	if result.Message.Role != "assistant" {
		t.Fatalf("message role: got %q, want assistant", result.Message.Role)
	}
	if result.TokenUsage.TotalTokens != 1 {
		t.Fatalf("total tokens: got %d, want 1", result.TokenUsage.TotalTokens)
	}
}

func TestGatewayInferencer_PreservesUnsupportedFeatureErrorBeforeProviderCall(t *testing.T) {
	provider := &capabilityProvider{
		name: "inferencer-validation-provider",
		caps: gateway.ProviderCapabilities{
			Provider: "inferencer-validation-provider",
			Stateless: capabilities.StatelessCapabilities{
				Tools: capabilities.Unsupported("inferencer tools unavailable"),
			},
		},
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	inferencer := NewGatewayInferencer(gw)
	_, err = inferencer.Infer(context.Background(), messages.InferenceRequest{
		Tools: []messages.ToolDefinition{{Name: "lookup"}},
	})

	var unsupported *gateway.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedFeatureError", err)
	}
	if unsupported.Provider != "inferencer-validation-provider" {
		t.Fatalf("provider = %q, want inferencer-validation-provider", unsupported.Provider)
	}
	if unsupported.Feature != capabilities.FeatureTools {
		t.Fatalf("feature = %q, want %q", unsupported.Feature, capabilities.FeatureTools)
	}
	if unsupported.RequestedMode != capabilities.RequestedModeStateless {
		t.Fatalf("mode = %q, want %q", unsupported.RequestedMode, capabilities.RequestedModeStateless)
	}
	if unsupported.Capability.State != capabilities.CapabilityStateUnsupported {
		t.Fatalf("capability state = %q, want unsupported", unsupported.Capability.State)
	}
	if provider.inferCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.inferCalls)
	}
}
