package chrome

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	lockedChromeChannel  = "Stable"
	lockedChromePlatform = "mac-arm64"
	lockedChromeVersion  = "152.0.7977.64"
	lockedChromeRevision = "1669021"
	lockedChromeSHA256   = "10033804338bd0a5aa098149a8dd64f3f2e0e8b201bf3d400d7c17d067ff696f"

	completeToolName = "webmcp_lane_d_complete"
	pendingToolName  = "webmcp_lane_d_pending"
	cancelToolName   = "webmcp_lane_d_cancel"
	slowToolName     = "webmcp_lane_d_slow_autosubmit"
)

// The fixture is deliberately local and self-contained. It is only acquired
// by the explicitly opted-in test below.
//
//go:embed testdata/webmcp_adapter.html
var chromeAdapterFixtureHTML []byte

type chromeForTestingLock = ChromeForTestingLock

type fixtureOracle struct {
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Pending     bool     `json:"pending"`
	Invocations []string `json:"invocations"`
}

type fixtureServer struct {
	server *httptest.Server

	mu     sync.Mutex
	oracle fixtureOracle
}

// liveCDPProxy adds one observable hold to the browser HTTP surface while
// leaving the browser WebSocket endpoint in the version response untouched.
// This makes a real CLI selection stop in target resolution without changing
// the production command or browser process.
type liveCDPProxy struct {
	server   *httptest.Server
	upstream string
	client   *http.Client

	mu            sync.Mutex
	browserDead   bool
	delayNextList bool
	listAdmitted  chan struct{}
	admitOnce     sync.Once
	releaseOnce   sync.Once
	releaseList   chan struct{}
}

func newLiveCDPProxy(upstream string) *liveCDPProxy {
	proxy := &liveCDPProxy{
		upstream:     strings.TrimRight(upstream, "/"),
		client:       &http.Client{Timeout: 2 * time.Second},
		listAdmitted: make(chan struct{}),
		releaseList:  make(chan struct{}),
	}
	proxy.server = httptest.NewServer(http.HandlerFunc(proxy.handle))
	return proxy
}

func (p *liveCDPProxy) URL() string {
	if p == nil || p.server == nil {
		return ""
	}
	return p.server.URL
}

func (p *liveCDPProxy) DelayNextList() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.delayNextList = true
	p.mu.Unlock()
}

func (p *liveCDPProxy) MarkBrowserDead() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.browserDead = true
	p.mu.Unlock()
}

func (p *liveCDPProxy) ListAdmitted() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.listAdmitted
}

func (p *liveCDPProxy) ReleaseList() {
	if p == nil {
		return
	}
	p.releaseOnce.Do(func() { close(p.releaseList) })
}

func (p *liveCDPProxy) Close() {
	if p == nil {
		return
	}
	p.ReleaseList()
	if p.server != nil {
		p.server.Close()
	}
}

func (p *liveCDPProxy) handle(writer http.ResponseWriter, request *http.Request) {
	delay := false
	var dead bool
	if request.URL.Path == jsonListPath {
		p.mu.Lock()
		delay = p.delayNextList
		p.delayNextList = false
		dead = p.browserDead
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		dead = p.browserDead
		p.mu.Unlock()
	}
	if delay {
		p.admitOnce.Do(func() { close(p.listAdmitted) })
		<-p.releaseList
		// The test releases this request only after killing Chrome. Returning a
		// bounded upstream failure avoids retaining an HTTP handler while a
		// platform Chrome child may still own the DevTools port.
		closeLiveCDPProxyConnection(writer)
		return
	}
	if dead {
		closeLiveCDPProxyConnection(writer)
		return
	}

	upstreamRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, p.upstream+request.URL.Path, nil)
	if err != nil {
		http.Error(writer, "invalid upstream request", http.StatusBadGateway)
		return
	}
	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		http.Error(writer, "upstream browser unavailable", http.StatusBadGateway)
		return
	}
	defer closeAfterRead(response.Body)
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	discardSecondaryError(func() error { _, err := io.Copy(writer, response.Body); return err })
}

