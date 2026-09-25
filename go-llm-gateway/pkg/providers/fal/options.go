package fal

import (
	"io"
	"net/http"
)

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

// closeResponseBody releases an HTTP response body whose content has already
// been consumed or abandoned. The response outcome is decided by then, so a
// close failure cannot change it and is intentionally not reported.
func closeResponseBody(body io.Closer) {
	if err := body.Close(); err != nil {
		return
	}
}

// readErrorBody reads a failed response's diagnostic body. The HTTP status is
// the primary error; a body read failure is appended to the diagnostic text.
func readErrorBody(body io.Reader) string {
	data, err := io.ReadAll(body)
	if err != nil {
		return string(data) + " (read error body: " + err.Error() + ")"
	}
	return string(data)
}
