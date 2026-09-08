package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	perplexityMaxTokens         = 1000
	webSearchDefaultTimeout     = 30 * time.Second
	webSearchDefaultResultCount = 5
	webSearchMinimumResultCount = 1
	webSearchMaximumResultCount = 10
)

type PerplexitySearchProvider struct {
	apiKey     string
	httpClient *http.Client
}

func (p *PerplexitySearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := "https://api.perplexity.ai/chat/completions"

	payload := map[string]any{
		"model": "sonar",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a search assistant. Provide concise search results with titles, URLs, and brief descriptions in the following format:\n1. Title\n   URL\n   Description\n\nDo not add extra commentary.",
			},
			{
				"role":    "user",
				"content": fmt.Sprintf("Search for: %s. Provide up to %d relevant results.", query, count),
			},
		},
		"max_tokens": perplexityMaxTokens,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("User-Agent", userAgent)

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: webSearchDefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer closeWebResponseBody(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("perplexity API error: %s", string(body))
	}

	var searchResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(searchResp.Choices) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	return fmt.Sprintf("Results for: %s (via Perplexity)\n%s", query, searchResp.Choices[0].Message.Content), nil
}

type WebSearchTool struct {
	provider   SearchProvider
	maxResults int
}

// WebSearchToolOptions configures which search provider to use and how.
type WebSearchToolOptions struct {
	BraveAPIKey          string
	BraveMaxResults      int
	BraveEnabled         bool
	TavilyAPIKey         string
	TavilyBaseURL        string
	TavilyMaxResults     int
	TavilyEnabled        bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	PerplexityAPIKey     string
	PerplexityMaxResults int
	PerplexityEnabled    bool
	// HTTPClient is an optional HTTP client injected into the active search provider.
	// When set, all provider HTTP requests use this client instead of creating a default one.
	// Primarily useful for testing with a custom round tripper.
	HTTPClient *http.Client
	// DiagnosticWriter receives provider diagnostics when a host wants
	// observability. A nil writer keeps the reusable service silent.
	DiagnosticWriter io.Writer
}

func NewWebSearchTool(opts WebSearchToolOptions) *WebSearchTool {
	provider, maxResults := selectSearchProvider(opts)
	if provider == nil {
		return nil
	}
	return &WebSearchTool{
		provider:   provider,
		maxResults: maxResults,
	}
}

func selectSearchProvider(opts WebSearchToolOptions) (SearchProvider, int) {
	if opts.PerplexityEnabled && opts.PerplexityAPIKey != "" {
		return &PerplexitySearchProvider{apiKey: opts.PerplexityAPIKey, httpClient: opts.HTTPClient}, positiveOrDefault(opts.PerplexityMaxResults)
	}
	if opts.BraveEnabled && opts.BraveAPIKey != "" {
		return &BraveSearchProvider{apiKey: opts.BraveAPIKey, httpClient: opts.HTTPClient, diagnosticWriter: opts.DiagnosticWriter}, positiveOrDefault(opts.BraveMaxResults)
	}
	if opts.TavilyEnabled && opts.TavilyAPIKey != "" {
		return &TavilySearchProvider{apiKey: opts.TavilyAPIKey, baseURL: opts.TavilyBaseURL, httpClient: opts.HTTPClient}, positiveOrDefault(opts.TavilyMaxResults)
	}
	if opts.DuckDuckGoEnabled {
		return &DuckDuckGoSearchProvider{httpClient: opts.HTTPClient}, positiveOrDefault(opts.DuckDuckGoMaxResults)
	}
	return nil, 0
}

func positiveOrDefault(value int) int {
	if value > 0 {
		return value
	}
	return webSearchDefaultResultCount
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

func (t *WebSearchTool) Description() string {
	return "Search the web for current information. Returns titles, URLs, and snippets from search results."
}

func (t *WebSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of results (1-10)",
				"minimum":     float64(webSearchMinimumResultCount),
				"maximum":     float64(webSearchMaximumResultCount),
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	query, ok := args["query"].(string)
	if !ok {
		return nil, fmt.Errorf("query is required")
	}

	count := t.maxResults
	if c, ok := args["count"].(float64); ok {
		if int(c) >= webSearchMinimumResultCount && int(c) <= webSearchMaximumResultCount {
			count = int(c)
		}
	}

	result, err := t.provider.Search(ctx, query, count)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, result)}, nil
}
