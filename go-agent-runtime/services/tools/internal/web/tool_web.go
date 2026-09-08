package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

type SearchProvider interface {
	Search(ctx context.Context, query string, count int) (string, error)
}

type BraveSearchProvider struct {
	apiKey           string
	httpClient       *http.Client
	diagnosticWriter io.Writer
}

func (p *BraveSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
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

	var searchResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		if p.diagnosticWriter != nil {
			writeWebDiagnostic(p.diagnosticWriter, "Brave API response could not be decoded: %s\n", string(body))
		}
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s", query))
	for i, item := range results {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Description != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Description))
		}
	}

	return strings.Join(lines, "\n"), nil
}

type TavilySearchProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func (p *TavilySearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := p.baseURL
	if searchURL == "" {
		searchURL = "https://api.tavily.com/search"
	}

	payload := map[string]any{
		"api_key":             p.apiKey,
		"query":               query,
		"search_depth":        "advanced",
		"include_answer":      false,
		"include_images":      false,
		"include_raw_content": false,
		"max_results":         count,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
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
		return "", fmt.Errorf("tavily api error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via Tavily)", query))
	for i, item := range results {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Content != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Content))
		}
	}

	return strings.Join(lines, "\n"), nil
}

type DuckDuckGoSearchProvider struct {
	httpClient *http.Client
}

func (p *DuckDuckGoSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
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

	return p.extractResults(string(body), count, query)
}

func (p *DuckDuckGoSearchProvider) extractResults(html string, count int, query string) (string, error) {
	// Simple regex based extraction for DDG HTML
	// Strategy: Find all result containers or key anchors directly

	// Try finding the result links directly first, as they are the most critical
	// Pattern: <a class="result__a" href="...">Title</a>
	// The previous regex was a bit strict. Let's make it more flexible for attributes order/content
	reLink := regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([\s\S]*?)</a>`)
	matches := reLink.FindAllStringSubmatch(html, count+searchMatchReserve)

	if len(matches) == 0 {
		return fmt.Sprintf("No results found or extraction failed. Query: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via DuckDuckGo)", query))

	// Pre-compile snippet regex to run inside the loop
	// We'll search for snippets relative to the link position or just globally if needed
	// But simple global search for snippets might mismatch order.
	// Since we only have the raw HTML string, let's just extract snippets globally and assume order matches (risky but simple for regex)
	// Or better: Let's assume the snippet follows the link in the HTML

	// A better regex approach: iterate through text and find matches in order
	// But for now, let's grab all snippets too
	reSnippet := regexp.MustCompile(`<a class="result__snippet[^"]*".*?>([\s\S]*?)</a>`)
	snippetMatches := reSnippet.FindAllStringSubmatch(html, count+searchMatchReserve)
	lines = append(lines, formatDuckDuckGoResults(matches, snippetMatches, count)...)

	return strings.Join(lines, "\n"), nil
}

const searchMatchReserve = 5

func closeWebResponseBody(body io.Closer) {
	if body == nil {
		return
	}
	if err := body.Close(); err != nil {
		return
	}
}

func writeWebDiagnostic(writer io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(writer, format, args...); err != nil {
		return
	}
}

func formatDuckDuckGoResults(matches, snippetMatches [][]string, count int) []string {
	lines := make([]string, 0, min(len(matches), count)*2)
	for i := 0; i < min(len(matches), count); i++ {
		urlStr := decodeDuckDuckGoURL(matches[i][1])
		title := strings.TrimSpace(stripTags(matches[i][2]))
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, urlStr))
		if i < len(snippetMatches) {
			if snippet := strings.TrimSpace(stripTags(snippetMatches[i][1])); snippet != "" {
				lines = append(lines, fmt.Sprintf("   %s", snippet))
			}
		}
	}
	return lines
}

func decodeDuckDuckGoURL(urlStr string) string {
	if !strings.Contains(urlStr, "uddg=") {
		return urlStr
	}
	u, err := url.QueryUnescape(urlStr)
	if err != nil {
		return urlStr
	}
	idx := strings.Index(u, "uddg=")
	if idx == -1 {
		return urlStr
	}
	return u[idx+5:]
}

func stripTags(content string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return re.ReplaceAllString(content, "")
}
