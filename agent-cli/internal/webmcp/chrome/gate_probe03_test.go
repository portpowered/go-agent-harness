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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	workDir := t.TempDir()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	binaryPath := filepath.Join(workDir, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build actual agent binary: %v", err)
	}

	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	fixture := newProbe03Fixture(t, probe03RandomToken(t))
	pageAURL := fixture.PageURL(probe03PageA)
	pageBURL := fixture.PageURL(probe03PageB)
	launchCtx, cancelLaunch := context.WithTimeout(ctx, 45*time.Second)
	browser, err := launchPinnedChrome(launchCtx, pinned, pageAURL)
	cancelLaunch()
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("Probe 03 Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, baseURL, pageAURL)
	if err != nil {
		t.Fatalf("discover Probe 03 page A target: %v", err)
	}
	if _, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageA), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-a" && len(state.Invocations) == 0
	}); err != nil {
		t.Fatalf("wait for Probe 03 page A readiness: %v", err)
	}

	profileDir := filepath.Join(workDir, "profile")
	explicitConfigDir := filepath.Join(workDir, "explicit-config")
	if err := writeProbe03Config(explicitConfigDir, "", fixture.Origin()); err != nil {
		t.Fatalf("write explicit Probe 03 config: %v", err)
	}
	cdpURL := baseURL + "/json/version?probe03_endpoint=" + fixture.Token() + "#probe03-fragment-" + fixture.Token()
	transcript := make([]string, 0, 16)
	transcript = append(transcript,
		"WEBMCP_PROBE_03_PASS",
		fmt.Sprintf("chrome channel=%s version=%s revision=%s platform=%s", lockedChromeChannel, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform),
		fmt.Sprintf("chrome_observed browser=%s protocol=%s", version.Browser, version.ProtocolVersion),
		"randomized_fixture page_a=redacted page_b=redacted tool_names=redacted messages=redacted",
		"flags --headless=new --disable-gpu --disable-component-update --disable-extensions --disable-features=DelayMediaSinkDiscovery --disable-sync --no-default-browser-check --no-first-run --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 --enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport profile=<temporary>",
		"fixture origin=loopback query_fragment=redacted",
	)

	// The first listing uses the original explicit --cdp-url shape. No
	// selection is persisted, so every child process must resolve the exact
	// browser and target it was given.
	explicitBrowsers := runProbe03Command(t, ctx, binaryPath, explicitConfigDir, "", "webmcp", "browsers", "--cdp-url", cdpURL, "--json")
	recordProbe03Command(&transcript, explicitBrowsers, cdpURL, fixture.Token(), profileDir, explicitConfigDir, "")
	assertProbe03SafeOutput(t, explicitBrowsers, cdpURL, fixture.Token())
	explicitBrowsersData := requireGateSuccessData[gateBrowsersData](t, explicitBrowsers)
	if len(explicitBrowsersData.Browsers) != 1 || explicitBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceExplicit) {
		t.Fatalf("explicit browsers = %+v, want one explicit candidate", explicitBrowsersData)
	}
	browserID := explicitBrowsersData.Browsers[0].ID
	if browserID == "" {
		t.Fatal("explicit browsers returned an empty browser ID")
	}

	explicitTabs := runProbe03Command(t, ctx, binaryPath, explicitConfigDir, "", "webmcp", "tabs", "--cdp-url", cdpURL, "--browser", browserID, "--eligible", "--json")
	recordProbe03Command(&transcript, explicitTabs, cdpURL, fixture.Token(), profileDir, explicitConfigDir, "")
	assertProbe03SafeOutput(t, explicitTabs, cdpURL, fixture.Token())
	explicitTabsData := requireGateSuccessData[gateTabsData](t, explicitTabs)
	pageATab := probe03FindTab(t, explicitTabsData, browserID, fixture.Origin(), pageAURL)
	publicTargetID := pageATab.TargetID

	pageATools := runProbe03Command(t, ctx, binaryPath, explicitConfigDir, "", "webmcp", "tools", "--cdp-url", cdpURL, "--browser", browserID, "--tab", publicTargetID, "--json")
	recordProbe03Command(&transcript, pageATools, cdpURL, fixture.Token(), profileDir, explicitConfigDir, "")
	assertProbe03SafeOutput(t, pageATools, cdpURL, fixture.Token())
	pageAToolsData := requireGateSuccessData[gateToolsData](t, pageATools)
	pageATool := probe03FindTool(t, pageAToolsData, fixture.ToolName(probe03PageA))
	if pageATool.Generation == 0 || !webmcp.IsValidToolRef(webmcp.ToolRef(pageATool.Ref)) {
		t.Fatalf("page A tool = %+v, want generation-bound reference", pageATool)
	}

	// A fresh config directory must be able to discover the same browser from
	// Chrome's DevToolsActivePort file without an explicit endpoint. This also
	// proves that the production factory, rather than a test-only injected
	// runtime, serves the no-explicit-endpoint path.
	activeConfigDir := filepath.Join(workDir, "active-config")
	if err := writeProbe03Config(activeConfigDir, profileDir, fixture.Origin()); err != nil {
		t.Fatalf("write active-port Probe 03 config: %v", err)
	}
	activeBrowsers := runProbe03Command(t, ctx, binaryPath, activeConfigDir, "", "webmcp", "browsers", "--json")
	recordProbe03Command(&transcript, activeBrowsers, cdpURL, fixture.Token(), profileDir, activeConfigDir, "")
	assertProbe03SafeOutput(t, activeBrowsers, cdpURL, fixture.Token())
	activeBrowsersData := requireGateSuccessData[gateBrowsersData](t, activeBrowsers)
	if len(activeBrowsersData.Browsers) != 1 || activeBrowsersData.Browsers[0].ID != browserID || activeBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceActivePort) {
		t.Fatalf("active-port browsers = %+v, want the same active_port browser %q", activeBrowsersData, browserID)
	}

	defaultHome := filepath.Join(workDir, "default-home")
	defaultConfigDir := filepath.Join(defaultHome, ".agent-cli")
	if err := writeProbe03Config(defaultConfigDir, profileDir, fixture.Origin()); err != nil {
		t.Fatalf("write default-home Probe 03 config: %v", err)
	}
	defaultBrowsers := runProbe03Command(t, ctx, binaryPath, "", defaultHome, "webmcp", "browsers", "--json")
	recordProbe03Command(&transcript, defaultBrowsers, cdpURL, fixture.Token(), profileDir, "", defaultHome)
	assertProbe03SafeOutput(t, defaultBrowsers, cdpURL, fixture.Token())
	defaultBrowsersData := requireGateSuccessData[gateBrowsersData](t, defaultBrowsers)
	if len(defaultBrowsersData.Browsers) != 1 || defaultBrowsersData.Browsers[0].ID != browserID || defaultBrowsersData.Browsers[0].Source != string(webmcp.DiscoverySourceActivePort) {
		t.Fatalf("default-home browsers = %+v, want the same active_port browser %q", defaultBrowsersData, browserID)
	}

	defaultTools := runProbe03Command(t, ctx, binaryPath, "", defaultHome, "webmcp", "tools", "--browser", browserID, "--tab", publicTargetID, "--json")
	recordProbe03Command(&transcript, defaultTools, cdpURL, fixture.Token(), profileDir, "", defaultHome)
	assertProbe03SafeOutput(t, defaultTools, cdpURL, fixture.Token())
	defaultToolsData := requireGateSuccessData[gateToolsData](t, defaultTools)
	defaultPageATool := probe03FindTool(t, defaultToolsData, fixture.ToolName(probe03PageA))
	if defaultPageATool.Ref != pageATool.Ref || defaultPageATool.Generation != pageATool.Generation {
		t.Fatalf("default-config page A tool = %+v, want explicit ref/generation %+v", defaultPageATool, pageATool)
	}

	// Keep one long-lived broker attached while the independent CDP observer
	// navigates the target. Its event envelope is the proof that the catalog
	// generation advanced inside the same selected page session.
	watchProcess, err := startProbe03Command(ctx, binaryPath, explicitConfigDir, "", "webmcp", "watch", "--cdp-url", cdpURL, "--browser", browserID, "--tab", publicTargetID, "--timeout", "12s", "--json")
	if err != nil {
		t.Fatalf("start Probe 03 generation watcher: %v", err)
	}
	watchAttachedCtx, cancelWatchAttached := context.WithTimeout(ctx, 30*time.Second)
	_, err = waitForFixtureTarget(watchAttachedCtx, baseURL, webmcp.TargetID(rawTarget.ID), pageAURL, true)
	cancelWatchAttached()
	if err != nil {
		watchProcess.cancel()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("wait for Probe 03 generation watcher target: %v", err)
	}
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		watchProcess.cancel()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("settle Probe 03 generation watcher: %v", ctx.Err())
	}
	if err := navigateProbe03Target(ctx, browser.endpoint(), rawTarget.ID, pageBURL); err != nil {
		watchProcess.cancel()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("navigate Probe 03 target from page A to page B: %v", err)
	}
	if _, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageB), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-b" && len(state.Invocations) == 0
	}); err != nil {
		watchProcess.cancel()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("wait for Probe 03 page B readiness: %v", err)
	}
	watchResult, err := watchProcess.wait(ctx)
	if err != nil {
		t.Fatalf("wait for Probe 03 generation watcher: %v", err)
	}
	recordProbe03Command(&transcript, watchResult, cdpURL, fixture.Token(), profileDir, explicitConfigDir, "")
	assertProbe03SafeOutput(t, watchResult, cdpURL, fixture.Token())
	watchData := requireGateSuccessData[gateWatchData](t, watchResult)
	if watchData.Status != "canceled" {
		t.Fatalf("Probe 03 watch = %+v, want bounded canceled status", watchData)
	}
	assertProbe03GenerationChanged(t, watchData, browserID, publicTargetID, pageATool.Generation)

	pageAAfterStale, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageA), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-a" && len(state.Invocations) == 0
	})
	if err != nil {
		t.Fatalf("page A oracle before stale invocation: %v", err)
	}
	pageBAfterNavigation, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageB), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-b" && len(state.Invocations) == 0
	})
	if err != nil {
		t.Fatalf("page B oracle before stale invocation: %v", err)
	}

	staleMessage := "old-" + fixture.Token()
	staleResult := runProbe03Command(t, ctx, binaryPath, explicitConfigDir, "", "webmcp", "invoke", "--cdp-url", cdpURL, "--browser", browserID, "--tab", publicTargetID, "--tool-ref", pageATool.Ref, "--input-json", probe03Input(staleMessage), "--json")
	recordProbe03Command(&transcript, staleResult, cdpURL, fixture.Token(), profileDir, explicitConfigDir, "")
	assertProbe03SafeOutput(t, staleResult, cdpURL, fixture.Token())
	staleEnvelope := requireProbe03Failure(t, staleResult, webmcp.ErrorStaleToolRef)
	if staleEnvelope.Error == nil || staleEnvelope.Error.Details["refresh_required"] != true {
		t.Fatalf("stale envelope = %+v, want refresh_required=true", staleEnvelope.Error)
	}
	if strings.Contains(staleResult.Stdout+staleResult.Stderr, staleMessage) {
		t.Fatalf("stale error exposed tool input %q", staleMessage)
	}
	pageAAfterStale, err = waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageA), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-a" && len(state.Invocations) == 0
	})
	if err != nil {
		t.Fatalf("page A oracle after stale invocation: %v", err)
	}
	pageBAfterNavigation, err = waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageB), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-b" && len(state.Invocations) == 0
	})
	if err != nil {
		t.Fatalf("page B oracle after stale invocation: %v", err)
	}

	pageBTools := runProbe03Command(t, ctx, binaryPath, "", defaultHome, "webmcp", "tools", "--browser", browserID, "--tab", publicTargetID, "--json")
	recordProbe03Command(&transcript, pageBTools, cdpURL, fixture.Token(), profileDir, "", defaultHome)
	assertProbe03SafeOutput(t, pageBTools, cdpURL, fixture.Token())
	pageBToolsData := requireGateSuccessData[gateToolsData](t, pageBTools)
	pageBTool := probe03FindTool(t, pageBToolsData, fixture.ToolName(probe03PageB))
	if pageBTool.Ref == pageATool.Ref || pageBTool.Generation == 0 {
		t.Fatalf("page B tool = %+v, want a fresh ref distinct from page A %q", pageBTool, pageATool.Ref)
	}

	freshMessage := "fresh-" + fixture.Token()
	freshResult := runProbe03Command(t, ctx, binaryPath, "", defaultHome, "webmcp", "invoke", "--browser", browserID, "--tab", publicTargetID, "--tool-ref", pageBTool.Ref, "--input-json", probe03Input(freshMessage), "--json")
	recordProbe03Command(&transcript, freshResult, cdpURL, fixture.Token(), profileDir, "", defaultHome)
	assertProbe03SafeOutput(t, freshResult, cdpURL, fixture.Token())
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

	pageBAfterFresh, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageB), func(state probe03PageState) bool {
		return state.Ready && state.Value == "completed:"+freshMessage && len(state.Invocations) == 1 && state.Invocations[0] == fixture.ToolName(probe03PageB)+":"+freshMessage
	})
	if err != nil {
		t.Fatalf("page B oracle after fresh invocation: %v", err)
	}
	pageAAfterFresh, err := waitForProbe03Oracle(ctx, fixture.StateURL(probe03PageA), func(state probe03PageState) bool {
		return state.Ready && state.Value == "initial-a" && len(state.Invocations) == 0
	})
	if err != nil {
		t.Fatalf("page A oracle after fresh invocation: %v", err)
	}
	pageBDirect, err := inspectProbe03Target(ctx, browser.endpoint(), rawTarget.ID)
	if err != nil {
		t.Fatalf("independent page B CDP oracle: %v", err)
	}
	if pageBDirect.Page != probe03PageB || pageBDirect.URL != pageBURL || pageBDirect.Value != pageBAfterFresh.Value || len(pageBDirect.Invocations) != 1 {
		t.Fatalf("independent page B state = %+v, oracle = %+v", pageBDirect, pageBAfterFresh)
	}

	transcript = append(transcript,
		fmt.Sprintf("oracle before_stale page_a={value:%q invocations:%d} page_b={value:%q invocations:%d}", pageAAfterStale.Value, len(pageAAfterStale.Invocations), pageBAfterNavigation.Value, len(pageBAfterNavigation.Invocations)),
		fmt.Sprintf("oracle after_fresh page_a={value:%q invocations:%d} page_b={value:%q invocations:%d exact_once=true}", pageAAfterFresh.Value, len(pageAAfterFresh.Invocations), strings.ReplaceAll(pageBAfterFresh.Value, fixture.Token(), "<fixture-token>"), len(pageBAfterFresh.Invocations)),
		fmt.Sprintf("independent_cdp_oracle page_b={url:%q value:%q visible:%q invocations:%d exact_once=true}", strings.ReplaceAll(pageBDirect.URL, fixture.Token(), "<fixture-token>"), strings.ReplaceAll(pageBDirect.Value, fixture.Token(), "<fixture-token>"), strings.ReplaceAll(pageBDirect.VisibleText, fixture.Token(), "<fixture-token>"), len(pageBDirect.Invocations)),
		"cleanup browser=owned profile=<temporary> fixture=owned target_cleanup=detach_only external_target_retained=true",
	)
	for _, line := range transcript {
		t.Log(line)
	}

	chromePID := 0
	if browser.cmd != nil && browser.cmd.Process != nil {
		chromePID = browser.cmd.Process.Pid
	}
	if closeErr := browser.Close(); closeErr != nil {
		t.Logf("Probe 03 Chrome process %d exited after exact-process cleanup: %v", chromePID, closeErr)
	}
	closed = true
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

