package inference

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// sessionGateway is the subset of gateway.DefaultSessionGateway needed by
// SessionGatewayInferencer. Defined locally to avoid importing the gateway package.
type sessionGateway interface {
	ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error)
}

// Ensure SessionGatewayInferencer satisfies messages.SessionInferencer at compile time.
var _ messages.SessionInferencer = (*SessionGatewayInferencer)(nil)

// SessionGatewayInferencer is the public bridge from gateway session
// establishment into the loop-owned messages.SessionInferencer contract. It is
// the session counterpart to GatewayInferencer: where GatewayInferencer adapts
// stateless gateway behavior, SessionGatewayInferencer adapts persistent
// bidirectional session behavior without defining a second shared session API.
//
// ConnectSession returns the loop-owned messages.Session boundary contract
// directly from the gateway/provider path after provider-specific protocol
// translation has already been handled internally.
type SessionGatewayInferencer struct {
	sessionGW    sessionGateway
	model        string
	voice        string
	instructions string
}

// SessionOption configures the SessionGatewayInferencer.
type SessionOption func(*SessionGatewayInferencer)

// WithSessionModel sets the model ID for every session connection.
func WithSessionModel(model string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.model = model
	}
}

// WithSessionVoice sets the voice ID for session audio output.
func WithSessionVoice(voice string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.voice = voice
	}
}

// WithSessionInstructions sets system-level instructions for sessions.
func WithSessionInstructions(instructions string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.instructions = instructions
	}
}

// NewSessionGatewayInferencer creates a bridge that delegates session
// establishment to a gateway-owned session adapter while preserving
// messages.SessionInferencer as the consumer-facing contract.
func NewSessionGatewayInferencer(sessionGW sessionGateway, opts ...SessionOption) *SessionGatewayInferencer {
	si := &SessionGatewayInferencer{sessionGW: sessionGW}
	for _, opt := range opts {
		opt(si)
	}
	return si
}

// ConnectSession establishes a new session via the gateway and returns the
// loop-owned messages.Session boundary contract.
func (si *SessionGatewayInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return si.sessionGW.ConnectSession(ctx, models.SessionConfig{
		Model:        si.model,
		Voice:        si.voice,
		Instructions: si.instructions,
	})
}
