package chrome

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	probe03PageA = "a"
	probe03PageB = "b"
)

// TestPinnedChromeWebMCPProbe03ThroughActualBinary is the end-to-end stale
// reference proof for the shipped CLI. It is opt-in because it downloads and
// launches the pinned Chrome for Testing artifact, while the ordinary package
// tests remain hermetic and offline.
func TestPinnedChromeWebMCPProbe03ThroughActualBinary(t *testing.T) {
	if os.Getenv(chromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the actual-binary Probe 03 proof", chromeIntegrationEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	run := launchProbe03(t, ctx)
	pageATool := run.discoverExplicitPageATool(t, ctx)
	run.verifyActivePortDiscovery(t, ctx, pageATool)
	run.watchNavigationToPageB(t, ctx, pageATool.Generation)
	pageAAfterStale, pageBAfterNavigation := run.invokeStaleReference(t, ctx, pageATool)
	pageAAfterFresh, pageBAfterFresh, pageBDirect := run.invokeFreshReference(t, ctx, pageATool)

	token := run.fixture.Token()
	run.transcript = append(run.transcript,
		fmt.Sprintf("oracle before_stale page_a={value:%q invocations:%d} page_b={value:%q invocations:%d}", pageAAfterStale.Value, len(pageAAfterStale.Invocations), pageBAfterNavigation.Value, len(pageBAfterNavigation.Invocations)),
		fmt.Sprintf("oracle after_fresh page_a={value:%q invocations:%d} page_b={value:%q invocations:%d exact_once=true}", pageAAfterFresh.Value, len(pageAAfterFresh.Invocations), strings.ReplaceAll(pageBAfterFresh.Value, token, "<fixture-token>"), len(pageBAfterFresh.Invocations)),
		fmt.Sprintf("independent_cdp_oracle page_b={url:%q value:%q visible:%q invocations:%d exact_once=true}", strings.ReplaceAll(pageBDirect.URL, token, "<fixture-token>"), strings.ReplaceAll(pageBDirect.Value, token, "<fixture-token>"), strings.ReplaceAll(pageBDirect.VisibleText, token, "<fixture-token>"), len(pageBDirect.Invocations)),
		"cleanup browser=owned profile=<temporary> fixture=owned target_cleanup=detach_only external_target_retained=true",
	)
	for _, line := range run.transcript {
		t.Log(line)
	}

	chromePID := 0
	if run.browser.cmd != nil && run.browser.cmd.Process != nil {
		chromePID = run.browser.cmd.Process.Pid
	}
	if closeErr := run.browser.Close(); closeErr != nil {
		t.Logf("Probe 03 Chrome process %d exited after exact-process cleanup: %v", chromePID, closeErr)
	}
	run.closed = true
}

// probe03Run is the actual binary, pinned browser, randomized fixture, and
// sanitized transcript shared by the Probe 03 phases.
type probe03Run struct {
	binaryPath        string
	fixture           *probe03Fixture
	pageAURL          string
	pageBURL          string
	browser           *runningChrome
	closed            bool
	baseURL           string
	rawTarget         devToolsTarget
	workDir           string
	profileDir        string
	explicitConfigDir string
	defaultHome       string
	cdpURL            string
	browserID         string
	publicTargetID    string
	transcript        []string
}

func launchProbe03(t *testing.T, ctx context.Context) *probe03Run {
	t.Helper()
	run := &probe03Run{workDir: t.TempDir()}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	run.binaryPath = filepath.Join(run.workDir, "agent")
	if err := buildGateBinary(ctx, root, run.binaryPath); err != nil {
		t.Fatalf("build actual agent binary: %v", err)
	}

	pinned, err := acquirePinnedChrome(ctx, run.workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	run.fixture = newProbe03Fixture(t, probe03RandomToken(t))
	run.pageAURL = run.fixture.PageURL(probe03PageA)
	run.pageBURL = run.fixture.PageURL(probe03PageB)
	launchCtx, cancelLaunch := context.WithTimeout(ctx, 45*time.Second)
	run.browser, err = launchPinnedChrome(launchCtx, pinned, run.pageAURL)
	cancelLaunch()
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if !run.closed {
			if closeErr := run.browser.Close(); closeErr != nil {
				t.Logf("Probe 03 Chrome cleanup: %v", closeErr)
			}
		}
	})

	run.baseURL = browserHTTPURL(run.browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, run.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	run.rawTarget, err = waitForFixturePageTarget(ctx, run.baseURL, run.pageAURL)
	if err != nil {
		t.Fatalf("discover Probe 03 page A target: %v", err)
	}
	if _, err := run.waitInitial(ctx, probe03PageA); err != nil {
		t.Fatalf("wait for Probe 03 page A readiness: %v", err)
	}
	run.configureExplicit(t, version)
	return run
}

func (r *probe03Run) configureExplicit(t *testing.T, version devToolsVersion) {
	t.Helper()
	r.profileDir = filepath.Join(r.workDir, "profile")
	r.explicitConfigDir = filepath.Join(r.workDir, "explicit-config")
	if err := writeProbe03Config(r.explicitConfigDir, "", r.fixture.Origin()); err != nil {
		t.Fatalf("write explicit Probe 03 config: %v", err)
	}
	r.cdpURL = r.baseURL + "/json/version?probe03_endpoint=" + r.fixture.Token() + "#probe03-fragment-" + r.fixture.Token()
	r.transcript = make([]string, 0, 16)
	r.transcript = append(r.transcript,
		"WEBMCP_PROBE_03_PASS",
		fmt.Sprintf("chrome channel=%s version=%s revision=%s platform=%s", lockedChromeChannel, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform),
		fmt.Sprintf("chrome_observed browser=%s protocol=%s", version.Browser, version.ProtocolVersion),
		"randomized_fixture page_a=redacted page_b=redacted tool_names=redacted messages=redacted",
		"flags --headless=new --disable-gpu --disable-component-update --disable-extensions --disable-features=DelayMediaSinkDiscovery --disable-sync --no-default-browser-check --no-first-run --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 --enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport profile=<temporary>",
		"fixture origin=loopback query_fragment=redacted",
	)
}

// command runs one CLI child, records its sanitized transcript line, and
// asserts that no endpoint or fixture secret leaked into its output.
func (r *probe03Run) command(t *testing.T, ctx context.Context, configDir, homeDir string, args ...string) gateCLIResult {
	t.Helper()
	result := runProbe03Command(t, ctx, r.binaryPath, configDir, homeDir, args...)
	recordProbe03Command(&r.transcript, result, r.cdpURL, r.fixture.Token(), r.profileDir, configDir, homeDir)
	assertProbe03SafeOutput(t, result, r.cdpURL, r.fixture.Token())
	return result
}

// waitInitial waits until a fixture page is ready and still untouched.
func (r *probe03Run) waitInitial(ctx context.Context, page string) (probe03PageState, error) {
	return waitForProbe03Oracle(ctx, r.fixture.StateURL(page), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-"+page && len(state.Invocations) == 0
	})
}

