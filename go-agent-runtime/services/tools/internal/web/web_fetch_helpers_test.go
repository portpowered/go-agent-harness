package web

import "net/http"

// WithWebFetchHTTPClient sets a custom HTTP client on the WebFetchTool.
// When set, all fetch requests use this client instead of the default one.
// Primarily useful for testing with a custom round tripper.
func WithWebFetchHTTPClient(client *http.Client) WebFetchToolOption {
	return func(t *WebFetchTool) {
		t.httpClient = client
	}
}
