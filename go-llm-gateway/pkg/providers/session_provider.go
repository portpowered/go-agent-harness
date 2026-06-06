package providers

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// SessionProvider is the interface that session-based inference providers must implement.
// It is the sessional counterpart to Provider: Provider handles stateless request/response
// inference, SessionProvider handles persistent bidirectional sessions (e.g. WebSocket).
//
// ConnectSession returns a messages.Session (declared in go-agent-loop) rather than a
// provider-specific type, so that the agent loop owns its dependency contracts and
// go-llm-gateway implements them.
type SessionProvider interface {
	// Name returns the provider name (e.g. "grok").
	Name() string
	// ConnectSession establishes a new session with the provider using the given
	// configuration. The returned Session wraps a bidirectional StreamMessage stream.
	ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error)
}