// discoverExplicitPageATool uses the original explicit --cdp-url shape. No
// selection is persisted, so every child process must resolve the exact
// browser and target it was given.
func (r *probe03Run) discoverExplicitPageATool(t *testing.T, ctx context.Context) gateTool {
	t.Helper()
	explicitBrowsers := r.command(t, ctx, r.explicitConfigDir, "", "webmcp", "browsers", "--cdp-url", r.cdpURL, "--json")
	explicitBrowsersData := requireGateSuccessData[gateBrowsersData](t, explicitBrowsers)
	if len(explicitBrowsersData.Browsers) != 1 || explicitBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceExplicit) {
		t.Fatalf("explicit browsers = %+v, want one explicit candidate", explicitBrowsersData)
	}
	r.browserID = explicitBrowsersData.Browsers[0].ID
	if r.browserID == "" {
		t.Fatal("explicit browsers returned an empty browser ID")
	}

	explicitTabs := r.command(t, ctx, r.explicitConfigDir, "", "webmcp", "tabs", "--cdp-url", r.cdpURL, "--browser", r.browserID, "--eligible", "--json")
	explicitTabsData := requireGateSuccessData[gateTabsData](t, explicitTabs)
	pageATab := probe03FindTab(t, explicitTabsData, r.browserID, r.fixture.Origin(), r.pageAURL)
	r.publicTargetID = pageATab.TargetID

	pageATools := r.command(t, ctx, r.explicitConfigDir, "", "webmcp", "tools", "--cdp-url", r.cdpURL, "--browser", r.browserID, "--tab", r.publicTargetID, "--json")
	pageAToolsData := requireGateSuccessData[gateToolsData](t, pageATools)
	pageATool := probe03FindTool(t, pageAToolsData, r.fixture.ToolName(probe03PageA))
	if pageATool.Generation == 0 || !webmcp.IsValidToolRef(webmcp.ToolRef(pageATool.Ref)) {
		t.Fatalf("page A tool = %+v, want generation-bound reference", pageATool)
	}
	return pageATool
}

