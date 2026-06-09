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
	sessionGW sessionGateway
	request   SessionRequest
}

// SessionOption configures the SessionGatewayInferencer.
type SessionOption func(*SessionGatewayInferencer)

// SessionRequest is the persistent loop-facing session shape used by
// SessionGatewayInferencer. The caller-owned context.Context remains
// per-operation input to ConnectSession; it is not stored here.
type SessionRequest struct {
	Config models.SessionConfig
}

// WithSessionRequest sets the complete persistent session request used for
// every session connection. The request is copied so later caller mutations do
// not change future ConnectSession operations.
func WithSessionRequest(req SessionRequest) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.request = cloneSessionRequest(req)
	}
}

// WithSessionModel sets the model ID for every session connection.
func WithSessionModel(model string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.request.Config.Model = model
	}
}

// WithSessionVoice sets the voice ID for session audio output.
func WithSessionVoice(voice string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.request.Config.Voice = voice
	}
}

// WithSessionInstructions sets system-level instructions for sessions.
func WithSessionInstructions(instructions string) SessionOption {
	return func(si *SessionGatewayInferencer) {
		si.request.Config.Instructions = instructions
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
// loop-owned messages.Session boundary contract. ctx is caller-owned for this
// connection attempt only; persistent session shape is copied from Request().
func (si *SessionGatewayInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return si.sessionGW.ConnectSession(ctx, cloneSessionConfig(si.request.Config))
}

// Request returns the persistent session request configured for this bridge.
// The returned value is a copy; mutating it does not affect future connections.
func (si *SessionGatewayInferencer) Request() SessionRequest {
	return cloneSessionRequest(si.request)
}

func cloneSessionRequest(req SessionRequest) SessionRequest {
	req.Config = cloneSessionConfig(req.Config)
	return req
}

func cloneSessionConfig(config models.SessionConfig) models.SessionConfig {
	config.Modalities = append([]models.SessionModality(nil), config.Modalities...)
	config.Tools = append([]models.ToolDefinition(nil), config.Tools...)
	for i := range config.Tools {
		config.Tools[i].Parameters = append([]models.ToolParameter(nil), config.Tools[i].Parameters...)
	}
	if config.TurnDetection != nil {
		turnDetection := *config.TurnDetection
		config.TurnDetection = &turnDetection
	}
	config.Config = append([]byte(nil), config.Config...)
	return config
}
