package gateway

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

type capabilityProvider struct {
	name        string
	caps        ProviderCapabilities
	capCalls    int
	inferCalls  int
	streamCalls int
}

func (p *capabilityProvider) Name() string {
	return p.name
}

func (p *capabilityProvider) Capabilities() ProviderCapabilities {
	p.capCalls++
	return p.caps
}

func (p *capabilityProvider) Infer(_ context.Context, _ providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{}, nil
}

func (p *capabilityProvider) InferStream(_ context.Context, _ providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type legacyProvider struct {
	name        string
	inferCalls  int
	streamCalls int
}

func (p *legacyProvider) Name() string {
	return p.name
}

func (p *legacyProvider) Infer(_ context.Context, _ providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{}, nil
}

func (p *legacyProvider) InferStream(_ context.Context, _ providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type capabilitySessionProvider struct {
	name         string
	caps         ProviderCapabilities
	capCalls     int
	connectCalls int
}

func (p *capabilitySessionProvider) Name() string {
	return p.name
}

func (p *capabilitySessionProvider) Capabilities() ProviderCapabilities {
	p.capCalls++
	return p.caps
}

func (p *capabilitySessionProvider) ConnectSession(context.Context, models.SessionConfig) (messages.Session, error) {
	p.connectCalls++
	return nil, nil
}

func TestGatewayCapabilitiesUsesProviderReporterWithoutInference(t *testing.T) {
	t.Parallel()

	provider := &capabilityProvider{
		name: "fake-provider",
		caps: ProviderCapabilities{
			Provider: "",
			Stateless: capabilities.StatelessCapabilities{
				Tools:     capabilities.Supported("test provider supports tools"),
				Streaming: capabilities.Unsupported("streaming disabled in test provider"),
			},
			Session: capabilities.SessionCapabilities{
				Sessions: capabilities.Unknown("session behavior not reported"),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "fake-provider" {
		t.Fatalf("Provider = %q, want fake-provider", got.Provider)
	}
	if got.Stateless.Tools.State != CapabilityStateSupported {
		t.Fatalf("Tools state = %q, want supported", got.Stateless.Tools.State)
	}
	if got.Stateless.Streaming.State != CapabilityStateUnsupported {
		t.Fatalf("Streaming state = %q, want unsupported", got.Stateless.Streaming.State)
	}
	if provider.capCalls != 1 {
		t.Fatalf("capability calls = %d, want 1", provider.capCalls)
	}
	if provider.inferCalls != 0 || provider.streamCalls != 0 {
		t.Fatalf("discovery called provider execution: infer=%d stream=%d", provider.inferCalls, provider.streamCalls)
	}
}

func TestGatewayCapabilitiesFallbacksToUnknownForLegacyProvider(t *testing.T) {
	t.Parallel()

	provider := &legacyProvider{name: "legacy-provider"}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "legacy-provider" {
		t.Fatalf("Provider = %q, want legacy-provider", got.Provider)
	}
	if got.Stateless.Tools.State != CapabilityStateUnknown {
		t.Fatalf("Tools state = %q, want unknown", got.Stateless.Tools.State)
	}
	if got.Stateless.Streaming.State != CapabilityStateUnknown {
		t.Fatalf("Streaming state = %q, want unknown", got.Stateless.Streaming.State)
	}
	if got.Session.Sessions.State != CapabilityStateUnknown {
		t.Fatalf("Sessions state = %q, want unknown", got.Session.Sessions.State)
	}
	if provider.inferCalls != 0 || provider.streamCalls != 0 {
		t.Fatalf("discovery called provider execution: infer=%d stream=%d", provider.inferCalls, provider.streamCalls)
	}
}

func TestSessionGatewayCapabilitiesUsesProviderReporterWithoutConnecting(t *testing.T) {
	t.Parallel()

	provider := &capabilitySessionProvider{
		name: "session-provider",
		caps: ProviderCapabilities{
			Provider: "session-provider",
			Session: capabilities.SessionCapabilities{
				Sessions:    capabilities.Supported("session connection supported"),
				AudioInput:  capabilities.Supported("audio input supported"),
				AudioOutput: capabilities.Unsupported("audio output disabled in test provider"),
			},
		},
	}
	gw, err := NewSessionGateway(WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "session-provider" {
		t.Fatalf("Provider = %q, want session-provider", got.Provider)
	}
	if got.Session.Sessions.State != CapabilityStateSupported {
		t.Fatalf("Sessions state = %q, want supported", got.Session.Sessions.State)
	}
	if got.Session.AudioOutput.State != CapabilityStateUnsupported {
		t.Fatalf("AudioOutput state = %q, want unsupported", got.Session.AudioOutput.State)
	}
	if provider.capCalls != 1 {
		t.Fatalf("capability calls = %d, want 1", provider.capCalls)
	}
	if provider.connectCalls != 0 {
		t.Fatalf("discovery connected session provider %d times", provider.connectCalls)
	}
}