// verifyActivePortDiscovery proves a fresh config directory discovers the same
// browser from Chrome's DevToolsActivePort file without an explicit endpoint,
// so the production factory, rather than a test-only injected runtime, serves
// the no-explicit-endpoint path.
func (r *probe03Run) verifyActivePortDiscovery(t *testing.T, ctx context.Context, pageATool gateTool) {
	t.Helper()
	activeConfigDir := filepath.Join(r.workDir, "active-config")
	if err := writeProbe03Config(activeConfigDir, r.profileDir, r.fixture.Origin()); err != nil {
		t.Fatalf("write active-port Probe 03 config: %v", err)
	}
	activeBrowsers := r.command(t, ctx, activeConfigDir, "", "webmcp", "browsers", "--json")
	activeBrowsersData := requireGateSuccessData[gateBrowsersData](t, activeBrowsers)
	if len(activeBrowsersData.Browsers) != 1 || activeBrowsersData.Browsers[0].ID != r.browserID || activeBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceActivePort) {
		t.Fatalf("active-port browsers = %+v, want the same active_port browser %q", activeBrowsersData, r.browserID)
	}

	r.defaultHome = filepath.Join(r.workDir, "default-home")
	defaultConfigDir := filepath.Join(r.defaultHome, ".agent-cli")
	if err := writeProbe03Config(defaultConfigDir, r.profileDir, r.fixture.Origin()); err != nil {
		t.Fatalf("write default-home Probe 03 config: %v", err)
	}
	defaultBrowsers := r.command(t, ctx, "", r.defaultHome, "webmcp", "browsers", "--json")
	defaultBrowsersData := requireGateSuccessData[gateBrowsersData](t, defaultBrowsers)
	if len(defaultBrowsersData.Browsers) != 1 || defaultBrowsersData.Browsers[0].ID != r.browserID || defaultBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceActivePort) {
		t.Fatalf("default-home browsers = %+v, want the same active_port browser %q", defaultBrowsersData, r.browserID)
	}

	defaultTools := r.command(t, ctx, "", r.defaultHome, "webmcp", "tools", "--browser", r.browserID, "--tab", r.publicTargetID, "--json")
	defaultToolsData := requireGateSuccessData[gateToolsData](t, defaultTools)
	defaultPageATool := probe03FindTool(t, defaultToolsData, r.fixture.ToolName(probe03PageA))
	if defaultPageATool.Ref != pageATool.Ref || defaultPageATool.Generation != pageATool.Generation {
		t.Fatalf("default-config page A tool = %+v, want explicit ref/generation %+v", defaultPageATool, pageATool)
	}
}

// watchNavigationToPageB keeps one long-lived broker attached while the
// independent CDP observer navigates the target. Its event envelope is the
// proof that the catalog generation advanced inside the same selected page
// session.
func (r *probe03Run) watchNavigationToPageB(t *testing.T, ctx context.Context, initialGeneration uint64) {
	t.Helper()
	watchProcess, err := startProbe03Command(ctx, r.binaryPath, r.explicitConfigDir, "", "webmcp", "watch", "--cdp-url", r.cdpURL, "--browser", r.browserID, "--tab", r.publicTargetID, "--timeout", "12s", "--json")
	if err != nil {
		t.Fatalf("start Probe 03 generation watcher: %v", err)
	}
	r.navigateWhileWatching(t, ctx, watchProcess)
	watchResult, err := watchProcess.wait(ctx)
	if err != nil {
		t.Fatalf("wait for Probe 03 generation watcher: %v", err)
	}
	recordProbe03Command(&r.transcript, watchResult, r.cdpURL, r.fixture.Token(), r.profileDir, r.explicitConfigDir, "")
	assertProbe03SafeOutput(t, watchResult, r.cdpURL, r.fixture.Token())
	watchData := requireGateSuccessData[gateWatchData](t, watchResult)
	if watchData.Status != invocationStatusCanceled {
		t.Fatalf("Probe 03 watch = %+v, want bounded canceled status", watchData)
	}
	assertProbe03GenerationChanged(t, watchData, r.browserID, r.publicTargetID, initialGeneration)
}

