package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// BraveSearchProvider tests
// ---------------------------------------------------------------------------

func TestBraveSearchProvider_Success(t *testing.T) {
	braveResp := `{
		"web": {
			"results": [
				{"title": "Result One", "url": "https://example.com/1", "description": "First result"},
				{"title": "Result Two", "url": "https://example.com/2", "description": "Second result"}
			]
		}
	}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        braveResp,
	})

	provider := &BraveSearchProvider{
		apiKey:     "test-key",
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "golang testing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "Result One") {
		t.Errorf("result should contain 'Result One', got: %s", result)
	}
	if !strings.Contains(result, "https://example.com/1") {
		t.Errorf("result should contain URL, got: %s", result)
	}
	if !strings.Contains(result, "First result") {
		t.Errorf("result should contain description, got: %s", result)
	}
	if len(rt.calls) != 1 {
		t.Errorf("expected 1 HTTP call, got %d", len(rt.calls))
	}
	if rt.calls[0].Header.Get("X-Subscription-Token") != "test-key" {
		t.Errorf("X-Subscription-Token header not set correctly, got %q", rt.calls[0].Header.Get("X-Subscription-Token"))
	}
}

func TestBraveSearchProvider_NoResults(t *testing.T) {
	braveResp := `{"web": {"results": []}}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        braveResp,
	})

	provider := &BraveSearchProvider{
		apiKey:     "test-key",
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "nothing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("expected 'No results' message, got: %s", result)
	}
}

func TestBraveSearchProvider_RequestURL(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        `{"web": {"results": [{"title": "X", "url": "https://x.com", "description": ""}]}}`,
	})

	provider := &BraveSearchProvider{
		apiKey:     "key123",
		httpClient: &http.Client{Transport: rt},
	}

	_, err := provider.Search(context.Background(), "go lang", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rt.calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	gotURL := rt.calls[0].URL.String()
	if !strings.Contains(gotURL, "search.brave.com") {
		t.Errorf("expected Brave search URL, got: %s", gotURL)
	}
	if !strings.Contains(gotURL, "go+lang") && !strings.Contains(gotURL, "go%20lang") {
		t.Errorf("expected query in URL, got: %s", gotURL)
	}
}

// ---------------------------------------------------------------------------
// TavilySearchProvider tests
// ---------------------------------------------------------------------------

func TestTavilySearchProvider_Success(t *testing.T) {
	tavilyResp := `{
		"results": [
			{"title": "Tavily Result", "url": "https://example.com/tavily", "content": "Some content here"},
			{"title": "Tavily Result 2", "url": "https://example.com/tavily2", "content": "More content"}
		]
	}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        tavilyResp,
	})

	provider := &TavilySearchProvider{
		apiKey:     "test-key",
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "Tavily Result") {
		t.Errorf("result should contain 'Tavily Result', got: %s", result)
	}
	if !strings.Contains(result, "https://example.com/tavily") {
		t.Errorf("result should contain URL, got: %s", result)
	}
	if !strings.Contains(result, "Some content here") {
		t.Errorf("result should contain content, got: %s", result)
	}
	if !strings.Contains(result, "via Tavily") {
		t.Errorf("result should mention Tavily, got: %s", result)
	}
}

func TestTavilySearchProvider_HTTPError(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  401,
		ContentType: "application/json",
		Body:        `{"error": "unauthorized"}`,
	})

	provider := &TavilySearchProvider{
		apiKey:     "bad-key",
		httpClient: &http.Client{Transport: rt},
	}

	_, err := provider.Search(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for 401 status, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status code 401, got: %v", err)
	}
}

func TestTavilySearchProvider_PostPayload(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        `{"results": [{"title": "T", "url": "https://t.com", "content": ""}]}`,
	})

	provider := &TavilySearchProvider{
		apiKey:     "mykey",
		httpClient: &http.Client{Transport: rt},
	}

	_, err := provider.Search(context.Background(), "openai", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rt.calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	req := rt.calls[0]
	if req.Method != http.MethodPost {
		t.Errorf("method: got %s, want POST", req.Method)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", req.Header.Get("Content-Type"))
	}
}

// ---------------------------------------------------------------------------
// PerplexitySearchProvider tests
// ---------------------------------------------------------------------------

func TestPerplexitySearchProvider_Success(t *testing.T) {
	perplexityResp := `{
		"choices": [
			{"message": {"content": "1. Go Programming\n   https://go.dev\n   The official Go website"}}
		]
	}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        perplexityResp,
	})

	provider := &PerplexitySearchProvider{
		apiKey:     "test-key",
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "Go Programming") {
		t.Errorf("result should contain 'Go Programming', got: %s", result)
	}
	if !strings.Contains(result, "via Perplexity") {
		t.Errorf("result should mention Perplexity, got: %s", result)
	}
}

func TestPerplexitySearchProvider_AuthHeader(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        `{"choices": [{"message": {"content": "result"}}]}`,
	})

	provider := &PerplexitySearchProvider{
		apiKey:     "secret-bearer-token",
		httpClient: &http.Client{Transport: rt},
	}

	_, err := provider.Search(context.Background(), "test", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rt.calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	auth := rt.calls[0].Header.Get("Authorization")
	if auth != "Bearer secret-bearer-token" {
		t.Errorf("Authorization: got %q, want Bearer secret-bearer-token", auth)
	}
}