func closeLiveCDPProxyConnection(writer http.ResponseWriter) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return
	}
	connection, _, err := hijacker.Hijack()
	if err == nil {
		discardSecondaryError(connection.Close)
	}
}

var devToolsEndpointPattern = regexp.MustCompile(`DevTools listening on (ws://127\.0\.0\.1:[0-9]+/devtools/browser/[^[:space:]]+)`)

func newFixtureServer() *fixtureServer {
	fixture := &fixtureServer{oracle: fixtureOracle{Value: fixtureOracleInitial, VisibleText: fixtureOracleInitial}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			if request.Method != http.MethodGet {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Origin-Agent-Cluster", "?1")
			writer.Header().Set("Permissions-Policy", "tools=(self)")
			writeFixtureBody(writer, chromeAdapterFixtureHTML)
		case "/__test/state":
			fixture.handleOracle(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	return fixture
}

func (f *fixtureServer) URL() string {
	return f.server.URL + "/"
}

func (f *fixtureServer) StateURL() string {
	return f.server.URL + "/__test/state"
}

func (f *fixtureServer) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *fixtureServer) handleOracle(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		encodeFixtureJSON(writer, f.oracle)
	case http.MethodPost:
		var oracle fixtureOracle
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&oracle); err != nil {
			http.Error(writer, "invalid oracle", http.StatusBadRequest)
			return
		}
		oracle.Invocations = append([]string(nil), oracle.Invocations...)
		f.oracle = oracle
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func assertFixtureHeaders(t *testing.T, ctx context.Context, fixtureURL string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixtureURL, nil)
	if err != nil {
		t.Fatalf("create fixture request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read fixture headers: %v", err)
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fixture status = %s, want 200", response.Status)
	}
	if response.Header.Get("Origin-Agent-Cluster") != "?1" || response.Header.Get("Permissions-Policy") != "tools=(self)" {
		t.Fatalf("fixture isolation headers = Origin-Agent-Cluster %q Permissions-Policy %q", response.Header.Get("Origin-Agent-Cluster"), response.Header.Get("Permissions-Policy"))
	}
}

func findFixtureTarget(targets []webmcp.Target, fixtureURL string) (webmcp.Target, error) {
	var matches []webmcp.Target
	for _, target := range targets {
		if target.Type == pageTargetType && target.URL == fixtureURL {
			matches = append(matches, target)
		}
	}
	if len(matches) != 1 {
		return webmcp.Target{}, fmt.Errorf("found %d page targets for fixture URL %q", len(matches), fixtureURL)
	}
	return matches[0], nil
}

func hasTool(tools []webmcp.ToolDescriptor, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func findIntegrationTools(tools []webmcp.ToolDescriptor) (webmcp.ToolDescriptor, webmcp.ToolDescriptor, webmcp.ToolDescriptor, error) {
	var complete, pending, cancel webmcp.ToolDescriptor
	for _, tool := range tools {
		switch tool.Name {
		case completeToolName:
			complete = tool
		case pendingToolName:
			pending = tool
		case cancelToolName:
			cancel = tool
		}
	}
	if complete.Name == "" || pending.Name == "" || cancel.Name == "" {
		return webmcp.ToolDescriptor{}, webmcp.ToolDescriptor{}, webmcp.ToolDescriptor{}, fmt.Errorf("toolsAdded omitted complete/pending declarative or cancel imperative tool: %+v", tools)
	}
	return complete, pending, cancel, nil
}

func waitForIntegrationEvent(ctx context.Context, events <-chan webmcp.BrowserEvent, label string, match func(webmcp.BrowserEvent) bool) (webmcp.BrowserEvent, error) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return webmcp.BrowserEvent{}, fmt.Errorf("%s: event channel closed", label)
			}
			if match(event) {
				return event, nil
			}
		case <-ctx.Done():
			return webmcp.BrowserEvent{}, fmt.Errorf("%s: %w", label, ctx.Err())
		}
	}
}