func runProbe03Command(t *testing.T, parent context.Context, binaryPath, configDir, homeDir string, args ...string) gateCLIResult {
	t.Helper()
	commandContext, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	process, err := startProbe03Command(commandContext, binaryPath, configDir, homeDir, args...)
	if err != nil {
		return gateCLIResult{Args: append([]string(nil), args...), ExitCode: -1, Err: err}
	}
	result, waitErr := process.wait(commandContext)
	if waitErr != nil {
		result.Args = append([]string(nil), process.args...)
		result.ExitCode = -1
		result.Err = waitErr
	}
	return result
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

func probe03ChildEnvironment(homeDir string) []string {
	base := gateChildEnvironment()
	if homeDir == "" {
		return base
	}
	environment := make([]string, 0, len(base)+2)
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || key == "USERPROFILE" || key == "XDG_CONFIG_HOME" || strings.HasPrefix(key, "AGENT_") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "HOME="+homeDir, "USERPROFILE="+homeDir)
	return environment
}

func recordProbe03Command(transcript *[]string, result gateCLIResult, cdpURL, fixtureToken, profileDir, configDir, homeDir string) {
	if transcript == nil {
		return
	}
	args := strings.Join(result.Args, " ")
	args = strings.ReplaceAll(args, cdpURL, "<cdp-url?query-redacted>")
	args = strings.ReplaceAll(args, fixtureToken, "<fixture-token>")
	args = strings.ReplaceAll(args, profileDir, "<temporary-profile>")
	if configDir != "" {
		args = strings.ReplaceAll(args, configDir, "<fresh-config-dir>")
	}
	if homeDir != "" {
		args = strings.ReplaceAll(args, homeDir, "<temporary-home>")
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = "<empty>"
	}
	output = strings.ReplaceAll(output, cdpURL, "<cdp-url?query-redacted>")
	output = strings.ReplaceAll(output, fixtureToken, "<fixture-token>")
	output = strings.ReplaceAll(output, profileDir, "<temporary-profile>")
	*transcript = append(*transcript, fmt.Sprintf("$ agent %s exit=%d\n%s", args, result.ExitCode, output))
}