func (r *probe03Run) navigateWhileWatching(t *testing.T, ctx context.Context, watchProcess *gateCLIProcess) {
	t.Helper()
	abandon := func(format string, args ...any) {
		watchProcess.cancel()
		watchProcess.abandon(ctx)
		t.Fatalf(format, args...)
	}
	watchAttachedCtx, cancelWatchAttached := context.WithTimeout(ctx, 30*time.Second)
	_, err := waitForFixtureTarget(watchAttachedCtx, r.baseURL, webmcp.TargetID(r.rawTarget.ID), r.pageAURL, true)
	cancelWatchAttached()
	if err != nil {
		abandon("wait for Probe 03 generation watcher target: %v", err)
	}
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		abandon("settle Probe 03 generation watcher: %v", ctx.Err())
	}
	if err := navigateProbe03Target(ctx, r.browser.endpoint(), r.rawTarget.ID, r.pageBURL); err != nil {
		abandon("navigate Probe 03 target from page A to page B: %v", err)
	}
	if _, err := r.waitInitial(ctx, probe03PageB); err != nil {
		abandon("wait for Probe 03 page B readiness: %v", err)
	}
}

func (r *probe03Run) invokeStaleReference(t *testing.T, ctx context.Context, pageATool gateTool) (probe03PageState, probe03PageState) {
	t.Helper()
	if _, err := r.waitInitial(ctx, probe03PageA); err != nil {
		t.Fatalf("page A oracle before stale invocation: %v", err)
	}
	if _, err := r.waitInitial(ctx, probe03PageB); err != nil {
		t.Fatalf("page B oracle before stale invocation: %v", err)
	}

	staleMessage := "old-" + r.fixture.Token()
	staleResult := r.command(t, ctx, r.explicitConfigDir, "", "webmcp", "invoke", "--cdp-url", r.cdpURL, "--browser", r.browserID, "--tab", r.publicTargetID, "--tool-ref", pageATool.Ref, "--input-json", probe03Input(staleMessage), "--json")
	staleEnvelope := requireProbe03Failure(t, staleResult, webmcp.ErrorStaleToolRef)
	if staleEnvelope.Error == nil || staleEnvelope.Error.Details["refresh_required"] != true {
		t.Fatalf("stale envelope = %+v, want refresh_required=true", staleEnvelope.Error)
	}
	if strings.Contains(staleResult.Stdout+staleResult.Stderr, staleMessage) {
		t.Fatalf("stale error exposed tool input %q", staleMessage)
	}
	pageAAfterStale, err := r.waitInitial(ctx, probe03PageA)
	if err != nil {
		t.Fatalf("page A oracle after stale invocation: %v", err)
	}
	pageBAfterNavigation, err := r.waitInitial(ctx, probe03PageB)
	if err != nil {
		t.Fatalf("page B oracle after stale invocation: %v", err)
	}
	return pageAAfterStale, pageBAfterNavigation
}

