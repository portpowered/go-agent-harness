package gateway

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-loop/pkg/messages"
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
	return g.provider.Infer(ctx, providers.InferenceRequest{
		Messages:         req.Messages,
		Tools:            req.Tools,
		Model:            req.Model,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		StopSequences:    req.StopSequences,
		FrequencyPenalty: req.FrequencyPenalty,
		Thinking:         req.Thinking,
		CacheControl:     req.CacheControl,
		Config:           req.Config,
	})
}

func (g *DefaultGateway) InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error) {
	return g.provider.InferStream(ctx, providers.InferenceRequest{
		Messages:         req.Messages,
		Tools:            req.Tools,
		Model:            req.Model,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		StopSequences:    req.StopSequences,
		FrequencyPenalty: req.FrequencyPenalty,
		Thinking:         req.Thinking,
		CacheControl:     req.CacheControl,
		Config:           req.Config,
	})
}
