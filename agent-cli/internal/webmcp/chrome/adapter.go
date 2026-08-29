package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
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

	endpoint, err = r.resolveBrowserWebSocket(ctx, endpoint)
	if err != nil {
		return nil, classifiedOpenError(candidate, err)
	}

	// NewRemoteAllocator's cancellation path calls chromedp.Cancel on its
	// root context. In chromedp v0.16.0 that root is considered the first
	// context and may issue Browser.close while a remote browser is still
	// owned by the caller. Keep the websocket connection under a plain client
	// context instead; canceling it only closes this adapter's connection.
	allocatorContext, cancelAllocator := context.WithCancel(ctx)
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	browserData := chromedp.FromContext(browserContext)
	if browserData == nil || browserData.Allocator == nil {
		cancelBrowser()
		cancelAllocator()
		return nil, classifiedOpenError(candidate, errors.New("invalid browser context"))
	}

	browser, err := chromedp.NewBrowser(allocatorContext, endpoint)
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
		browserExecutor: browser,
		httpClient:      r.options.HTTPClient,
		commandTimeout:  r.options.CommandTimeout,
		eventBuffer:     r.options.EventBuffer,
		wireTrace:       r.options.WireTrace,
		sessions:        make(map[*targetSession]struct{}),
		done:            make(chan struct{}),
		disconnectDone:  make(chan struct{}),
	}
	go handle.watchBrowserConnection()
	return handle, nil
}

// Version connects to the explicit endpoint, reads Browser.getVersion, and
// returns only the neutral version fields. The short-lived browser handle is
// closed before returning; this method is the DevToolsCatalog seam for callers
// that need protocol metadata without retaining a session.
func (r *Runtime) Version(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
	handleValue, err := r.Open(ctx, candidate)
	if err != nil {
		return webmcp.BrowserVersion{}, err
	}
	defer func() { _ = handleValue.Close() }()

	handle, ok := handleValue.(*handle)
	if !ok {
		return webmcp.BrowserVersion{}, classifiedOpenError(candidate, errors.New("browser handle has an unsupported implementation"))
	}
	commandContext, releaseContext := handle.operationContext(ctx)
	defer releaseContext()
	executor := handle.executor()
	if executor == nil {
		if handle.isDisconnected() {
			return webmcp.BrowserVersion{}, handle.disconnectError("", "version", nil)
		}
		return webmcp.BrowserVersion{}, classifiedHandleError(candidate, webmcp.ErrorBrowserProtocol, "version", errors.New("browser connection is unavailable"))
	}
	protocolVersion, product, _, _, _, err := browser.GetVersion().Do(cdp.WithExecutor(commandContext, executor))
	if err != nil {
		if handle.isDisconnected() {
			return webmcp.BrowserVersion{}, handle.disconnectError("", "version", err)
		}
		return webmcp.BrowserVersion{}, classifiedHandleError(candidate, webmcp.ErrorBrowserProtocol, "version", err)
	}
	if handle.isDisconnected() {
		return webmcp.BrowserVersion{}, handle.disconnectError("", "version", nil)
	}
	return webmcp.BrowserVersion{
		Browser:              product,
		ProtocolVersion:      protocolVersion,
		WebSocketDebuggerURL: candidate.BrowserWSURL,
		BrowserInstanceID:    candidate.BrowserInstanceID,
	}, nil
}

// ListTargets opens the explicit endpoint for one normalized target snapshot
// and releases the browser handle after the snapshot is copied.
func (r *Runtime) ListTargets(ctx context.Context, candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	handle, err := r.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	return handle.ListTargets(ctx)
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

func (r *Runtime) resolveBrowserWebSocket(ctx context.Context, endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("browser endpoint is invalid")
	}
	if parsed.Scheme == "ws" || parsed.Scheme == "wss" {
		if strings.Contains(parsed.Path, "/devtools/browser/") {
			return endpoint, nil
		}
		parsed.Scheme = map[string]string{"ws": "http", "wss": "https"}[parsed.Scheme]
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("browser endpoint scheme is unsupported")
	}

	requestURL := *parsed
	requestURL.Path = "/json/version"
	requestURL.RawQuery = ""
	requestURL.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return "", errors.New("browser version endpoint is invalid")
	}
	client := r.options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", errors.New("browser version endpoint is unavailable")
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&version); err != nil {
		return "", errors.New("browser version response is invalid")
	}
	websocket := strings.TrimSpace(version.WebSocketDebuggerURL)
	parsedWebsocket, err := url.Parse(websocket)
	if err != nil || parsedWebsocket.Host == "" || (parsedWebsocket.Scheme != "ws" && parsedWebsocket.Scheme != "wss") {
		return "", errors.New("browser version response has no valid websocket")
	}
	return websocket, nil
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
	_ webmcp.BrowserRuntime  = (*Runtime)(nil)
	_ webmcp.DevToolsCatalog = (*Runtime)(nil)
)