func assertProbe03SafeOutput(t *testing.T, result gateCLIResult, cdpURL, token string) {
	t.Helper()
	output := result.Stdout + "\n" + result.Stderr
	for _, secret := range []string{cdpURL, "probe03-fragment-" + token} {
		if strings.Contains(output, secret) {
			t.Fatalf("Probe 03 command %q exposed %q: stdout=%q stderr=%q", result.Args, secret, result.Stdout, result.Stderr)
		}
	}
	assertGateSafeOutput(t, result)
}

func requireProbe03Failure(t *testing.T, result gateCLIResult, wantCode webmcp.ErrorCode) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("Probe 03 failure command unexpectedly succeeded: args=%q exit=%d err=%v stdout=%q stderr=%q", result.Args, result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := decodeOneJSON([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode Probe 03 failure envelope: %v; output=%q", err, result.Stdout)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(wantCode) {
		t.Fatalf("Probe 03 failure envelope = %+v, want code %q", envelope, wantCode)
	}
	return envelope
}

func probe03FindTab(t *testing.T, data gateTabsData, browserID, origin, pageURL string) gateTab {
	t.Helper()
	var match *gateTab
	for index := range data.Tabs {
		candidate := data.Tabs[index]
		if candidate.BrowserID != browserID || candidate.Type != "page" || candidate.Origin != origin || !candidate.Eligible {
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
	defer response.Body.Close()
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
