package chrome

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Runtime is a browser-neutral WebMCP runtime backed by one explicit Chrome
// DevTools endpoint per Open call.
type Runtime struct {
	options RuntimeOptions
}

// NewRuntime constructs a production Chrome adapter.
func NewRuntime(options ...Option) *Runtime {
	resolved := defaultRuntimeOptions()
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	if resolved.EventBuffer <= 0 {
		resolved.EventBuffer = defaultEventBuffer
	}
	if resolved.CommandTimeout <= 0 {
		resolved.CommandTimeout = defaultCommandTimeout
	}
	if resolved.HTTPClient == nil {
		resolved.HTTPClient = httpDefaultClient()
	}
	return &Runtime{options: resolved}
}

// NewAdapter is an expressive alias for callers that name the concrete
// implementation rather than the neutral runtime it supplies.
func NewAdapter(options ...Option) *Runtime {
	return NewRuntime(options...)
}

// Open connects to the supplied browser endpoint. The endpoint is explicit;
// this adapter does not discover browsers or silently select a different one.
func (r *Runtime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := contextError(ctx); err != nil {
		return nil, classifiedOpenError(candidate, err)
	}
	endpoint, err := browserEndpoint(candidate)
	if err != nil {
		return nil, classifiedOpenError(candidate, err)
	}

	allocatorOptions := make([]chromedp.RemoteAllocatorOption, 0, 1)
	if isFullBrowserWebSocket(endpoint) {
		allocatorOptions = append(allocatorOptions, chromedp.NoModifyURL)
	}
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(ctx, endpoint, allocatorOptions...)
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	browserData := chromedp.FromContext(browserContext)
	if browserData == nil || browserData.Allocator == nil {
		cancelBrowser()
		cancelAllocator()
		return nil, classifiedOpenError(candidate, errors.New("invalid browser context"))
	}

	browser, err := browserData.Allocator.Allocate(browserContext)
	if err != nil {
		cancelBrowser()
		cancelAllocator()
		return nil, classifiedOpenError(candidate, err)
	}
	browserData.Browser = browser

	handle := &handle{
		candidate:       cloneCandidate(candidate),
		browserContext:  browserContext,
		cancelBrowser:   cancelBrowser,
		cancelAllocator: cancelAllocator,
		browser:         browser,
		httpClient:      r.options.HTTPClient,
		commandTimeout:  r.options.CommandTimeout,
		eventBuffer:     r.options.EventBuffer,
		sessions:        make(map[*targetSession]struct{}),
		done:            make(chan struct{}),
	}
	go handle.watchBrowserConnection()
	return handle, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func browserEndpoint(candidate webmcp.BrowserCandidate) (string, error) {
	endpoint := strings.TrimSpace(candidate.BrowserWSURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(candidate.HTTPURL)
	}
	if endpoint == "" {
		return "", errors.New("browser endpoint is empty")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("browser endpoint is invalid")
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("browser endpoint scheme is unsupported")
	}
	return endpoint, nil
}

func isFullBrowserWebSocket(endpoint string) bool {
	return (strings.HasPrefix(endpoint, "ws://") || strings.HasPrefix(endpoint, "wss://")) &&
		strings.Contains(endpoint, "/devtools/browser/")
}

func httpDefaultClient() *http.Client {
	return http.DefaultClient
}

func cloneCandidate(candidate webmcp.BrowserCandidate) webmcp.BrowserCandidate {
	candidate.Diagnostics = append([]webmcp.Diagnostic(nil), candidate.Diagnostics...)
	for i := range candidate.Diagnostics {
		if candidate.Diagnostics[i].Details != nil {
			candidate.Diagnostics[i].Details = cloneDetails(candidate.Diagnostics[i].Details)
		}
	}
	return candidate
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}

var (
	_ webmcp.BrowserRuntime = (*Runtime)(nil)
)
