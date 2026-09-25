package chrome

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime"
	"slices"
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
	chromeIntegrationEnv = "WEBMCP_CHROME_INTEGRATION"

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
	dead := false
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

func TestPinnedChromeWebMCPAdapterIntegration(t *testing.T) {
	// This check must remain the first observable operation: ordinary tests
	// neither read the O0 lock nor make a network request or start Chrome.
	if os.Getenv(chromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome integration proof", chromeIntegrationEnv)
	}

	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	run := launchAdapterIntegration(t, ctx)
	handle := run.openHandle(t, ctx)
	defer func() {
		if closeErr := handle.Close(); closeErr != nil {
			t.Errorf("adapter handle cleanup: %v", closeErr)
		}
	}()
	session := run.attachExternal(t, ctx, handle)
	cancelTool, completedID, completed := run.invokeCompleted(t, ctx, session)
	pendingID := run.startPending(t, ctx, session, cancelTool)
	cancelObservationContext, cancelObservation := context.WithTimeout(ctx, 10*time.Second)
	defer cancelObservation()
	canceled, cancelTrace, pendingOracle := run.observeCancellation(t, ctx, cancelObservationContext, session, pendingID)

	if err := session.Close(); err != nil {
		t.Fatalf("detach external target session: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close adapter handle after external detach: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, run.baseURL, run.selectedTarget.ID, run.fixtureURL, true); err != nil {
		t.Fatalf("target after adapter detach: %v", err)
	}
	afterDetach, afterReattach := run.verifyDetachAndReattach(t, ctx, pendingOracle)

	t.Logf("WEBMCP_WIRE_CANCEL_PASS chrome=%s revision=%s browser=%s target=%s target_session=%s method=%s invocation=%s phase=%s listener_ready=%t", lockedChromeVersion, lockedChromeRevision, cancelTrace.BrowserID, cancelTrace.TargetID, cancelTrace.TargetSessionID, cancelTrace.Method, cancelTrace.InvocationID, cancelTrace.Phase, cancelTrace.ListenerReady)
	t.Logf("WEBMCP_INTEGRATION_PASS chrome=%s revision=%s platform=%s target=%s listener_before_enable=true completed=%s/%s canceled=%s/%s state_after_detach=%q state_after_reattach=%q", lockedChromeVersion, lockedChromeRevision, lockedChromePlatform, run.selectedTarget.ID, completedID, completed.Status, pendingID, canceled.Status, afterDetach.VisibleText, afterReattach.VisibleText)
}

// adapterIntegrationRun carries the pinned browser, fixture, and neutral
// adapter shared by the phases of the adapter integration proof.
type adapterIntegrationRun struct {
	fixture             *fixtureServer
	fixtureURL          string
	browser             *runningChrome
	baseURL             string
	version             devToolsVersion
	targetBeforeAdapter devToolsTarget
	candidate           webmcp.BrowserCandidate
	wire                *wireTraceRecorder
	adapter             *Runtime
	selectedTarget      webmcp.Target
}

func launchAdapterIntegration(t *testing.T, ctx context.Context) *adapterIntegrationRun {
	t.Helper()
	workDir := t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	run := &adapterIntegrationRun{fixture: newFixtureServer()}
	t.Cleanup(func() { run.fixture.Close() })
	run.fixtureURL = run.fixture.URL()
	assertFixtureHeaders(t, ctx, run.fixtureURL)

	run.browser, err = launchPinnedChrome(ctx, pinned, run.fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := run.browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})

	run.baseURL = browserHTTPURL(run.browser.endpoint())
	run.version, err = waitForDevToolsVersion(ctx, run.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if run.version.WebSocketDebuggerURL != run.browser.endpoint() {
		t.Fatalf("DevTools websocket = %q, launch announcement = %q", run.version.WebSocketDebuggerURL, run.browser.endpoint())
	}
	run.targetBeforeAdapter, err = waitForFixturePageTarget(ctx, browserHTTPURL(run.browser.endpoint()), run.fixtureURL)
	if err != nil {
		t.Fatalf("discover exact external fixture target before adapter attach: %v", err)
	}

	run.candidate = webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID("chrome-cft-" + lockedChromeVersion),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      run.version.Browser,
		Protocol:     run.version.ProtocolVersion,
		HTTPURL:      run.baseURL,
		BrowserWSURL: run.version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	run.wire = &wireTraceRecorder{}
	run.adapter = NewRuntime(WithEventBuffer(128), WithCommandTimeout(20*time.Second), WithWireTraceSink(run.wire))
	return run
}

func (r *adapterIntegrationRun) openHandle(t *testing.T, ctx context.Context) webmcp.BrowserHandle {
	t.Helper()
	neutralVersion, err := r.adapter.Version(ctx, r.candidate)
	if err != nil {
		t.Fatalf("neutral BrowserRuntime.Version: %v", err)
	}
	if neutralVersion.Browser != r.version.Browser || neutralVersion.ProtocolVersion != r.version.ProtocolVersion {
		t.Fatalf("neutral version = %+v, want browser=%q protocol=%q", neutralVersion, r.version.Browser, r.version.ProtocolVersion)
	}
	handle, err := r.adapter.Open(ctx, r.candidate)
	if err != nil {
		t.Fatalf("neutral BrowserRuntime.Open: %v", err)
	}
	return handle
}

func (r *adapterIntegrationRun) attachExternal(t *testing.T, ctx context.Context, handle webmcp.BrowserHandle) webmcp.TargetSession {
	t.Helper()
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		t.Fatalf("neutral BrowserHandle.ListTargets: %v", err)
	}
	r.selectedTarget, err = findFixtureTarget(targets, r.fixtureURL)
	if err != nil {
		t.Fatalf("find exact fixture target through neutral target list: %v", err)
	}
	if r.selectedTarget.ID != webmcp.TargetID(r.targetBeforeAdapter.ID) {
		t.Fatalf("neutral target selection ID = %q, pre-attach HTTP discovery ID = %q", r.selectedTarget.ID, r.targetBeforeAdapter.ID)
	}
	if !r.selectedTarget.Eligible || r.selectedTarget.ID == "" {
		t.Fatalf("fixture target = %+v, want eligible exact page target", r.selectedTarget)
	}

	session, err := handle.Attach(ctx, r.selectedTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("neutral BrowserHandle.Attach(%s): %v", r.selectedTarget.ID, err)
	}
	if session.Ownership() != webmcp.TargetOwnershipExternal {
		t.Fatalf("session ownership = %q, want external", session.Ownership())
	}
	if got := session.Context().Key.TargetID; got != r.selectedTarget.ID {
		t.Fatalf("attached target ID = %q, want exact %q", got, r.selectedTarget.ID)
	}
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == fixtureOracleInitial && oracle.VisibleText == fixtureOracleInitial
	}); err != nil {
		t.Fatalf("initial independent page-state oracle: %v", err)
	}
	return session
}