func waitForFixtureOracle(ctx context.Context, endpoint string, match func(fixtureOracle) bool) (fixtureOracle, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last fixtureOracle
	var lastErr error
	for {
		oracle, err := readFixtureOracle(ctx, endpoint)
		if err == nil {
			last = oracle
			if match(oracle) {
				return oracle, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("wait for fixture oracle: %w (last=%+v err=%w)", ctx.Err(), last, lastErr)
		case <-ticker.C:
		}
	}
}

func readFixtureOracle(ctx context.Context, endpoint string) (fixtureOracle, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fixtureOracle{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fixtureOracle{}, err
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode != http.StatusOK {
		return fixtureOracle{}, fmt.Errorf("fixture oracle HTTP status: %s", response.Status)
	}
	var oracle fixtureOracle
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&oracle); err != nil {
		return fixtureOracle{}, err
	}
	return oracle, nil
}

func waitForFixtureTarget(ctx context.Context, baseURL string, targetID webmcp.TargetID, fixtureURL string, wantPresent bool) (devToolsTarget, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		targets, err := readDevToolsTargets(ctx, baseURL)
		lastErr = err
		if err == nil {
			target, present := findDevToolsFixtureTarget(targets, targetID, fixtureURL)
			if present == wantPresent {
				return target, nil
			}
			lastErr = fmt.Errorf("target present=%t", present)
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for target presence=%t: %w (last error: %w)", wantPresent, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// findDevToolsFixtureTarget returns the DevTools target with targetID at fixtureURL.
func findDevToolsFixtureTarget(targets []devToolsTarget, targetID webmcp.TargetID, fixtureURL string) (devToolsTarget, bool) {
	for _, target := range targets {
		if target.ID == string(targetID) && target.URL == fixtureURL {
			return target, true
		}
	}
	return devToolsTarget{}, false
}

type inspectedPageState struct {
	URL         string   `json:"url"`
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Pending     bool     `json:"pending"`
	Invocations []string `json:"invocations"`
}

func inspectExternalTarget(ctx context.Context, endpoint, targetID string) (state inspectedPageState, err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("#state")); err != nil {
		return inspectedPageState{}, fmt.Errorf("attach target for direct page verification: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(pageStateExpression(), &state)); err != nil {
		return inspectedPageState{}, fmt.Errorf("read direct page state: %w", err)
	}
	return state, nil
}

func pageStateExpression() string {
	return `(() => {
  const state = window.__webmcpLaneD;
  const visible = document.querySelector("#state");
  return {
    url: location.href,
    ready: Boolean(state && state.ready),
    value: state && state.value !== undefined ? String(state.value) : "",
    visibleText: visible ? String(visible.textContent || "") : "",
    pending: Boolean(state && state.pending),
    invocations: state && Array.isArray(state.invocations)
      ? state.invocations.map((value) => String(value))
      : []
  };
})()`
}

func detachExternalIntegrationTarget(targetContext context.Context, cancelTarget context.CancelFunc) error {
	client := chromedp.FromContext(targetContext)
	if client == nil || client.Browser == nil || client.Target == nil {
		cancelTarget()
		return nil
	}
	targetClient := client.Target
	var detachErr error
	if targetClient.SessionID != "" {
		detachContext, cancelDetach := context.WithTimeout(context.WithoutCancel(targetContext), 5*time.Second)
		detachErr = cdpTarget.DetachFromTarget().WithSessionID(targetClient.SessionID).Do(cdp.WithExecutor(detachContext, client.Browser))
		cancelDetach()
	}
	// Clear the protocol IDs before cancellation; chromedp otherwise follows a
	// target-context cancellation with Target.closeTarget in this pinned
	// version. Keep the pointer until cancelTarget returns because chromedp's
	// cleanup goroutine reads it without synchronization. The test client is
	// never allowed to close the external page.
	targetClient.SessionID = ""
	targetClient.TargetID = ""
	cancelTarget()
	client.Target = nil
	return detachErr
}

func assertPageStateMatchesOracle(t *testing.T, state inspectedPageState, oracle fixtureOracle) {
	t.Helper()
	if !state.Ready || state.Value != oracle.Value || state.VisibleText != oracle.VisibleText || state.Pending != oracle.Pending {
		t.Fatalf("direct page state = %+v, oracle = %+v", state, oracle)
	}
}