func (r *probe03Run) invokeFreshReference(t *testing.T, ctx context.Context, pageATool gateTool) (probe03PageState, probe03PageState, probe03PageSnapshot) {
	t.Helper()
	pageBTools := r.command(t, ctx, "", r.defaultHome, "webmcp", "tools", "--browser", r.browserID, "--tab", r.publicTargetID, "--json")
	pageBToolsData := requireGateSuccessData[gateToolsData](t, pageBTools)
	pageBTool := probe03FindTool(t, pageBToolsData, r.fixture.ToolName(probe03PageB))
	if pageBTool.Ref == pageATool.Ref || pageBTool.Generation == 0 {
		t.Fatalf("page B tool = %+v, want a fresh ref distinct from page A %q", pageBTool, pageATool.Ref)
	}

	freshMessage := "fresh-" + r.fixture.Token()
	freshResult := r.command(t, ctx, "", r.defaultHome, "webmcp", "invoke", "--browser", r.browserID, "--tab", r.publicTargetID, "--tool-ref", pageBTool.Ref, "--input-json", probe03Input(freshMessage), "--json")
	freshData := requireGateSuccessData[gateInvocation](t, freshResult)
	if freshData.InvocationID == "" || freshData.ToolRef != pageBTool.Ref || freshData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("fresh invocation = %+v, want one completed invocation for page B", freshData)
	}
	var freshOutput map[string]any
	if err := json.Unmarshal(freshData.Output, &freshOutput); err != nil {
		t.Fatalf("decode fresh Probe 03 output: %v", err)
	}
	if freshOutput["page"] != probe03PageB || freshOutput["message"] != freshMessage {
		t.Fatalf("fresh output = %+v, want page=%q message=%q", freshOutput, probe03PageB, freshMessage)
	}

	pageBAfterFresh, err := waitForProbe03Oracle(ctx, r.fixture.StateURL(probe03PageB), func(state probe03PageState) bool {
		return state.Ready && state.Value == "completed:"+freshMessage && len(state.Invocations) == 1 && state.Invocations[0] == r.fixture.ToolName(probe03PageB)+":"+freshMessage
	})
	if err != nil {
		t.Fatalf("page B oracle after fresh invocation: %v", err)
	}
	pageAAfterFresh, err := r.waitInitial(ctx, probe03PageA)
	if err != nil {
		t.Fatalf("page A oracle after fresh invocation: %v", err)
	}
	pageBDirect, err := inspectProbe03Target(ctx, r.browser.endpoint(), r.rawTarget.ID)
	if err != nil {
		t.Fatalf("independent page B CDP oracle: %v", err)
	}
	if pageBDirect.Page != probe03PageB || pageBDirect.URL != r.pageBURL || pageBDirect.Value != pageBAfterFresh.Value || len(pageBDirect.Invocations) != 1 {
		t.Fatalf("independent page B state = %+v, oracle = %+v", pageBDirect, pageBAfterFresh)
	}
	return pageAAfterFresh, pageBAfterFresh, pageBDirect
}

type probe03Fixture struct {
	server *httptest.Server
	token  string
	origin string
	tools  map[string]string

	mu     sync.Mutex
	states map[string]probe03PageState
}

type probe03PageState struct {
	Page        string   `json:"page"`
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Invocations []string `json:"invocations"`
}

type probe03PageSnapshot struct {
	Page        string   `json:"page"`
	URL         string   `json:"url"`
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Invocations []string `json:"invocations"`
}

func newProbe03Fixture(t *testing.T, token string) *probe03Fixture {
	t.Helper()
	fixture := &probe03Fixture{
		token: token,
		tools: map[string]string{
			probe03PageA: "probe03_a_" + token,
			probe03PageB: "probe03_b_" + token,
		},
		states: map[string]probe03PageState{
			probe03PageA: {Page: probe03PageA, Value: "initial-a", VisibleText: "initial-a", Invocations: []string{}},
			probe03PageB: {Page: probe03PageB, Value: "initial-b", VisibleText: "initial-b", Invocations: []string{}},
		},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	fixture.origin = fixture.server.URL
	t.Cleanup(fixture.Close)
	return fixture
}

func (f *probe03Fixture) Token() string {
	if f == nil {
		return ""
	}
	return f.token
}

func (f *probe03Fixture) Origin() string {
	if f == nil {
		return ""
	}
	return f.origin
}

func (f *probe03Fixture) ToolName(page string) string {
	if f == nil {
		return ""
	}
	return f.tools[page]
}

func (f *probe03Fixture) PageURL(page string) string {
	return f.origin + "/page-" + page + "?fixture=" + f.token + "#fragment-" + f.token
}

func (f *probe03Fixture) StateURL(page string) string {
	return f.origin + "/__probe03/state/" + page
}

func (f *probe03Fixture) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func (f *probe03Fixture) handle(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/page-a":
		f.writePage(writer, probe03PageA)
	case "/page-b":
		f.writePage(writer, probe03PageB)
	case "/__probe03/state/a":
		f.handleState(writer, request, probe03PageA)
	case "/__probe03/state/b":
		f.handleState(writer, request, probe03PageB)
	default:
		http.NotFound(writer, request)
	}
}

