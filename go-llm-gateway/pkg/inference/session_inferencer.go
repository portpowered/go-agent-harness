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

// SessionGatewayInferencer bridges a go-llm-gateway SessionInferencer to the
// agent loop's session inference needs. It is the sessional counterpart to
// GatewayInferencer: where GatewayInferencer adapts Gateway for request/response
// inference, SessionGatewayInferencer adapts SessionInferencer for persistent
// bidirectional sessions.
//
// ConnectSession returns the messages.Session directly from the provider —
// providers handle all protocol translation internally.
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

// NewSessionGatewayInferencer creates a SessionGatewayInferencer that delegates
// to the given session gateway for session establishment.
func NewSessionGatewayInferencer(sessionGW sessionGateway, opts ...SessionOption) *SessionGatewayInferencer {
	si := &SessionGatewayInferencer{sessionGW: sessionGW}
	for _, opt := range opts {
		opt(si)
	}
	return si
}

// ConnectSession establishes a new session via the gateway and returns a
// messages.Session. Protocol translation is handled by the provider internally.
func (si *SessionGatewayInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return si.sessionGW.ConnectSession(ctx, models.SessionConfig{
		Model:        si.model,
		Voice:        si.voice,
		Instructions: si.instructions,
	})
}