// enableIntegrationTools enables WebMCP after the listeners are installed,
// asserts all three fixture tools, and returns the complete and cancel tools.
func enableIntegrationTools(t *testing.T, ctx context.Context, session webmcp.TargetSession) (webmcp.ToolDescriptor, webmcp.ToolDescriptor) {
	t.Helper()
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("neutral TargetSession.EnableWebMCP: %v", err)
	}
	attached, err := waitForIntegrationEvent(ctx, session.Events(), "target attached", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached
	})
	if err != nil {
		t.Fatal(err)
	}
	added, err := waitForIntegrationEvent(ctx, session.Events(), "declarative toolsAdded", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolsAdded && hasTool(event.Tools, completeToolName) && hasTool(event.Tools, pendingToolName)
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Sequence <= attached.Sequence {
		t.Fatalf("toolsAdded sequence = %d, targetAttached sequence = %d; listener-before-enable order was lost", added.Sequence, attached.Sequence)
	}
	completeTool, pendingTool, cancelTool, err := findIntegrationTools(added.Tools)
	if err != nil {
		t.Fatal(err)
	}
	assertDeclarativeTool(t, completeTool, true)
	assertDeclarativeTool(t, pendingTool, false)
	assertRegisteredTool(t, cancelTool)
	return completeTool, cancelTool
}

