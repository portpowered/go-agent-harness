package gateway

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// DefaultSessionGateway routes session requests to a configured SessionProvider.
// It is the sessional counterpart to DefaultGateway.
type DefaultSessionGateway struct {
	provider providers.SessionProvider
}

// SessionGatewayOption configures the DefaultSessionGateway.
type SessionGatewayOption func(*DefaultSessionGateway)

// WithSessionProvider sets the session provider.
func WithSessionProvider(p providers.SessionProvider) SessionGatewayOption {
	return func(g *DefaultSessionGateway) {
		g.provider = p
	}
}

// NewSessionGateway creates a new DefaultSessionGateway.
// Returns an error if no session provider is configured.
func NewSessionGateway(opts ...SessionGatewayOption) (*DefaultSessionGateway, error) {
	g := &DefaultSessionGateway{}
	for _, opt := range opts {
		opt(g)
	}
	if g.provider == nil {
		return nil, errors.New("session provider is required")
	}
	return g, nil
}

// sessionInferencer is the gateway-layer interface for establishing sessions
// with a given configuration. It is an internal detail — external callers use
// messages.SessionInferencer (declared in go-agent-loop) via SessionGatewayInferencer.
type sessionInferencer interface {
	ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error)
}

// Compile-time check that DefaultSessionGateway satisfies sessionInferencer.
var _ sessionInferencer = (*DefaultSessionGateway)(nil)

// ConnectSession establishes a session via the configured provider.
func (g *DefaultSessionGateway) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	if err := validateSessionRequest(g.provider, config); err != nil {
		return nil, err
	}
	return g.provider.ConnectSession(ctx, config)
}
