package chrome

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
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

type pinnedChrome struct {
	Lock       chromeForTestingLock
	Executable string
	WorkDir    string
}

type devToolsVersion struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type devToolsTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	Attached             bool   `json:"attached"`
}

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

type runningChrome struct {
	cmd           *exec.Cmd
	done          chan struct{}
	endpointValue string

	waitErr error

	closeOnce sync.Once
	closeErr  error
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
	if request.URL.Path == "/json/list" {
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
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func closeLiveCDPProxyConnection(writer http.ResponseWriter) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return
	}
	connection, _, err := hijacker.Hijack()
	if err == nil {
		_ = connection.Close()
	}
}

var devToolsEndpointPattern = regexp.MustCompile(`DevTools listening on (ws://127\.0\.0\.1:[0-9]+/devtools/browser/[^[:space:]]+)`)

func TestPinnedChromeWebMCPAdapterIntegration(t *testing.T) {
	// This check must remain the first observable operation: ordinary tests
	// neither read the O0 lock nor make a network request or start Chrome.
	if os.Getenv(chromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome integration proof", chromeIntegrationEnv)
	}

	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	workDir := t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newFixtureServer()
	t.Cleanup(func() { fixture.Close() })
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, ctx, fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if version.WebSocketDebuggerURL != browser.endpoint() {
		t.Fatalf("DevTools websocket = %q, launch announcement = %q", version.WebSocketDebuggerURL, browser.endpoint())
	}
	targetBeforeAdapter, err := waitForFixturePageTarget(ctx, browserHTTPURL(browser.endpoint()), fixtureURL)
	if err != nil {
		t.Fatalf("discover exact external fixture target before adapter attach: %v", err)
	}

	candidate := webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID("chrome-cft-" + lockedChromeVersion),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      version.Browser,
		Protocol:     version.ProtocolVersion,
		HTTPURL:      baseURL,
		BrowserWSURL: version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	wire := &wireTraceRecorder{}
	adapter := NewRuntime(WithEventBuffer(128), WithCommandTimeout(20*time.Second), WithWireTraceSink(wire))

	neutralVersion, err := adapter.Version(ctx, candidate)
	if err != nil {
		t.Fatalf("neutral BrowserRuntime.Version: %v", err)
	}
	if neutralVersion.Browser != version.Browser || neutralVersion.ProtocolVersion != version.ProtocolVersion {
		t.Fatalf("neutral version = %+v, want browser=%q protocol=%q", neutralVersion, version.Browser, version.ProtocolVersion)
	}
	handle, err := adapter.Open(ctx, candidate)
	if err != nil {
		t.Fatalf("neutral BrowserRuntime.Open: %v", err)
	}
	defer func() {
		if closeErr := handle.Close(); closeErr != nil {
			t.Errorf("adapter handle cleanup: %v", closeErr)
		}
	}()

	targets, err := handle.ListTargets(ctx)
	if err != nil {
		t.Fatalf("neutral BrowserHandle.ListTargets: %v", err)
	}
	selectedTarget, err := findFixtureTarget(targets, fixtureURL)
	if err != nil {
		t.Fatalf("find exact fixture target through neutral target list: %v", err)
	}
	if selectedTarget.ID != webmcp.TargetID(targetBeforeAdapter.ID) {
		t.Fatalf("neutral target selection ID = %q, pre-attach HTTP discovery ID = %q", selectedTarget.ID, targetBeforeAdapter.ID)
	}
	if !selectedTarget.Eligible || selectedTarget.ID == "" {
		t.Fatalf("fixture target = %+v, want eligible exact page target", selectedTarget)
	}

	session, err := handle.Attach(ctx, selectedTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("neutral BrowserHandle.Attach(%s): %v", selectedTarget.ID, err)
	}
	if session.Ownership() != webmcp.TargetOwnershipExternal {
		t.Fatalf("session ownership = %q, want external", session.Ownership())
	}
	if got := session.Context().Key.TargetID; got != selectedTarget.ID {
		t.Fatalf("attached target ID = %q, want exact %q", got, selectedTarget.ID)
	}
	initialOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "initial" && oracle.VisibleText == "initial"
	})
	if err != nil {
		t.Fatalf("initial independent page-state oracle: %v", err)
	}

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
	if completed.Status != "Completed" || !json.Valid(completed.Output) || completed.ErrorCode != "" {
		t.Fatalf("completed response = %+v, want Completed structured output", completed)
	}
	var completedOutput map[string]any
	if err := json.Unmarshal(completed.Output, &completedOutput); err != nil {
		t.Fatalf("decode completed output: %v", err)
	}
	if completedOutput["greeting"] != "hello" || completedOutput["message"] != "complete" {
		t.Fatalf("completed output = %v, want greeting/message object", completedOutput)
	}
	completedOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == "completed:complete" && oracle.VisibleText == "completed:complete" && !oracle.Pending
	})
	if err != nil {
		t.Fatalf("page-state oracle after completed invocation: %v", err)
	}

	pendingID, err := session.InvokeWebMCP(ctx, cancelTool.FrameID, cancelTool.Name, json.RawMessage(`{"message":"hold"}`))
	if err != nil {
		t.Fatalf("neutral invoke of pending imperative tool: %v", err)
	}
	if _, err := waitForIntegrationEvent(ctx, session.Events(), "toolInvoked for pending call", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == pendingID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == "pending:hold" && oracle.VisibleText == "pending:hold" && oracle.Pending
	}); err != nil {
		t.Fatalf("page-state oracle before cancellation: %v", err)
	}
	if err := session.CancelWebMCP(ctx, pendingID); err != nil {
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorInvocationCanceled || classified.Details["invocation_id"] != string(pendingID) || classified.Details["side_effect_unknown"] != true {
			t.Fatalf("neutral cancelInvocation(%s): %v", pendingID, err)
		}
	}
	cancelObservationContext, cancelObservation := context.WithTimeout(ctx, 10*time.Second)
	defer cancelObservation()
	if _, err := waitForFixtureOracle(cancelObservationContext, fixture.StateURL(), func(oracle fixtureOracle) bool {
		for _, invocation := range oracle.Invocations {
			if invocation == "canceled:"+cancelToolName {
				return true
			}
		}
		return false
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
	traces := wire.snapshot()
	var cancelTrace *webmcp.WebMCPWireTrace
	for index := range traces {
		trace := &traces[index]
		if trace.Method == webmcp.WebMCPCancelInvocationMethod && trace.InvocationID == pendingID {
			cancelTrace = trace
			break
		}
	}
	if cancelTrace == nil || cancelTrace.BrowserID != candidate.ID || cancelTrace.TargetID != selectedTarget.ID || cancelTrace.TargetSessionID == "" || cancelTrace.Phase != webmcp.WebMCPWirePhaseBeforeDispatch || !cancelTrace.ListenerReady {
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
	pendingOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == "pending:hold" && oracle.VisibleText == "pending:hold"
	})
	if err != nil {
		t.Fatalf("page-state oracle after cancellation: %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("detach external target session: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close adapter handle after external detach: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, selectedTarget.ID, fixtureURL, true); err != nil {
		t.Fatalf("target after adapter detach: %v", err)
	}

	// This is deliberately a separate CDP client and a separate target
	// attachment. It verifies the actual visible DOM agrees with the independent
	// HTTP oracle after the adapter released the external target.
	afterDetach, err := inspectExternalTarget(ctx, browser.endpoint(), string(selectedTarget.ID))
	if err != nil {
		t.Fatalf("direct browser verification after adapter detach: %v", err)
	}
	assertPageStateMatchesOracle(t, afterDetach, pendingOracle)

	secondHandle, err := adapter.Open(ctx, candidate)
	if err != nil {
		t.Fatalf("fresh neutral client Open: %v", err)
	}
	secondSession, err := secondHandle.Attach(ctx, selectedTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		_ = secondHandle.Close()
		t.Fatalf("fresh neutral client reattach(%s): %v", selectedTarget.ID, err)
	}
	if secondSession.Context().Key.TargetID != selectedTarget.ID || !secondSession.Context().Connected {
		_ = secondSession.Close()
		_ = secondHandle.Close()
		t.Fatalf("fresh neutral session context = %+v, want connected exact target", secondSession.Context())
	}
	if err := secondSession.Close(); err != nil {
		_ = secondHandle.Close()
		t.Fatalf("fresh neutral client detach: %v", err)
	}
	if err := secondHandle.Close(); err != nil {
		t.Fatalf("fresh neutral client close: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, selectedTarget.ID, fixtureURL, true); err != nil {
		t.Fatalf("target after fresh neutral reattach/detach: %v", err)
	}
	afterReattach, err := inspectExternalTarget(ctx, browser.endpoint(), string(selectedTarget.ID))
	if err != nil {
		t.Fatalf("direct browser verification after fresh reattach: %v", err)
	}
	assertPageStateMatchesOracle(t, afterReattach, pendingOracle)

	t.Logf("WEBMCP_WIRE_CANCEL_PASS chrome=%s revision=%s browser=%s target=%s target_session=%s method=%s invocation=%s phase=%s listener_ready=%t", lockedChromeVersion, lockedChromeRevision, cancelTrace.BrowserID, cancelTrace.TargetID, cancelTrace.TargetSessionID, cancelTrace.Method, cancelTrace.InvocationID, cancelTrace.Phase, cancelTrace.ListenerReady)
	t.Logf("WEBMCP_INTEGRATION_PASS chrome=%s revision=%s platform=%s target=%s listener_before_enable=true completed=%s/%s canceled=%s/%s state_after_detach=%q state_after_reattach=%q", lockedChromeVersion, lockedChromeRevision, lockedChromePlatform, selectedTarget.ID, completedID, completed.Status, pendingID, canceled.Status, afterDetach.VisibleText, afterReattach.VisibleText)
	_ = initialOracle
	_ = completedOracle
}

func acquirePinnedChrome(ctx context.Context, workDir string) (pinnedChrome, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return pinnedChrome{}, fmt.Errorf("locked artifact platform is %s (darwin/arm64), observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	root, err := repositoryRoot()
	if err != nil {
		return pinnedChrome{}, err
	}
	lockPath := filepath.Join(root, "scripts", "webmcp-o0", "chrome-for-testing.json")
	lock, err := LoadChromeForTestingLock(lockPath)
	if err != nil {
		return pinnedChrome{}, fmt.Errorf("read O0 Chrome lock: %w", err)
	}
	if err := validatePinnedChromeLock(lock); err != nil {
		return pinnedChrome{}, err
	}
	platform, err := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return pinnedChrome{}, err
	}
	acquirer := NewChromeForTestingAcquirer(ChromeForTestingOptions{})
	executable, err := acquirer.AcquirePinnedChrome(ctx, PinnedChromeRequest{
		Platform:      platform,
		RequiredMajor: MinimumManagedChromeMajor,
		LockPath:      lockPath,
		CacheDir:      workDir,
	})
	if err != nil {
		return pinnedChrome{}, err
	}
	return pinnedChrome{Lock: lock, Executable: executable.Path, WorkDir: workDir}, nil
}

func repositoryRoot() (string, error) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate integration test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..")), nil
}

func validatePinnedChromeLock(lock chromeForTestingLock) error {
	if lock.Channel != lockedChromeChannel || lock.Platform != lockedChromePlatform || lock.Version != lockedChromeVersion || lock.Revision != lockedChromeRevision || lock.ArchiveSHA256 != lockedChromeSHA256 {
		return fmt.Errorf("O0 Chrome lock is not the qualified %s/%s %s revision %s artifact", lockedChromeChannel, lockedChromePlatform, lockedChromeVersion, lockedChromeRevision)
	}
	if lock.ManifestRetrievedAt == "" || lock.ExecutableRelative == "" {
		return errors.New("O0 Chrome lock omits manifest retrieval or executable metadata")
	}
	if !strings.HasPrefix(lock.ManifestURL, "https://googlechromelabs.github.io/chrome-for-testing/") {
		return fmt.Errorf("O0 Chrome manifest URL is not official: %q", lock.ManifestURL)
	}
	if !strings.HasPrefix(lock.DownloadURL, "https://storage.googleapis.com/chrome-for-testing-public/") {
		return fmt.Errorf("O0 Chrome download URL is not official: %q", lock.DownloadURL)
	}
	return nil
}

func launchPinnedChrome(ctx context.Context, pinned pinnedChrome, fixtureURL string) (*runningChrome, error) {
	return launchPinnedChromeAtPort(ctx, pinned, fixtureURL, 0)
}

// launchPinnedChromeAtPort keeps the normal O0 launch shape when port is zero
// and permits the recovery suite to deliberately reuse the old browser's
// loopback port after that browser has exited. The caller supplies only a
// pinned executable and a temporary profile owned by this test package.
func launchPinnedChromeAtPort(ctx context.Context, pinned pinnedChrome, fixtureURL string, port int) (*runningChrome, error) {
	profileDir := filepath.Join(pinned.WorkDir, "profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create isolated Chrome profile: %w", err)
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("Chrome debugging port is invalid: %d", port)
	}
	args := pinnedChromeLaunchFlags(profileDir, fixtureURL, port)
	cmd := exec.Command(pinned.Executable, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Chrome stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Chrome stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Chrome: %w", err)
	}
	running := &runningChrome{cmd: cmd, done: make(chan struct{})}
	go func() {
		running.waitErr = cmd.Wait()
		close(running.done)
	}()
	endpoint := make(chan string, 1)
	var stdoutLog, stderrLog bytes.Buffer
	go scanChromeEndpoint(io.TeeReader(stdout, &stdoutLog), endpoint)
	go scanChromeEndpoint(io.TeeReader(stderr, &stderrLog), endpoint)
	select {
	case value := <-endpoint:
		running.setEndpoint(value)
		return running, nil
	case <-running.done:
		return nil, fmt.Errorf("Chrome exited before exposing DevTools: %v (stdout=%q stderr=%q)", running.waitErr, strings.TrimSpace(stdoutLog.String()), strings.TrimSpace(stderrLog.String()))
	case <-ctx.Done():
		_ = running.Close()
		return nil, fmt.Errorf("wait for Chrome DevTools endpoint: %w (stdout=%q stderr=%q)", ctx.Err(), strings.TrimSpace(stdoutLog.String()), strings.TrimSpace(stderrLog.String()))
	}
}

