package grok

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// WebSocketDialer abstracts WebSocket connection establishment for testing.
type WebSocketDialer interface {
	// Dial connects to the given URL with the provided request headers.
	// Returns a WebSocketConn or an error.
	Dial(url string, headers map[string]string) (WebSocketConn, error)
}

// WebSocketConn abstracts a WebSocket connection for testing.
type WebSocketConn interface {
	// ReadMessage reads the next message from the connection.
	ReadMessage() (messageType int, p []byte, err error)
	// WriteMessage writes a message to the connection.
	WriteMessage(messageType int, data []byte) error
	// Close closes the connection.
	Close() error
}

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