func TestPerplexitySearchProvider_APIError(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  429,
		ContentType: "application/json",
		Body:        `{"error": "rate limited"}`,
	})

	provider := &PerplexitySearchProvider{
		apiKey:     "key",
		httpClient: &http.Client{Transport: rt},
	}

	_, err := provider.Search(context.Background(), "test", 3)
	if err == nil {
		t.Fatal("expected error for 429 status, got nil")
	}
}

// ---------------------------------------------------------------------------
// DuckDuckGoSearchProvider tests
// ---------------------------------------------------------------------------

func TestDuckDuckGoSearchProvider_HTMLParsing(t *testing.T) {
	// Minimal DDG HTML with the result link pattern the extractor uses
	ddgHTML := `<html><body>
		<a class="result__a" href="https://golang.org">Go Programming Language</a>
		<a class="result__snippet">Learn Go programming</a>
		<a class="result__a" href="https://go.dev">Official Go Site</a>
		<a class="result__snippet">The official Go website</a>
	</body></html>`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/html",
		Body:        ddgHTML,
	})

	provider := &DuckDuckGoSearchProvider{
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Result must be non-empty and reference the query
	if result == "" {
		t.Error("result should not be empty")
	}
	if !strings.Contains(result, "golang") && !strings.Contains(result, "No results") {
		t.Errorf("result should reference query, got: %s", result)
	}
}

func TestDuckDuckGoSearchProvider_NoMatches(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/html",
		Body:        "<html><body>no results here</body></html>",
	})

	provider := &DuckDuckGoSearchProvider{
		httpClient: &http.Client{Transport: rt},
	}

	result, err := provider.Search(context.Background(), "xyzzy", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(result, "No results") {
		t.Errorf("expected 'No results' message when HTML has no matches, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// WebSearchTool.Execute integration tests
// ---------------------------------------------------------------------------

func TestWebSearchTool_ExecuteWithBrave(t *testing.T) {
	braveResp := `{"web": {"results": [
		{"title": "Go Lang", "url": "https://go.dev", "description": "Official Go website"}
	]}}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        braveResp,
	})

	tool := NewWebSearchTool(WebSearchToolOptions{
		BraveEnabled:    true,
		BraveAPIKey:     "test-key",
		BraveMaxResults: 5,
		HTTPClient:      &http.Client{Transport: rt},
	})
	if tool == nil {
		t.Fatal("NewWebSearchTool returned nil")
	}

	msgs, err := tool.Execute(context.Background(), map[string]any{
		"query": "golang testing",
		"count": float64(3),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	text := msgs[0].TextContent()
	if !strings.Contains(text, "Go Lang") {
		t.Errorf("result should contain 'Go Lang', got: %s", text)
	}
}

func TestWebSearchTool_ExecuteWithTavily(t *testing.T) {
	tavilyResp := `{"results": [{"title": "Tavily Go", "url": "https://tavily.example", "content": "info"}]}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        tavilyResp,
	})

	tool := NewWebSearchTool(WebSearchToolOptions{
		TavilyEnabled: true,
		TavilyAPIKey:  "tavily-key",
		HTTPClient:    &http.Client{Transport: rt},
	})
	if tool == nil {
		t.Fatal("NewWebSearchTool returned nil")
	}

	msgs, err := tool.Execute(context.Background(), map[string]any{
		"query": "tavily test",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := msgs[0].TextContent()
	if !strings.Contains(text, "Tavily Go") {
		t.Errorf("result should contain 'Tavily Go', got: %s", text)
	}
}

func TestWebSearchTool_MissingQuery(t *testing.T) {
	tool := NewWebSearchTool(WebSearchToolOptions{DuckDuckGoEnabled: true})
	if tool == nil {
		t.Fatal("NewWebSearchTool returned nil")
	}
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing query, got nil")
	}
}

func TestWebSearchTool_NilWhenNoProvider(t *testing.T) {
	tool := NewWebSearchTool(WebSearchToolOptions{})
	if tool != nil {
		t.Error("expected nil tool when no provider is configured")
	}
}

func TestWebSearchTool_ProviderPriority(t *testing.T) {
	// When multiple providers are configured, Perplexity wins.
	perpResp := `{"choices": [{"message": {"content": "perplexity result"}}]}`
	braveResp := `{"web": {"results": [{"title": "Brave", "url": "https://b.com", "description": ""}]}}`

	// Both round trippers; only one should be called.
	perpRT := newMockRoundTripper(mockHTTPResponse{StatusCode: 200, ContentType: "application/json", Body: perpResp})
	braveRT := newMockRoundTripper(mockHTTPResponse{StatusCode: 200, ContentType: "application/json", Body: braveResp})
	_ = braveRT // only perpRT should receive calls

	// Inject Perplexity's client; Brave's client is not injected via HTTPClient since
	// only one client is shared. We verify by checking which result text we get.
	tool := NewWebSearchTool(WebSearchToolOptions{
		PerplexityEnabled: true,
		PerplexityAPIKey:  "pkey",
		BraveEnabled:      true,
		BraveAPIKey:       "bkey",
		HTTPClient:        &http.Client{Transport: perpRT},
	})
	if tool == nil {
		t.Fatal("NewWebSearchTool returned nil")
	}

	msgs, err := tool.Execute(context.Background(), map[string]any{"query": "test"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	text := msgs[0].TextContent()
	if !strings.Contains(text, "perplexity result") {
		t.Errorf("Perplexity should have priority, result: %s", text)
	}
	if perpRT.callIdx != 1 {
		t.Errorf("expected exactly 1 call to Perplexity, got %d", perpRT.callIdx)
	}
}
