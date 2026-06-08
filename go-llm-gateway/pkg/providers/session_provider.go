package providers

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// SessionProvider is the provider-facing bridge for session-based inference.
// It is the sessional counterpart to Provider: Provider handles stateless
// request/response inference, while SessionProvider adapts provider-specific
// realtime transports to the loop-owned messages.Session contract.
//
// ConnectSession returns a messages.Session (declared in go-agent-loop) rather than a
// provider-specific type, so that the agent loop owns its dependency contracts and
// go-llm-gateway implements them.
type SessionProvider interface {
	// Name returns the provider name (e.g. "grok").
	Name() string
	// ConnectSession establishes a new session with the provider using the given
	// gateway-owned configuration. The returned Session wraps the loop-owned
	// bidirectional StreamMessage contract.
	ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error)
}