func (r *adapterIntegrationRun) invokeCompleted(t *testing.T, ctx context.Context, session webmcp.TargetSession) (webmcp.ToolDescriptor, webmcp.InvocationID, webmcp.BrowserEvent) {
	t.Helper()
	completeTool, cancelTool := enableIntegrationTools(t, ctx, session)
	completedID, err := session.InvokeWebMCP(ctx, completeTool.FrameID, completeTool.Name, json.RawMessage(`{"message":"complete"}`))
	if err != nil {
		t.Fatalf("neutral invoke of declarative tool: %v", err)
	}
	invoked, err := waitForIntegrationEvent(ctx, session.Events(), "toolInvoked for completed call", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == completedID
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(invoked.Input) != `{"message":"complete"}` || invoked.ToolName != completeToolName {
		t.Fatalf("toolInvoked = %+v, want exact object input and declarative tool", invoked)
	}
	completed, err := waitForIntegrationEvent(ctx, session.Events(), "completed toolResponded", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.InvocationID == completedID
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != toolStatusCompleted || !json.Valid(completed.Output) || completed.ErrorCode != "" {
		t.Fatalf("completed response = %+v, want Completed structured output", completed)
	}
	var completedOutput map[string]any
	if err := json.Unmarshal(completed.Output, &completedOutput); err != nil {
		t.Fatalf("decode completed output: %v", err)
	}
	if completedOutput["greeting"] != fixtureGreeting || completedOutput["message"] != "complete" {
		t.Fatalf("completed output = %v, want greeting/message object", completedOutput)
	}
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == "completed:complete" && oracle.VisibleText == "completed:complete" && !oracle.Pending
	}); err != nil {
		t.Fatalf("page-state oracle after completed invocation: %v", err)
	}
	return cancelTool, completedID, completed
}

func (r *adapterIntegrationRun) startPending(t *testing.T, ctx context.Context, session webmcp.TargetSession, cancelTool webmcp.ToolDescriptor) webmcp.InvocationID {
	t.Helper()
	pendingID, err := session.InvokeWebMCP(ctx, cancelTool.FrameID, cancelTool.Name, json.RawMessage(`{"message":"hold"}`))
	if err != nil {
		t.Fatalf("neutral invoke of pending imperative tool: %v", err)
	}
	if _, err := waitForIntegrationEvent(ctx, session.Events(), "toolInvoked for pending call", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == pendingID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == fixtureOraclePendingHold && oracle.VisibleText == fixtureOraclePendingHold && oracle.Pending
	}); err != nil {
		t.Fatalf("page-state oracle before cancellation: %v", err)
	}
	if err := session.CancelWebMCP(ctx, pendingID); err != nil {
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorInvocationCanceled || classified.Details["invocation_id"] != string(pendingID) || classified.Details["side_effect_unknown"] != true {
			t.Fatalf("neutral cancelInvocation(%s): %v", pendingID, err)
		}
	}
	return pendingID
}

func (r *adapterIntegrationRun) observeCancellation(t *testing.T, ctx, cancelObservationContext context.Context, session webmcp.TargetSession, pendingID webmcp.InvocationID) (webmcp.BrowserEvent, *webmcp.WebMCPWireTrace, fixtureOracle) {
	t.Helper()
	if _, err := waitForFixtureOracle(cancelObservationContext, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return slices.Contains(oracle.Invocations, "canceled:"+cancelToolName)
	}); err != nil {
		t.Fatalf("page cancellation event: %v", err)
	}
	canceled, err := waitForIntegrationEvent(cancelObservationContext, session.Events(), "canceled toolResponded", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.InvocationID == pendingID
	})
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != "Canceled" || canceled.ErrorCode != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("canceled response = %+v, want Canceled invocation semantics", canceled)
	}
	cancelTrace := r.assertCancelWireTrace(t, pendingID)
	pendingOracle, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == fixtureOraclePendingHold && oracle.VisibleText == fixtureOraclePendingHold
	})
	if err != nil {
		t.Fatalf("page-state oracle after cancellation: %v", err)
	}
	return canceled, cancelTrace, pendingOracle
}

func (r *adapterIntegrationRun) assertCancelWireTrace(t *testing.T, pendingID webmcp.InvocationID) *webmcp.WebMCPWireTrace {
	t.Helper()
	traces := r.wire.snapshot()
	var cancelTrace *webmcp.WebMCPWireTrace
	for index := range traces {
		trace := &traces[index]
		if trace.Method == webmcp.WebMCPCancelInvocationMethod && trace.InvocationID == pendingID {
			cancelTrace = trace
			break
		}
	}
	if cancelTrace == nil || cancelTrace.BrowserID != r.candidate.ID || cancelTrace.TargetID != r.selectedTarget.ID || cancelTrace.TargetSessionID == "" || cancelTrace.Phase != webmcp.WebMCPWirePhaseBeforeDispatch || !cancelTrace.ListenerReady {
		t.Fatalf("cancel wire trace = %+v, want exact ready target/session before dispatch", cancelTrace)
	}
	traceJSON, err := json.Marshal(cancelTrace)
	if err != nil {
		t.Fatalf("marshal cancel wire trace: %v", err)
	}
	for _, forbidden := range []string{"endpoint", "credential", "input", "output", "ws://", "https://"} {
		if bytes.Contains(traceJSON, []byte(forbidden)) {
			t.Fatalf("cancel wire trace contains forbidden %q: %s", forbidden, traceJSON)
		}
	}
	return cancelTrace
}

