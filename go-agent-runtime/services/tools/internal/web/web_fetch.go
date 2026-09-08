package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	webFetchMinimumCharacters     = 100
	webFetchDefaultTimeout        = 60 * time.Second
	webFetchIdleConnectionTimeout = 30 * time.Second
	webFetchTLSHandshakeTimeout   = 15 * time.Second
	webFetchMaximumRedirects      = 5
)

// WebFetchToolOption configures a WebFetchTool.
type WebFetchToolOption func(*WebFetchTool)

// WithWebFetchHTTPClient sets a custom HTTP client on the WebFetchTool.
// When set, all fetch requests use this client instead of the default one.
// Primarily useful for testing with a custom round tripper.
func WithWebFetchHTTPClient(client *http.Client) WebFetchToolOption {
	return func(t *WebFetchTool) {
		t.httpClient = client
	}
}

type WebFetchTool struct {
	maxChars   int
	httpClient *http.Client
}

func NewWebFetchTool(maxChars int, opts ...WebFetchToolOption) *WebFetchTool {
	if maxChars <= 0 {
		maxChars = 50000
	}
	t := &WebFetchTool{maxChars: maxChars}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *WebFetchTool) Name() string {
	return "web_fetch"
}

func (t *WebFetchTool) Description() string {
	return "Fetch a URL and extract readable content (HTML to text). Use this to get weather info, news, articles, or any web content."
}

func (t *WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "URL to fetch",
			},
			"maxChars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters to extract",
				"minimum":     float64(webFetchMinimumCharacters),
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	urlStr, maxChars, err := fetchArguments(args, t.maxChars)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := t.fetchClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer closeWebResponseBody(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	text, extractor := t.decodeBody(body, resp.Header.Get("Content-Type"))
	return fetchResponse(urlStr, resp.StatusCode, extractor, text, maxChars), nil
}

func fetchArguments(args map[string]any, defaultMax int) (string, int, error) {
	urlStr, ok := args["url"].(string)
	if !ok {
		return "", 0, fmt.Errorf("url is required")
	}
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", 0, fmt.Errorf("only http/https URLs are allowed")
	}
	if parsedURL.Host == "" {
		return "", 0, fmt.Errorf("missing domain in URL")
	}
	maxChars := defaultMax
	if mc, ok := args["maxChars"].(float64); ok && int(mc) > webFetchMinimumCharacters {
		maxChars = int(mc)
	}
	return urlStr, maxChars, nil
}

func (t *WebFetchTool) fetchClient() *http.Client {
	if t.httpClient != nil {
		return t.httpClient
	}
	return &http.Client{
		Timeout: webFetchDefaultTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     webFetchIdleConnectionTimeout,
			DisableCompression:  false,
			TLSHandshakeTimeout: webFetchTLSHandshakeTimeout,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webFetchMaximumRedirects {
				return fmt.Errorf("stopped after %d redirects", webFetchMaximumRedirects)
			}
			return nil
		},
	}
}

func (t *WebFetchTool) decodeBody(body []byte, contentType string) (string, string) {
	if strings.Contains(contentType, "application/json") {
		var jsonData any
		if err := json.Unmarshal(body, &jsonData); err == nil {
			formatted, err := json.MarshalIndent(jsonData, "", "  ")
			if err != nil {
				return string(body), "raw"
			}
			return string(formatted), "json"
		}
		return string(body), "raw"
	}
	if strings.Contains(contentType, "text/html") || isHTMLBody(body) {
		return t.extractText(string(body)), "text"
	}
	return string(body), "raw"
}

func isHTMLBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	content := string(body)
	return strings.HasPrefix(content, "<!DOCTYPE") || strings.HasPrefix(strings.ToLower(content), "<html")
}

func fetchResponse(urlStr string, status int, extractor, text string, maxChars int) []messages.Message {
	truncated := len(text) > maxChars
	if truncated {
		text = text[:maxChars]
	}
	result := map[string]any{
		"url": urlStr, "status": status, "extractor": extractor,
		"truncated": truncated, "length": len(text), "text": text,
	}
	resultJSON := marshalFetchResult(result)
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, string(resultJSON))}
}

func marshalFetchResult(result map[string]any) []byte {
	resultJSON, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return []byte(`{"status":"serialization_failed","text":"web fetch result could not be serialized"}`)
	}
	return resultJSON
}

func (t *WebFetchTool) extractText(htmlContent string) string {
	re := regexp.MustCompile(`<script[\s\S]*?</script>`)
	result := re.ReplaceAllLiteralString(htmlContent, "")
	re = regexp.MustCompile(`<style[\s\S]*?</style>`)
	result = re.ReplaceAllLiteralString(result, "")
	re = regexp.MustCompile(`<[^>]+>`)
	result = re.ReplaceAllLiteralString(result, "")

	result = strings.TrimSpace(result)

	re = regexp.MustCompile(`[^\S\n]+`)
	result = re.ReplaceAllString(result, " ")
	re = regexp.MustCompile(`\n{3,}`)
	result = re.ReplaceAllString(result, "\n\n")

	lines := strings.Split(result, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, "\n")
}
