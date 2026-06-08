package gateway

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// DefaultGateway routes inference requests to a configured provider.
type DefaultGateway struct {
	provider providers.Provider
}

// Option configures the DefaultGateway.
type Option func(*DefaultGateway)

// WithProvider sets the LLM provider.
func WithProvider(p providers.Provider) Option {
	return func(g *DefaultGateway) {
		g.provider = p
	}
}

// NewGateway creates a new DefaultGateway.
func NewGateway(opts ...Option) (*DefaultGateway, error) {
	g := &DefaultGateway{}
	for _, opt := range opts {
		opt(g)
	}
	if g.provider == nil {
		return nil, errors.New("provider is required")
	}
	return g, nil
}

// Capabilities reports the configured provider's public capability contract.
// It is local metadata discovery only; providers that do not implement
// providers.CapabilityReporter return the documented unknown fallback.
func (g *DefaultGateway) Capabilities() ProviderCapabilities {
	return providerCapabilities(g.provider)
}

func (g *DefaultGateway) Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error) {
	if err := validateStatelessRequest(g.Capabilities(), req, capabilities.RequestedModeStateless); err != nil {
		return InferenceResponse{}, err
	}
	return g.provider.Infer(ctx, providerInferenceRequest(req))
}

func (g *DefaultGateway) InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error) {
	if err := validateStatelessRequest(g.Capabilities(), req, capabilities.RequestedModeStatelessStream); err != nil {
		return nil, err
	}
	return g.provider.InferStream(ctx, providerInferenceRequest(req))
}
