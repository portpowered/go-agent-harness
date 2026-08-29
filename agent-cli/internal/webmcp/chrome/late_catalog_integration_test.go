package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	lateCatalogIntegrationEnv = "WEBMCP_CHROME_LATE_CATALOG"
	lateCatalogToolName       = "webmcp_late_registration"
	lateCatalogPath           = "/late"
	producerlessPath          = "/producerless"
	emptyCatalogPath          = "/empty"
)

// TestPinnedChromeLateCatalogReevaluation is the real-browser companion to
// the broker and adapter regressions. It is opt-in because it downloads the
// locked Chrome for Testing artifact and starts a fresh browser process.
func TestPinnedChromeLateCatalogReevaluation(t *testing.T) {
	// Keep the gate before lock-file access, network access, or browser startup.
	if os.Getenv(lateCatalogIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned late-catalog integration proof", lateCatalogIntegrationEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pinned, err := acquirePinnedChrome(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newLateCatalogFixture()
	t.Cleanup(func() {
		fixture.ReleaseLoading()
		fixture.Close()
	})
	lateURL := fixture.URL(lateCatalogPath)
	browser, err := launchPinnedChrome(ctx, pinned, lateURL)
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
	if err := fixture.WaitForLoadingRequest(ctx); err != nil {
		t.Fatalf("wait for fixture loading gate: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, lateURL); err != nil {
		t.Fatalf("discover late-registration target: %v", err)
	}

	candidate := webmcp.BrowserCandidate{
		ID:           "chrome-late-catalog",
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      version.Browser,
		Protocol:     version.ProtocolVersion,
		HTTPURL:      baseURL,
		BrowserWSURL: version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	targets, err := readDevToolsTargets(ctx, baseURL)
	if err != nil {
		t.Fatalf("read late-registration target list: %v", err)
	}
	rawLate, err := findRawFixtureTarget(targets, lateURL)
	if err != nil {
		t.Fatalf("find late-registration target: %v", err)
	}

	wire := &wireTraceRecorder{}
	adapter := NewRuntime(
		WithEventBuffer(128),
		WithCommandTimeout(15*time.Second),
		WithWireTraceSink(wire),
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            adapter,
		Discoverer:         pinnedCatalogDiscoverer{candidate: candidate},
		CatalogWait:        100 * time.Millisecond,
		LoadingCatalogWait: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = broker.Close() })

	selectContext, cancelSelect := context.WithTimeout(ctx, 30*time.Second)
	_, selectErr := broker.Select(selectContext, webmcp.TargetSelector{
		BrowserID: candidate.ID,
		TargetID:  webmcp.TargetID(rawLate.ID),
	})
	cancelSelect()
	if selectErr == nil {
		_ = broker.Close()
		t.Fatal("late registration selection succeeded before the fixture gate was released")
	}
	assertLateCatalogDeadline(t, selectErr, candidate.ID, webmcp.TargetID(rawLate.ID))

	selected, err := broker.Selected(ctx)
	if err != nil {
		_ = broker.Close()
		t.Fatalf("read selected page after catalog deadline: %v", err)
	}
	if selected.Key.BrowserID != candidate.ID || selected.Key.TargetID != webmcp.TargetID(rawLate.ID) || selected.Generation != 1 || !selected.Connected {
		_ = broker.Close()
		t.Fatalf("selected page after catalog deadline = %+v, want exact connected target generation one", selected)
	}

	fixture.ReleaseLoading()
	registered, err := fixture.WaitForOracle(ctx, func(oracle lateCatalogOracle) bool {
		return oracle.Registered && oracle.RegistrationError == ""
	})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("wait for late registration oracle: %v", err)
	}
	if registered.InvocationCount != 0 {
		_ = broker.Close()
		t.Fatalf("registration oracle before broker invocation = %+v, want zero invocations", registered)
	}

	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("list late catalog on original selection: %v", err)
	}
	if snapshot.Generation != 1 || snapshot.Context.Key != selected.Key || !snapshot.Context.Ready || !snapshot.Context.CatalogReady || len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != lateCatalogToolName {
		_ = broker.Close()
		t.Fatalf("late catalog snapshot = %+v, want one ready tool on the original target/generation", snapshot)
	}
	if snapshot.Tools[0].FrameID == "" || len(snapshot.Tools[0].InputSchema) == 0 {
		_ = broker.Close()
		t.Fatalf("late catalog tool = %+v, want frame and schema", snapshot.Tools[0])
	}

	invocation, err := broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: snapshot.Tools[0].Ref,
		Input:   json.RawMessage(`{"message":"late"}`),
	})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("invoke late catalog tool: %v", err)
	}
	terminal, err := broker.WaitInvocation(ctx, invocation.InvocationID)
	if err != nil {
		_ = broker.Close()
		t.Fatalf("wait for late catalog invocation: %v", err)
	}
	if terminal.State != webmcp.InvocationCompleted || terminal.BrowserInvocationID == "" {
		_ = broker.Close()
		t.Fatalf("late catalog terminal result = %+v, want completed result with browser correlation", terminal)
	}
	var output struct {
		OK              bool   `json:"ok"`
		Message         string `json:"message"`
		InvocationCount int    `json:"invocationCount"`
	}
	if err := json.Unmarshal(terminal.Output, &output); err != nil {
		_ = broker.Close()
		t.Fatalf("decode late catalog terminal output: %v", err)
	}
	if !output.OK || output.Message != "late" || output.InvocationCount != 1 {
		_ = broker.Close()
		t.Fatalf("late catalog terminal output = %+v, want one successful invocation", output)
	}
	invokedOracle, err := fixture.WaitForOracle(ctx, func(oracle lateCatalogOracle) bool {
		return oracle.Registered && oracle.InvocationCount >= 1
	})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("wait for invocation oracle: %v", err)
	}
	if invokedOracle.InvocationCount != 1 || invokedOracle.LastMessage != "late" {
		_ = broker.Close()
		t.Fatalf("invocation oracle = %+v, want exactly one late invocation", invokedOracle)
	}

	traces := wire.snapshot()
	if len(traces) != 2 {
		_ = broker.Close()
		t.Fatalf("late catalog wire traces = %+v, want exactly one enable and one invoke on the retained session", traces)
	}
	if traces[0].Method != webmcp.WebMCPEnableMethod || traces[1].Method != webmcp.WebMCPInvokeToolMethod || traces[0].TargetID != webmcp.TargetID(rawLate.ID) || traces[1].TargetID != webmcp.TargetID(rawLate.ID) || traces[0].TargetSessionID == "" || traces[0].TargetSessionID != traces[1].TargetSessionID || !traces[0].ListenerReady || !traces[1].ListenerReady {
		_ = broker.Close()
		t.Fatalf("late catalog wire traces = %+v, want enable/invoke on one listener-ready target session", traces)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("close late catalog broker: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, webmcp.TargetID(rawLate.ID), lateURL, true); err != nil {
		t.Fatalf("external late target after broker detach: %v", err)
	}

	producerlessRaw, err := openClassificationTarget(ctx, baseURL, fixture.URL(producerlessPath))
	if err != nil {
		t.Fatalf("open producerless negative-control target: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, fixture.URL(producerlessPath)); err != nil {
		t.Fatalf("discover producerless negative-control target: %v", err)
	}
	emptyRaw, err := openClassificationTarget(ctx, baseURL, fixture.URL(emptyCatalogPath))
	if err != nil {
		t.Fatalf("open empty-catalog negative-control target: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, fixture.URL(emptyCatalogPath)); err != nil {
		t.Fatalf("discover empty-catalog negative-control target: %v", err)
	}
	if err := fixture.WaitForPageReady(ctx, emptyCatalogPath); err != nil {
		t.Fatalf("wait for empty-catalog page load: %v", err)
	}

	runPinnedCatalogNegativeControls(t, ctx, adapter, candidate, baseURL, fixture, producerlessRaw, emptyRaw)
	t.Logf("WEBMCP_LATE_CATALOG_PASS chrome=%s revision=%s platform=%s target=%s generation=1 registered=true invocations=%d negative_controls=producerless_prompt,empty_ready", lockedChromeVersion, lockedChromeRevision, lockedChromePlatform, rawLate.ID, invokedOracle.InvocationCount)
}

func assertLateCatalogDeadline(t *testing.T, err error, browserID webmcp.BrowserID, targetID webmcp.TargetID) {
	t.Helper()
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("late selection error = %T %v, want classified catalog error", err, err)
	}
	if classified.Code != webmcp.ErrorBrowserProtocol || !classified.Retryable {
		t.Fatalf("late selection error = %+v, want retryable browser protocol error", classified)
	}
	if classified.Details["reason_code"] != "page_tools_unverified" || classified.Details["reason"] != "deadline_exceeded" {
		t.Fatalf("late selection details = %#v, want page_tools_unverified/deadline_exceeded", classified.Details)
	}
	if classified.Details["browser_id"] != string(browserID) || classified.Details["target_id"] != string(targetID) || classified.Details["generation"] != uint64(1) {
		t.Fatalf("late selection identity details = %#v, want exact browser/target/generation", classified.Details)
	}
}

func runPinnedCatalogNegativeControls(t *testing.T, ctx context.Context, adapter *Runtime, candidate webmcp.BrowserCandidate, baseURL string, fixture *lateCatalogFixture, producerless, empty devToolsTarget) {
	t.Helper()
	producerlessBroker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            adapter,
		Discoverer:         pinnedCatalogDiscoverer{candidate: candidate},
		CatalogWait:        150 * time.Millisecond,
		LoadingCatalogWait: 150 * time.Millisecond,
	})
	started := time.Now()
	producerlessPage, producerlessErr := producerlessBroker.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: webmcp.TargetID(producerless.ID)})
	elapsed := time.Since(started)
	if producerlessErr == nil {
		_ = producerlessBroker.Close()
		t.Fatalf("producerless negative control unexpectedly selected successfully: %+v", producerlessPage)
	}
	assertLateCatalogDeadline(t, producerlessErr, candidate.ID, webmcp.TargetID(producerless.ID))
	if elapsed >= time.Second {
		_ = producerlessBroker.Close()
		t.Fatalf("producerless negative-control selection took %s, want prompt completion under one second", elapsed)
	}
	producerlessPage, err := producerlessBroker.Selected(ctx)
	if err != nil {
		_ = producerlessBroker.Close()
		t.Fatalf("producerless selected page after prompt diagnostic: %v", err)
	}
	if !producerlessPage.Connected || producerlessPage.Ready || producerlessPage.CatalogReady || producerlessPage.Generation != 1 || producerlessPage.DocumentReadyState != webmcp.DocumentReadyStateComplete || producerlessPage.DocumentLoading || !producerlessPage.DocumentLoadingKnown {
		_ = producerlessBroker.Close()
		t.Fatalf("producerless selected page = %+v, want load-complete connected recoverable unready state", producerlessPage)
	}
	if err := producerlessBroker.Close(); err != nil {
		t.Fatalf("close producerless negative-control broker: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, webmcp.TargetID(producerless.ID), fixture.URL(producerlessPath), true); err != nil {
		t.Fatalf("producerless target after broker detach: %v", err)
	}

	emptyBroker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            adapter,
		Discoverer:         pinnedCatalogDiscoverer{candidate: candidate},
		CatalogWait:        150 * time.Millisecond,
		LoadingCatalogWait: 150 * time.Millisecond,
	})
	emptyPage, err := emptyBroker.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: webmcp.TargetID(empty.ID)})
	if err != nil {
		_ = emptyBroker.Close()
		t.Fatalf("explicit empty-catalog selection: %v", err)
	}
	if !emptyPage.Connected || !emptyPage.Ready || !emptyPage.CatalogReady || emptyPage.Generation != 1 {
		_ = emptyBroker.Close()
		t.Fatalf("empty-catalog selected page = %+v, want ready generation-one selection", emptyPage)
	}
	emptySnapshot, err := emptyBroker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		_ = emptyBroker.Close()
		t.Fatalf("explicit empty-catalog list: %v", err)
	}
	if emptySnapshot.Generation != 1 || len(emptySnapshot.Tools) != 0 || !emptySnapshot.Context.Ready || !emptySnapshot.Context.CatalogReady {
		_ = emptyBroker.Close()
		t.Fatalf("empty-catalog snapshot = %+v, want zero tools and explicit readiness", emptySnapshot)
	}
	if err := emptyBroker.Close(); err != nil {
		t.Fatalf("close empty-catalog negative-control broker: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, webmcp.TargetID(empty.ID), fixture.URL(emptyCatalogPath), true); err != nil {
		t.Fatalf("empty-catalog target after broker detach: %v", err)
	}
}

type pinnedCatalogDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d pinnedCatalogDiscoverer) Discover(_ context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if options.BrowserID != "" && options.BrowserID != d.candidate.ID {
		return nil, nil
	}
	return []webmcp.BrowserCandidate{d.candidate}, nil
}

type lateCatalogOracle struct {
	Registered        bool   `json:"registered"`
	InvocationCount   int    `json:"invocationCount"`
	LastMessage       string `json:"lastMessage"`
	LastOutput        string `json:"lastOutput"`
	RegistrationError string `json:"registrationError"`
}

type lateCatalogFixture struct {
	server *httptest.Server

	loadingRequested   chan struct{}
	loadingRequestOnce sync.Once
	releaseLoading     chan struct{}
	releaseLoadingOnce sync.Once
	pageReady          map[string]chan struct{}
	pageReadyOnce      map[string]*sync.Once

	mu            sync.Mutex
	oracle        lateCatalogOracle
	oracleChanges chan struct{}
}

func newLateCatalogFixture() *lateCatalogFixture {
	fixture := &lateCatalogFixture{
		loadingRequested: make(chan struct{}),
		releaseLoading:   make(chan struct{}),
		pageReady: map[string]chan struct{}{
			producerlessPath: make(chan struct{}),
			emptyCatalogPath: make(chan struct{}),
		},
		pageReadyOnce: map[string]*sync.Once{
			producerlessPath: new(sync.Once),
			emptyCatalogPath: new(sync.Once),
		},
		oracleChanges: make(chan struct{}),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *lateCatalogFixture) URL(path string) string {
	if f == nil || f.server == nil {
		return ""
	}
	return strings.TrimRight(f.server.URL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (f *lateCatalogFixture) Close() {
	if f == nil {
		return
	}
	if f.server != nil {
		f.server.Close()
	}
}

func (f *lateCatalogFixture) ReleaseLoading() {
	if f == nil {
		return
	}
	f.releaseLoadingOnce.Do(func() { close(f.releaseLoading) })
}

func (f *lateCatalogFixture) WaitForLoadingRequest(ctx context.Context) error {
	if f == nil {
		return errors.New("late catalog fixture is nil")
	}
	select {
	case <-f.loadingRequested:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *lateCatalogFixture) WaitForPageReady(ctx context.Context, path string) error {
	if f == nil {
		return errors.New("late catalog fixture is nil")
	}
	ready, ok := f.pageReady[path]
	if !ok {
		return fmt.Errorf("late catalog fixture has no page-ready signal for %q", path)
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *lateCatalogFixture) WaitForOracle(ctx context.Context, match func(lateCatalogOracle) bool) (lateCatalogOracle, error) {
	if f == nil {
		return lateCatalogOracle{}, errors.New("late catalog fixture is nil")
	}
	for {
		f.mu.Lock()
		oracle := f.oracle
		changes := f.oracleChanges
		f.mu.Unlock()
		if match == nil || match(oracle) {
			return oracle, nil
		}
		select {
		case <-changes:
		case <-ctx.Done():
			return oracle, ctx.Err()
		}
	}
}

func (f *lateCatalogFixture) handle(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case lateCatalogPath:
		f.writeHTML(writer, request, lateCatalogFixtureHTML)
	case producerlessPath:
		f.writeHTML(writer, request, producerlessFixtureHTML)
	case emptyCatalogPath:
		f.writeHTML(writer, request, emptyCatalogFixtureHTML)
	case "/__test/load-block":
		f.handleLoadingBlock(writer, request)
	case "/__test/ready":
		f.handlePageReady(writer, request)
	case "/__test/state":
		f.handleState(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (f *lateCatalogFixture) writeHTML(writer http.ResponseWriter, request *http.Request, page []byte) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Origin-Agent-Cluster", "?1")
	if request.URL.Path == producerlessPath {
		// Keep this page load-complete but without an effective WebMCP producer.
		// The adapter observes the permission policy before probing getTools().
		writer.Header().Set("Permissions-Policy", "tools=()")
	} else {
		writer.Header().Set("Permissions-Policy", "tools=(self)")
	}
	_, _ = writer.Write(page)
}

func (f *lateCatalogFixture) handleLoadingBlock(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	f.loadingRequestOnce.Do(func() { close(f.loadingRequested) })
	select {
	case <-f.releaseLoading:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/javascript")
		_, _ = writer.Write([]byte("// released by the deterministic test gate\n"))
	case <-request.Context().Done():
	}
}

func (f *lateCatalogFixture) handlePageReady(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := request.URL.Query().Get("path")
	once, ok := f.pageReadyOnce[path]
	if !ok {
		http.Error(writer, "unknown page", http.StatusNotFound)
		return
	}
	once.Do(func() { close(f.pageReady[path]) })
	writer.WriteHeader(http.StatusNoContent)
}

func (f *lateCatalogFixture) handleState(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		f.mu.Lock()
		oracle := f.oracle
		f.mu.Unlock()
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(oracle)
	case http.MethodPost:
		var oracle lateCatalogOracle
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&oracle); err != nil {
			http.Error(writer, "invalid oracle", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.oracle = oracle
		close(f.oracleChanges)
		f.oracleChanges = make(chan struct{})
		f.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

var lateCatalogFixtureHTML = []byte(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>WebMCP delayed registration fixture</title></head>
<body><main><h1>Delayed WebMCP registration</h1><p id="status">waiting for registration gate</p></main>
<script src="/__test/load-block"></script>
<script>
(async () => {
  const state = {
    registered: false,
    invocationCount: 0,
    lastMessage: "",
    lastOutput: "",
    registrationError: ""
  };
  window.__webmcpLateCatalog = state;
  const publish = () => fetch("/__test/state", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(state)
  }).catch(() => {});
  try {
    const modelContext = document.modelContext || navigator.modelContext;
    if (!modelContext || typeof modelContext.registerTool !== "function") {
      throw new Error("native WebMCP registration is unavailable");
    }
    await modelContext.registerTool({
      name: "webmcp_late_registration",
      title: "Delayed registration tool",
      description: "Returns a deterministic response and increments the fixture oracle exactly once per invocation.",
      inputSchema: {
        type: "object",
        properties: { message: { type: "string" } },
        required: ["message"],
        additionalProperties: false
      },
      execute: async (input) => {
        const message = String(input && input.message || "");
        state.invocationCount += 1;
        state.lastMessage = message;
        state.lastOutput = JSON.stringify({ ok: true, message, invocationCount: state.invocationCount });
        publish();
        return { ok: true, message, invocationCount: state.invocationCount };
      }
    });
    state.registered = true;
    document.querySelector("#status").textContent = "registered";
    publish();
  } catch (error) {
    state.registrationError = String(error);
    document.querySelector("#status").textContent = "registration error";
    publish();
  }
})();
</script></body></html>`)

var producerlessFixtureHTML = []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>WebMCP producerless fixture</title></head>
<body><p>no WebMCP producer</p></body></html>`)

var emptyCatalogFixtureHTML = []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>WebMCP empty catalog fixture</title></head>
<body><p>explicit empty catalog</p>
<script>
window.addEventListener("load", async () => {
  // Produce affirmative empty-catalog evidence through the public API: add a
  // temporary tool, then remove it before the page signals readiness. This
  // never invokes a page tool and leaves getTools() empty for the broker.
  try {
    const context = document.modelContext || navigator.modelContext;
    if (!context || typeof context.registerTool !== "function") throw new Error("native WebMCP registration is unavailable");
    const controller = new AbortController();
    await context.registerTool({
      name: "webmcp_empty_probe",
      title: "Temporary empty-catalog probe",
      description: "Temporary registration removed before the test observes the empty catalog.",
      inputSchema: { type: "object", properties: {}, additionalProperties: false },
      execute: async () => ({ ok: true })
    }, { signal: controller.signal });
    controller.abort();
  } catch (_) {}
  fetch("/__test/ready?path=/empty", { method: "POST" }).catch(() => {});
});
</script></body></html>`)

func findRawFixtureTarget(targets []devToolsTarget, fixtureURL string) (devToolsTarget, error) {
	var matches []devToolsTarget
	for _, target := range targets {
		if target.Type == "page" && target.URL == fixtureURL {
			matches = append(matches, target)
		}
	}
	if len(matches) != 1 {
		return devToolsTarget{}, fmt.Errorf("found %d page targets for fixture URL %q", len(matches), fixtureURL)
	}
	return matches[0], nil
}
