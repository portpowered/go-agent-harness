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