func pinnedChromeLaunchFlags(profileDir, fixtureURL string, port int) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-extensions",
		"--disable-sync",
		"--no-default-browser-check",
		"--no-first-run",
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"--enable-blink-features=DeclarativeWebmcp",
		"--enable-experimental-web-platform-features",
		"--user-data-dir=" + profileDir,
		fixtureURL,
	}
}

func scanChromeEndpoint(reader io.Reader, endpoints chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		match := devToolsEndpointPattern.FindStringSubmatch(scanner.Text())
		if len(match) != 2 {
			continue
		}
		select {
		case endpoints <- match[1]:
		default:
		}
		return
	}
}

func (p *runningChrome) endpoint() string {
	return p.endpointValue
}

func (p *runningChrome) setEndpoint(value string) {
	p.endpointValue = value
}

func (p *runningChrome) Close() error {
	p.closeOnce.Do(func() {
		select {
		case <-p.done:
			p.closeErr = p.waitErr
			return
		default:
		}
		if p.cmd.Process != nil {
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
				p.closeErr = err
			}
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.done
		}
		if p.closeErr == nil {
			p.closeErr = p.waitErr
		}
	})
	return p.closeErr
}

func (p *runningChrome) Kill() error {
	p.closeOnce.Do(func() {
		if p.cmd == nil || p.cmd.Process == nil {
			return
		}
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.closeErr = err
			return
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			p.closeErr = errors.New("Chrome did not exit after kill")
		}
	})
	return p.closeErr
}

