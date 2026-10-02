package openaichatgpt

import (
	"net/http"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// Option configures a Provider.
type Option func(*Provider)

// WithModel sets the default model. Without it (and without a per-request
// model) the provider uses the account's default Codex model from the model
// list (see DefaultModel).
func WithModel(model string) Option {
	return func(p *Provider) { p.model = model }
}

// WithBaseURL sets the ChatGPT Codex backend base URL (default
// DefaultBaseURL). Requests go to {base}/responses and {base}/models.
func WithBaseURL(baseURL string) Option {
	return func(p *Provider) { p.baseURL = baseURL }
}

// WithHTTPClient sets the client used for backend requests (for example a
// recording or replay transport). Token refresh does not use it.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.httpClient = client }
}

// WithLogger sets the provider logger. Tokens are never logged.
func WithLogger(logger logging.Logger) Option {
	return func(p *Provider) { p.logger = logger }
}

// WithSessionID fixes the conversation id sent as the session-id and
// x-client-request-id headers and as prompt_cache_key. By default each
// Provider draws a random id once, so every turn of one conversation shares
// it, as Codex does.
func WithSessionID(id string) Option {
	return func(p *Provider) { p.sessionID = id }
}

// WithStreamIdleTimeout sets how long a response stream may send nothing
// before the turn fails with ErrStreamIdle (default
// DefaultStreamIdleTimeout, Codex's stream_idle_timeout). Zero disables it.
func WithStreamIdleTimeout(timeout time.Duration) Option {
	return func(p *Provider) { p.idleTimeout = timeout }
}