func (f *probe03Fixture) writePage(writer http.ResponseWriter, page string) {
	if page != probe03PageA && page != probe03PageB {
		http.NotFound(writer, nil)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Origin-Agent-Cluster", "?1")
	writer.Header().Set("Permissions-Policy", "tools=(self)")
	_, _ = io.WriteString(writer, renderProbe03Page(page, f.ToolName(page), f.StateURL(page)))
}

func (f *probe03Fixture) handleState(writer http.ResponseWriter, request *http.Request, page string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		state := f.states[page]
		state.Invocations = append([]string(nil), state.Invocations...)
		_ = json.NewEncoder(writer).Encode(state)
	case http.MethodPost:
		var state probe03PageState
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&state); err != nil || state.Page != page {
			http.Error(writer, "invalid Probe 03 state", http.StatusBadRequest)
			return
		}
		state.Invocations = append([]string(nil), state.Invocations...)
		f.states[page] = state
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func renderProbe03Page(page, toolName, stateURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>WebMCP Probe 03 %s</title></head>
<body>
  <main>
    <h1>WebMCP Probe 03 page %s</h1>
    <p id="probe03-ready">starting</p>
    <p>State: <strong id="probe03-state">initial-%s</strong></p>
    <form id="probe03-tool" toolname="%s" tooltitle="Probe 03 page %s tool" tooldescription="Mutate the independent Probe 03 oracle." toolautosubmit>
      <label>Message <input name="message" type="text" value="" toolparamdescription="The randomized mutation message."></label>
    </form>
  </main>
  <script>
    (() => {
      const page = %s;
      const toolName = %s;
      const stateEndpoint = %s;
      const state = { page, ready: false, value: "initial-" + page, visibleText: "initial-" + page, invocations: [] };
      window.__webmcpProbe03 = state;
      const ready = document.querySelector("#probe03-ready");
      const visible = document.querySelector("#probe03-state");
      const render = () => { ready.textContent = state.ready ? "ready" : "starting"; visible.textContent = state.value; state.visibleText = visible.textContent || ""; };
      const publish = () => fetch(stateEndpoint, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(state) }).catch(() => {});
      document.querySelector("#probe03-tool").addEventListener("submit", (event) => {
        event.preventDefault();
        const message = String(new FormData(event.currentTarget).get("message") || "");
        state.invocations.push(toolName + ":" + message);
        state.value = "completed:" + message;
        render();
        publish();
        if (typeof event.respondWith === "function") {
          event.respondWith(Promise.resolve({ page, message, value: state.value }));
        }
      });
      state.ready = true;
      render();
      publish();
    })();
  </script>
</body>
</html>
`, page, page, page, toolName, page, strconv.Quote(page), strconv.Quote(toolName), strconv.Quote(stateURL))
}

func probe03RandomToken(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate randomized Probe 03 fixture token: %v", err)
	}
	return hex.EncodeToString(bytes)
}

func writeProbe03Config(configDir, userDataDir, origin string) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	connection := ""
	if userDataDir != "" {
		connection = fmt.Sprintf("    user_data_dir: %q\n", userDataDir)
	}
	contents := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
%s    allow_remote_cdp: false
  selection:
    auto_select: off
    activate_tab: false
    persist: false
  policy:
    allowed_origins:
      - %q
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 30s
`, connection, origin)
	return os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600)
}

func probe03Input(message string) string {
	encoded, _ := json.Marshal(map[string]string{"message": message})
	return string(encoded)
}

