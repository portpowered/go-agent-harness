package gemini

import "net/http"

// Option configures the Gemini provider.
type Option func(*GeminiProvider)

// WithAPIKey sets the API key.
func WithAPIKey(key string) Option {
	return func(p *GeminiProvider) {
		p.apiKey = key
	}
}

// WithModel sets the default model to use (e.g. gemini-2.5-flash, gemini-2.5-pro).
func WithModel(model string) Option {
	return func(p *GeminiProvider) {
		p.model = model
	}
}

// WithHTTPClient sets a custom HTTP client for API calls (e.g. for record/replay testing).
func WithHTTPClient(client *http.Client) Option {
	return func(p *GeminiProvider) {
		p.httpClient = client
	}
}
