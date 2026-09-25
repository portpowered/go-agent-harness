package anthropic

import "net/http"

// Option configures the Anthropic (Claude) provider.
type Option func(*AnthropicProvider)

// WithHTTPClient sets a custom HTTP client for API calls (e.g. for record/replay testing).
func WithHTTPClient(client *http.Client) Option {
	return func(p *AnthropicProvider) {
		p.httpClient = client
	}
}

// WithModel sets the default model to use (e.g. claude-sonnet-4-20250514, claude-3-5-sonnet-20241022).
func WithModel(model string) Option {
	return func(p *AnthropicProvider) {
		p.model = model
	}
}

// WithAPIKey sets the API key.
func WithAPIKey(key string) Option {
	return func(p *AnthropicProvider) {
		p.apiKey = key
	}
}

// WithMaxRetries sets how many times a failed request is retried after the
// first attempt. The SDK waits between attempts with exponential backoff (or
// the server's Retry-After), so tests that exercise error responses set 0 to
// avoid sleeping. Negative values are ignored. The default is
// DefaultMaxRetries.
func WithMaxRetries(retries int) Option {
	return func(p *AnthropicProvider) {
		if retries >= 0 {
			p.maxRetries = retries
		}
	}
}
