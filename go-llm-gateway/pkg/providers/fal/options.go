package fal

import "net/http"

// Option configures the FalProvider.
type Option func(*FalProvider)

// WithAPIKey sets the fal.ai API key (FAL_KEY). Required for authenticated requests.
func WithAPIKey(apiKey string) Option {
	return func(p *FalProvider) {
		p.apiKey = apiKey
	}
}

// WithBaseURL overrides the fal API base URL (default: https://fal.run).
func WithBaseURL(baseURL string) Option {
	return func(p *FalProvider) {
		p.baseURL = baseURL
	}
}

// WithHTTPClient sets a custom HTTP client for requests.
func WithHTTPClient(client *http.Client) Option {
	return func(p *FalProvider) {
		p.httpClient = client
	}
}