func (r *adapterIntegrationRun) verifyDetachAndReattach(t *testing.T, ctx context.Context, pendingOracle fixtureOracle) (inspectedPageState, inspectedPageState) {
	t.Helper()
	// This is deliberately a separate CDP client and a separate target
	// attachment. It verifies the actual visible DOM agrees with the independent
	// HTTP oracle after the adapter released the external target.
	afterDetach, err := inspectExternalTarget(ctx, r.browser.endpoint(), string(r.selectedTarget.ID))
	if err != nil {
		t.Fatalf("direct browser verification after adapter detach: %v", err)
	}
	assertPageStateMatchesOracle(t, afterDetach, pendingOracle)

	r.reattachFreshClient(t, ctx)
	if _, err := waitForFixtureTarget(ctx, r.baseURL, r.selectedTarget.ID, r.fixtureURL, true); err != nil {
		t.Fatalf("target after fresh neutral reattach/detach: %v", err)
	}
	afterReattach, err := inspectExternalTarget(ctx, r.browser.endpoint(), string(r.selectedTarget.ID))
	if err != nil {
		t.Fatalf("direct browser verification after fresh reattach: %v", err)
	}
	assertPageStateMatchesOracle(t, afterReattach, pendingOracle)
	return afterDetach, afterReattach
}

func (r *adapterIntegrationRun) reattachFreshClient(t *testing.T, ctx context.Context) {
	t.Helper()
	secondHandle, err := r.adapter.Open(ctx, r.candidate)
	if err != nil {
		t.Fatalf("fresh neutral client Open: %v", err)
	}
	secondSession, err := secondHandle.Attach(ctx, r.selectedTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		discardSecondaryError(secondHandle.Close)
		t.Fatalf("fresh neutral client reattach(%s): %v", r.selectedTarget.ID, err)
	}
	if secondSession.Context().Key.TargetID != r.selectedTarget.ID || !secondSession.Context().Connected {
		discardSecondaryError(secondSession.Close)
		discardSecondaryError(secondHandle.Close)
		t.Fatalf("fresh neutral session context = %+v, want connected exact target", secondSession.Context())
	}
	if err := secondSession.Close(); err != nil {
		discardSecondaryError(secondHandle.Close)
		t.Fatalf("fresh neutral client detach: %v", err)
	}
	if err := secondHandle.Close(); err != nil {
		t.Fatalf("fresh neutral client close: %v", err)
	}
}

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

func assertRegisteredTool(t *testing.T, tool webmcp.ToolDescriptor) {
	t.Helper()
	if tool.FrameID == "" || len(tool.InputSchema) == 0 || !json.Valid(tool.InputSchema) {
		t.Fatalf("registered tool = %+v, want frame and valid schema", tool)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("decode registered schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("registered schema type = %v, want object", schema["type"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["message"] == nil {
		t.Fatalf("registered schema properties = %v, want message property", schema["properties"])
	}
}

func assertDeclarativeTool(t *testing.T, tool webmcp.ToolDescriptor, wantAutoSubmit bool) {
	t.Helper()
	assertRegisteredTool(t, tool)
	if wantAutoSubmit {
		if tool.Annotations.AutoSubmit == nil || !*tool.Annotations.AutoSubmit {
			t.Fatalf("declarative annotations = %+v, want autosubmit=true", tool.Annotations)
		}
	} else if tool.Annotations.AutoSubmit != nil && *tool.Annotations.AutoSubmit {
		t.Fatalf("declarative annotations = %+v, want autosubmit=false or omitted", tool.Annotations)
	}
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
			return last, fmt.Errorf("wait for fixture oracle: %w (last=%+v err=%v)", ctx.Err(), last, lastErr)
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
		if err == nil {
			for _, target := range targets {
				if target.ID == string(targetID) && target.URL == fixtureURL {
					if wantPresent {
						return target, nil
					}
					lastErr = errors.New("target remains present")
				}
			}
			if !wantPresent {
				return devToolsTarget{}, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for target presence=%t: %w (last error: %v)", wantPresent, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
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
		detachContext, cancelDetach := context.WithTimeout(context.Background(), 5*time.Second)
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
