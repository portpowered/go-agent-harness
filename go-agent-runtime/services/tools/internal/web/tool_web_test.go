package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock round tripper
// ---------------------------------------------------------------------------

// mockHTTPResponse defines a single canned HTTP response returned by mockRoundTripper.
type mockHTTPResponse struct {
	StatusCode  int
	ContentType string
	Body        string
}

// mockRoundTripper implements http.RoundTripper and replays a queue of pre-configured
// responses. Each RoundTrip call advances an internal index; once the queue is exhausted
// the last response is repeated. Recorded requests are exposed via calls for assertions.
type mockRoundTripper struct {
	responses []mockHTTPResponse
	callIdx   int
	calls     []*http.Request
}

func newMockRoundTripper(responses ...mockHTTPResponse) *mockRoundTripper {
	return &mockRoundTripper{responses: responses}
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls = append(m.calls, req)
	idx := m.callIdx
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.callIdx++
	r := m.responses[idx]
	h := http.Header{}
	if r.ContentType != "" {
		h.Set("Content-Type", r.ContentType)
	}
	return &http.Response{
		StatusCode: r.StatusCode,
		Status:     fmt.Sprintf("%d %s", r.StatusCode, http.StatusText(r.StatusCode)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(r.Body)),
		Request:    req,
	}, nil
}

// ---------------------------------------------------------------------------
// WebFetchTool tests
// ---------------------------------------------------------------------------

func TestWebFetchTool_HTMLContent(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>Test</title></head><body><h1>Hello World</h1><p>This is a test page.</p></body></html>`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/html; charset=utf-8",
		Body:        html,
	})

	tool := NewWebFetchTool(0, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"url": "https://example.com/page",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result["extractor"] != "text" {
		t.Errorf("extractor: got %v, want text", result["extractor"])
	}
	status, ok := result["status"].(float64)
	if !ok || status != 200 {
		t.Errorf("status: got %v, want 200", result["status"])
	}
	content, ok := result["text"].(string)
	if !ok {
		t.Fatalf("content has type %T, want string", result["text"])
	}
	if !strings.Contains(content, "Hello World") {
		t.Errorf("content should contain 'Hello World', got: %s", content)
	}
	if !strings.Contains(content, "test page") {
		t.Errorf("content should contain 'test page', got: %s", content)
	}
	if len(rt.calls) != 1 {
		t.Errorf("expected 1 HTTP call, got %d", len(rt.calls))
	}
}

func TestWebFetchTool_JSONContent(t *testing.T) {
	jsonBody := `{"name":"Alice","age":30,"items":["a","b","c"]}`
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "application/json",
		Body:        jsonBody,
	})

	tool := NewWebFetchTool(0, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"url": "https://api.example.com/data",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["extractor"] != "json" {
		t.Errorf("extractor: got %v, want json", result["extractor"])
	}
	content, ok := result["text"].(string)
	if !ok {
		t.Fatalf("content has type %T, want string", result["text"])
	}
	if !strings.Contains(content, "Alice") {
		t.Errorf("JSON content should contain 'Alice', got: %s", content)
	}
	// Verify pretty-printed JSON has indentation
	if !strings.Contains(content, "\n") {
		t.Errorf("JSON content should be pretty-printed, got: %s", content)
	}
}

func TestWebFetchTool_RawContent(t *testing.T) {
	rawBody := "plain text response"
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/plain",
		Body:        rawBody,
	})

	tool := NewWebFetchTool(0, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"url": "https://example.com/text",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["extractor"] != "raw" {
		t.Errorf("extractor: got %v, want raw", result["extractor"])
	}
	content, ok := result["text"].(string)
	if !ok {
		t.Fatalf("content has type %T, want string", result["text"])
	}
	if content != rawBody {
		t.Errorf("content: got %q, want %q", content, rawBody)
	}
}

func TestWebFetchTool_Truncation(t *testing.T) {
	longBody := strings.Repeat("x", 1000)
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/plain",
		Body:        longBody,
	})

	tool := NewWebFetchTool(100, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"url": "https://example.com/long",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", result["truncated"])
	}
	content, ok := result["text"].(string)
	if !ok {
		t.Fatalf("content has type %T, want string", result["text"])
	}
	if len(content) > 100 {
		t.Errorf("content should be truncated to 100 chars, got %d", len(content))
	}
}

func TestWebFetchTool_NonHTTPScheme(t *testing.T) {
	tool := NewWebFetchTool(0)
	_, err := tool.Execute(context.Background(), map[string]any{
		"url": "ftp://example.com/file",
	})
	if err == nil {
		t.Fatal("expected error for non-http URL, got nil")
	}
}

func TestWebFetchTool_MissingURL(t *testing.T) {
	tool := NewWebFetchTool(0)
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing url, got nil")
	}
}

func TestWebFetchTool_UserAgentHeader(t *testing.T) {
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/plain",
		Body:        "ok",
	})

	tool := NewWebFetchTool(0, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	_, err := tool.Execute(context.Background(), map[string]any{
		"url": "https://example.com",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rt.calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	gotUA := rt.calls[0].Header.Get("User-Agent")
	if gotUA != userAgent {
		t.Errorf("User-Agent: got %q, want %q", gotUA, userAgent)
	}
}

func TestWebFetchTool_URLInResult(t *testing.T) {
	const targetURL = "https://example.com/specific-page"
	rt := newMockRoundTripper(mockHTTPResponse{
		StatusCode:  200,
		ContentType: "text/plain",
		Body:        "content",
	})

	tool := NewWebFetchTool(0, WithWebFetchHTTPClient(&http.Client{Transport: rt}))
	msgs, err := tool.Execute(context.Background(), map[string]any{
		"url": targetURL,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["url"] != targetURL {
		t.Errorf("url in result: got %v, want %v", result["url"], targetURL)
	}
}
