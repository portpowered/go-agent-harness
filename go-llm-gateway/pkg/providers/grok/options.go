package grok

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// WebSocketDialer is retained as a compatibility name for the shared
// provider-neutral transport contract.
type WebSocketDialer = transport.Dialer

// WebSocketConn is retained as a compatibility name for the shared
// provider-neutral transport contract.
type WebSocketConn = transport.Conn

// Option configures the Grok session provider.
type Option func(*GrokSessionProvider)

// WithAPIKey sets the API key for authentication.
func WithAPIKey(key string) Option {
	return func(p *GrokSessionProvider) {
		p.apiKey = key
	}
}

// WithBaseURL overrides the default Grok realtime API base URL.
func WithBaseURL(url string) Option {
	return func(p *GrokSessionProvider) {
		p.baseURL = url
	}
}

// WithWebSocketDialer sets a custom WebSocket dialer for testing.
func WithWebSocketDialer(d WebSocketDialer) Option {
	return func(p *GrokSessionProvider) {
		p.dialer = d
	}
}

// WithLogger sets the logger for the provider.
func WithLogger(logger logging.Logger) Option {
	return func(p *GrokSessionProvider) {
		p.logger = logger
	}
}
