package gateway

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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
	stream, err := g.provider.InferStream(ctx, providerInferenceRequest(req))
	if err != nil {
		return nil, err
	}
	return normalizeStreamErrors(stream), nil
}

func normalizeStreamErrors(in <-chan messages.StreamMessage) <-chan messages.StreamMessage {
	out := make(chan messages.StreamMessage)
	go func() {
		defer close(out)
		for msg := range in {
			normalizeStreamErrorValue(msg.Value)
			out <- msg
		}
	}()
	return out
}

func normalizeStreamErrorValue(value messages.StreamMessageValue) {
	errValue, ok := value.(*messages.ErrorValue)
	if !ok || errValue == nil || errValue.Err == nil {
		return
	}
	if errValue.Classification == "" {
		errValue.Classification = interactionErrorClassification(errValue.Err)
	}
	if errValue.TerminalReason == "" {
		errValue.TerminalReason = messages.TerminalReasonTerminalFailure
	}
	if errValue.TerminalProvenance == "" {
		errValue.TerminalProvenance = messages.TerminalProvenanceGateway
	}
	if errValue.OutputState == "" {
		errValue.OutputState = messages.TerminalOutputNone
	}
}
