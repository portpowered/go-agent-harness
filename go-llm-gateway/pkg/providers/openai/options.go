package openai

import (
	"net/http"

	"github.com/portpowered/go-llm-gateway/pkg/logging"
)

// Option configures the OpenAI provider.
type Option func(*OpenAIProvider)

// WebSocketDialer abstracts realtime WebSocket connection establishment for tests.
type WebSocketDialer interface {
	Dial(url string, headers map[string]string) (WebSocketConn, error)
}

// WebSocketConn abstracts a realtime WebSocket connection for tests.
type WebSocketConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// WithHTTPClient sets a custom HTTP client for API calls (e.g. for record/replay testing).
func WithHTTPClient(client *http.Client) Option {
	return func(p *OpenAIProvider) {
		p.httpClient = client
	}
}

// WithBaseURL sets a custom API base URL (for Groq, local servers, etc.).
func WithBaseURL(url string) Option {
	return func(p *OpenAIProvider) {
		p.baseURL = url
	}
}

// WithRealtimeBaseURL sets a custom Realtime WebSocket endpoint.
func WithRealtimeBaseURL(url string) Option {
	return func(p *OpenAIProvider) {
		p.realtimeBaseURL = url
	}
}

func WithLogger(logger logging.Logger) Option {
	return func(p *OpenAIProvider) {
		p.logger = logger
	}
}

// WithModel sets the default model to use.
func WithModel(model string) Option {
	return func(p *OpenAIProvider) {
		p.model = model
	}
}

// WithAPIKey sets the API key.
func WithAPIKey(key string) Option {
	return func(p *OpenAIProvider) {
		p.apiKey = key
	}
}

// WithWebSocketDialer sets a custom realtime WebSocket dialer for tests or replay.
func WithWebSocketDialer(d WebSocketDialer) Option {
	return func(p *OpenAIProvider) {
		p.realtimeDialer = d
	}
}

// WithLegacyRealtimeSessionUpdate sends the pre-GA flat Realtime session.update
// shape for compatibility with older replay fixtures or compatible providers.
func WithLegacyRealtimeSessionUpdate() Option {
	return func(p *OpenAIProvider) {
		p.realtimeLegacySessionUpdate = true
	}
}