func startProbe03Command(parent context.Context, binaryPath, configDir, homeDir string, args ...string) (*gateCLIProcess, error) {
	if parent == nil {
		parent = context.Background()
	}
	commandContext, cancel := context.WithCancel(parent)
	fullArgs := append([]string(nil), args...)
	if configDir != "" {
		fullArgs = append([]string{"--config-dir", configDir}, fullArgs...)
	}
	command := exec.CommandContext(commandContext, binaryPath, fullArgs...)
	command.Dir, _ = repositoryRoot()
	command.Env = probe03ChildEnvironment(homeDir)
	process := &gateCLIProcess{args: fullArgs, cmd: command, done: make(chan gateCLIResult, 1), cancel: cancel}
	command.Stdout = &process.stdout
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		err := command.Wait()
		exitCode := 0
		if command.ProcessState != nil {
			exitCode = command.ProcessState.ExitCode()
		}
		process.done <- gateCLIResult{Args: append([]string(nil), process.args...), Stdout: process.stdout.String(), Stderr: process.stderr.String(), ExitCode: exitCode, Err: err}
	}()
	return process, nil
}

func probe03FindTab(t *testing.T, data gateTabsData, browserID, origin, pageURL string) gateTab {
	t.Helper()
	var match *gateTab
	for index := range data.Tabs {
		candidate := data.Tabs[index]
		if candidate.BrowserID != browserID || candidate.Type != pageTargetType || candidate.Origin != origin || !candidate.Eligible {
			continue
		}
		if match != nil {
			t.Fatalf("Probe 03 tabs = %+v, want one matching page %q", data.Tabs, pageURL)
		}
		selected := candidate
		match = &selected
	}
	if match == nil || match.TargetID == "" {
		t.Fatalf("Probe 03 tabs = %+v, want an eligible page for %q", data.Tabs, pageURL)
	}
	return *match
}

func probe03FindTool(t *testing.T, data gateToolsData, name string) gateTool {
	t.Helper()
	for _, tool := range data.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("Probe 03 tools = %+v, want %q", data.Tools, name)
	return gateTool{}
}

func assertProbe03GenerationChanged(t *testing.T, data gateWatchData, browserID, targetID string, initialGeneration uint64) {
	t.Helper()
	var changed bool
	for _, event := range data.Events {
		if event.BrowserID != browserID || event.TargetID != targetID {
			continue
		}
		if event.Type == "generation_changed" && event.Generation > initialGeneration {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("Probe 03 watch events = %+v, want generation_changed above %d", data.Events, initialGeneration)
	}
}

func waitForProbe03Oracle(ctx context.Context, endpoint string, match func(probe03PageState) bool) (probe03PageState, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last probe03PageState
	var lastErr error
	for {
		requestContext, cancel := context.WithTimeout(ctx, time.Second)
		state, err := readProbe03Oracle(requestContext, endpoint)
		cancel()
		if err == nil {
			last = state
			if match(state) {
				return state, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("wait for Probe 03 oracle: %w (last=%+v err=%v)", ctx.Err(), last, lastErr)
		case <-ticker.C:
		}
	}
}

func readProbe03Oracle(ctx context.Context, endpoint string) (probe03PageState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return probe03PageState{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return probe03PageState{}, err
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode != http.StatusOK {
		return probe03PageState{}, fmt.Errorf("Probe 03 oracle HTTP status: %s", response.Status)
	}
	var state probe03PageState
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&state); err != nil {
		return probe03PageState{}, err
	}
	return state, nil
}

func navigateProbe03Target(ctx context.Context, endpoint, targetID, destination string) (err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(target.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.Navigate(destination), chromedp.WaitReady("#probe03-ready")); err != nil {
		return fmt.Errorf("navigate Probe 03 target: %w", err)
	}
	return nil
}

func inspectProbe03Target(ctx context.Context, endpoint, targetID string) (state probe03PageSnapshot, err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(target.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("#probe03-ready")); err != nil {
		return probe03PageSnapshot{}, fmt.Errorf("attach Probe 03 page oracle: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(`(() => {
  const state = window.__webmcpProbe03;
  const visible = document.querySelector("#probe03-state");
  return {
    page: state && state.page !== undefined ? String(state.page) : "",
    url: location.href,
    ready: Boolean(state && state.ready),
    value: state && state.value !== undefined ? String(state.value) : "",
    visibleText: visible ? String(visible.textContent || "") : "",
    invocations: state && Array.isArray(state.invocations) ? state.invocations.map((value) => String(value)) : []
  };
})()`, &state)); err != nil {
		return probe03PageSnapshot{}, fmt.Errorf("read Probe 03 page oracle: %w", err)
	}
	return state, nil
}
