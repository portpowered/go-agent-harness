package openai

import (
	"net/http"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Option configures the OpenAI provider.
type Option func(*OpenAIProvider)

// WebSocketDialer is retained as a compatibility name for the shared
// provider-neutral transport contract.
type WebSocketDialer = transport.Dialer

// WebSocketConn is retained as a compatibility name for the shared
// provider-neutral transport contract.
type WebSocketConn = transport.Conn

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
func WithWebSocketDialer(d transport.Dialer) Option {
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

// WithClientOwnedAudioTurnBoundaries configures a realtime session for finite
// client-scheduled audio turns. It explicitly disables provider turn detection
// so the caller owns the single commit and response.create pair for each turn.
func WithClientOwnedAudioTurnBoundaries() Option {
	return func(p *OpenAIProvider) {
		p.clientOwnsAudioTurnBoundaries = true
	}
}

// WithSessionWriteBackpressure makes continuous-session control admission wait
// for bounded outbound capacity. Cancellation and session closure still stop
// the wait. Offline replay uses this when producers run faster than wire I/O;
// the default non-blocking overload contract remains available to other hosts.
func WithSessionWriteBackpressure() Option {
	return func(p *OpenAIProvider) { p.sessionWriteBackpressure = true }
}