func newFixtureServer() *fixtureServer {
	fixture := &fixtureServer{oracle: fixtureOracle{Value: "initial", VisibleText: "initial"}}
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
			_, _ = writer.Write(chromeAdapterFixtureHTML)
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
		_ = json.NewEncoder(writer).Encode(f.oracle)
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
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fixture status = %s, want 200", response.Status)
	}
	if response.Header.Get("Origin-Agent-Cluster") != "?1" || response.Header.Get("Permissions-Policy") != "tools=(self)" {
		t.Fatalf("fixture isolation headers = Origin-Agent-Cluster %q Permissions-Policy %q", response.Header.Get("Origin-Agent-Cluster"), response.Header.Get("Permissions-Policy"))
	}
}

func waitForDevToolsVersion(ctx context.Context, baseURL, expectedVersion string) (devToolsVersion, error) {
	var lastErr error
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		version, err := readDevToolsVersion(ctx, baseURL)
		if err == nil {
			if strings.Contains(version.Browser, expectedVersion) {
				return version, nil
			}
			lastErr = fmt.Errorf("browser identity = %q, want %s", version.Browser, expectedVersion)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsVersion{}, fmt.Errorf("wait for DevTools version: %w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func readDevToolsVersion(ctx context.Context, baseURL string) (devToolsVersion, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/json/version", nil)
	if err != nil {
		return devToolsVersion{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return devToolsVersion{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return devToolsVersion{}, fmt.Errorf("DevTools version HTTP status: %s", response.Status)
	}
	var version devToolsVersion
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&version); err != nil {
		return devToolsVersion{}, err
	}
	return version, nil
}

func browserHTTPURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "ws" {
		parsed.Scheme = "http"
	} else if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func findFixtureTarget(targets []webmcp.Target, fixtureURL string) (webmcp.Target, error) {
	var matches []webmcp.Target
	for _, target := range targets {
		if target.Type == "page" && target.URL == fixtureURL {
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
	defer response.Body.Close()
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

func waitForFixturePageTarget(ctx context.Context, baseURL, fixtureURL string) (devToolsTarget, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		targets, err := readDevToolsTargets(ctx, baseURL)
		if err == nil {
			var matches []devToolsTarget
			for _, target := range targets {
				if target.Type == "page" && target.URL == fixtureURL {
					matches = append(matches, target)
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			lastErr = fmt.Errorf("found %d page targets for fixture URL", len(matches))
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for pre-attach fixture target: %w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func readDevToolsTargets(ctx context.Context, baseURL string) ([]devToolsTarget, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DevTools target list HTTP status: %s", response.Status)
	}
	var targets []devToolsTarget
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
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
