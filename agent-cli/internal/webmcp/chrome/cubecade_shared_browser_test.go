package chrome

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
)

const (
	cubecadeSharedBrowserIntegrationEnv = "WEBMCP_CUBECADE_SHARED_BROWSER_INTEGRATION"
	cubecadeSharedBrowserTestTimeout    = 8 * time.Minute
	cubecadeSharedBrowserQueueTool      = "queue_cube_moves"
	cubecadeSharedBrowserStateTool      = "get_cube_state"
)

// The fixture is served by a test-owned HTTP server so this proof never needs
// an external page, credentials, or an LLM provider. It exercises the same
// pinned Chrome/WebMCP path as the real Cubecade page while keeping the cube
// state deterministic and directly inspectable.
//
//go:embed testdata/cubecade_shared_browser.html
var cubecadeSharedBrowserFixtureHTML []byte

// TestPinnedChromeCubecadeTwoIndependentBrokerSessions is the credit-free
// room-shaped browser proof. It deliberately remains opt-in because it
// downloads the repository-pinned Chrome artifact and starts a real browser.
// Both brokers attach to the same externally owned target through separate
// production runtimes; no provider session or API key is involved.
func TestPinnedChromeCubecadeTwoIndependentBrokerSessions(t *testing.T) {
	// Keep the gate before lock-file access, network access, or browser startup.
	if os.Getenv(cubecadeSharedBrowserIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the credit-free two-broker Cubecade proof", cubecadeSharedBrowserIntegrationEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for %s, observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cubecadeSharedBrowserTestTimeout)
	defer cancel()

	pinned, err := acquirePinnedChrome(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	fixture := newCubecadeSharedBrowserFixture()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, ctx, fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	closedBrowser := false
	t.Cleanup(func() {
		if !closedBrowser {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("Cubecade shared-browser Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, baseURL, fixtureURL)
	if err != nil {
		t.Fatalf("discover exact Cubecade target: %v", err)
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
	targetID := webmcp.TargetID(rawTarget.ID)

	wireA := &wireTraceRecorder{}
	wireB := &wireTraceRecorder{}
	brokerA := newCubecadeSharedBrowserBroker(candidate, wireA)
	brokerB := newCubecadeSharedBrowserBroker(candidate, wireB)
	closedA := false
	closedB := false
	t.Cleanup(func() {
		if !closedB {
			if closeErr := brokerB.Close(); closeErr != nil {
				t.Logf("participant B broker cleanup: %v", closeErr)
			}
		}
		if !closedA {
			if closeErr := brokerA.Close(); closeErr != nil {
				t.Logf("participant A broker cleanup: %v", closeErr)
			}
		}
	})

	selectedA, err := brokerA.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: targetID})
	if err != nil {
		t.Fatalf("participant A select shared Cubecade target: %v", err)
	}
	selectedB, err := brokerB.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: targetID})
	if err != nil {
		t.Fatalf("participant B select shared Cubecade target: %v", err)
	}
	assertSharedBrowserSelection(t, "A", selectedA, candidate.ID, targetID)
	assertSharedBrowserSelection(t, "B", selectedB, candidate.ID, targetID)

	catalogA, err := brokerA.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("participant A list Cubecade tools: %v", err)
	}
	catalogB, err := brokerB.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("participant B list Cubecade tools: %v", err)
	}
	queueA := requireSharedBrowserTool(t, "A", catalogA, cubecadeSharedBrowserQueueTool)
	stateA := requireSharedBrowserTool(t, "A", catalogA, cubecadeSharedBrowserStateTool)
	queueB := requireSharedBrowserTool(t, "B", catalogB, cubecadeSharedBrowserQueueTool)
	stateB := requireSharedBrowserTool(t, "B", catalogB, cubecadeSharedBrowserStateTool)
	if queueA.Ref == queueB.Ref || stateA.Ref == stateB.Ref {
		t.Fatalf("participant page refs are not isolated: A=(%q,%q) B=(%q,%q)", queueA.Ref, stateA.Ref, queueB.Ref, stateB.Ref)
	}
	assertSharedBrowserFirstClassTools(t, ctx, brokerA, "A")
	assertSharedBrowserFirstClassTools(t, ctx, brokerB, "B")

	queueInvocation, queueRecord, queueTerminal, err := invokeSharedBrowserTool(ctx, brokerA, queueA, "participant-a", "response-a-queue", `{"moves":["R","U","F","L'","D","B2"]}`)
	if err != nil {
		t.Fatalf("participant A queue cube moves: %v", err)
	}
	assertSharedBrowserTerminal(t, "A queue_cube_moves", queueRecord, queueTerminal, candidate.ID, targetID, "participant-a")
	if queueTerminal.State != webmcp.InvocationCompleted || queueTerminal.BrowserInvocationID == "" {
		t.Fatalf("participant A queue terminal = %+v, want completed browser-backed receipt", queueTerminal)
	}
	var queueResult cubecadeSharedBrowserState
	if err := json.Unmarshal(queueTerminal.Output, &queueResult); err != nil {
		t.Fatalf("decode participant A queue result: %v", err)
	}
	if !queueResult.Accepted || queueResult.QueueDepth != 0 || queueResult.MoveCount != 6 || queueResult.Solved {
		t.Fatalf("participant A queue result = %+v, want accepted six-move unsolved state", queueResult)
	}
	queueCompletedAt := time.Now()

	type readResult struct {
		label      string
		invocation webmcp.InvocationID
		record     webmcp.Invocation
		terminal   webmcp.InvokeResult
		state      cubecadeSharedBrowserState
		err        error
	}
	readResults := make(chan readResult, 2)
	go func() {
		invocation, record, terminal, invokeErr := invokeSharedBrowserTool(ctx, brokerA, stateA, "participant-a", "response-a-read", `{}`)
		result := readResult{label: "A", invocation: invocation, record: record, terminal: terminal, err: invokeErr}
		if invokeErr == nil {
			result.err = json.Unmarshal(terminal.Output, &result.state)
		}
		readResults <- result
	}()
	go func() {
		invocation, record, terminal, invokeErr := invokeSharedBrowserTool(ctx, brokerB, stateB, "participant-b", "response-b-read", `{}`)
		result := readResult{label: "B", invocation: invocation, record: record, terminal: terminal, err: invokeErr}
		if invokeErr == nil {
			result.err = json.Unmarshal(terminal.Output, &result.state)
		}
		readResults <- result
	}()

	reads := make([]readResult, 0, 2)
	for range 2 {
		result := <-readResults
		if result.err != nil {
			t.Fatalf("participant %s get_cube_state: %v", result.label, result.err)
		}
		assertSharedBrowserTerminal(t, result.label+" get_cube_state", result.record, result.terminal, candidate.ID, targetID, result.record.SessionID)
		if result.terminal.State != webmcp.InvocationCompleted || result.terminal.BrowserInvocationID == "" {
			t.Fatalf("participant %s state terminal = %+v, want completed browser-backed receipt", result.label, result.terminal)
		}
		if !result.state.Accepted || result.state.QueueDepth != 0 || result.state.MoveCount != 6 || result.state.Solved || !equalStringSlices(result.state.LastMoves, []string{"R", "U", "F", "L'", "D", "B2"}) {
			t.Fatalf("participant %s state = %+v, want the completed six-move queue", result.label, result.state)
		}
		reads = append(reads, result)
	}
	if len(reads) != 2 || reads[0].terminal.InvocationID == reads[1].terminal.InvocationID || reads[0].terminal.BrowserInvocationID == reads[1].terminal.BrowserInvocationID {
		t.Fatalf("participant read receipts = %+v, want distinct per-session public and browser IDs", reads)
	}
	for _, result := range reads {
		if result.record.CreatedAt.Before(queueCompletedAt) {
			t.Fatalf("participant %s read receipt was admitted before queue terminal: read=%s queue_completed=%s", result.label, result.record.CreatedAt, queueCompletedAt)
		}
	}

	refreshedA, err := brokerA.ListTools(ctx, webmcp.ListToolsOptions{Refresh: true, IncludeSchemas: true})
	if err != nil {
		t.Fatalf("participant A refresh Cubecade catalog: %v", err)
	}
	if refreshedA.Context.Key != selectedA.Key || refreshedA.Generation != catalogA.Generation {
		t.Fatalf("participant A refresh context = %+v, want original target/generation %+v/%d", refreshedA.Context, selectedA, catalogA.Generation)
	}
	selectedBAfterRefresh, err := brokerB.Selected(ctx)
	if err != nil {
		t.Fatalf("participant B selected context after A refresh: %v", err)
	}
	if selectedBAfterRefresh.Key != selectedB.Key || selectedBAfterRefresh.Generation != selectedB.Generation || !selectedBAfterRefresh.Connected {
		t.Fatalf("participant B context after A refresh = %+v, want unchanged connected selection %+v", selectedBAfterRefresh, selectedB)
	}
	refreshedB, err := brokerB.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("participant B list after A refresh: %v", err)
	}
	if refreshedB.Context.Key != selectedB.Key || len(refreshedB.Tools) != len(catalogB.Tools) || !hasSharedBrowserTool(refreshedB.Tools, cubecadeSharedBrowserQueueTool) || !hasSharedBrowserTool(refreshedB.Tools, cubecadeSharedBrowserStateTool) {
		t.Fatalf("participant B catalog after A refresh = %+v, want unchanged shared-page surface", refreshedB)
	}

	if err := brokerA.Close(); err != nil {
		t.Fatalf("close participant A broker: %v", err)
	}
	closedA = true
	selectedBAfterClose, err := brokerB.Selected(ctx)
	if err != nil {
		t.Fatalf("participant B selected context after A close: %v", err)
	}
	if selectedBAfterClose.Key != selectedB.Key || !selectedBAfterClose.Connected {
		t.Fatalf("participant B context after A close = %+v, want connected original target", selectedBAfterClose)
	}
	postCloseInvocation, postCloseRecord, postCloseTerminal, err := invokeSharedBrowserTool(ctx, brokerB, stateB, "participant-b", "response-b-after-close", `{}`)
	if err != nil {
		t.Fatalf("participant B read after A close: %v", err)
	}
	if postCloseInvocation == "" {
		t.Fatal("participant B read after A close returned an empty receipt")
	}
	assertSharedBrowserTerminal(t, "B get_cube_state after A close", postCloseRecord, postCloseTerminal, candidate.ID, targetID, "participant-b")
	if postCloseTerminal.State != webmcp.InvocationCompleted || postCloseTerminal.BrowserInvocationID == "" {
		t.Fatalf("participant B post-close terminal = %+v, want completed browser-backed receipt", postCloseTerminal)
	}
	var postCloseState cubecadeSharedBrowserState
	if err := json.Unmarshal(postCloseTerminal.Output, &postCloseState); err != nil {
		t.Fatalf("decode participant B post-close state: %v", err)
	}
	if postCloseState.MoveCount != 6 || postCloseState.QueueDepth != 0 || postCloseState.Solved {
		t.Fatalf("participant B post-close state = %+v, want completed six-move state", postCloseState)
	}

	directState, err := inspectCubecadeSharedBrowserTarget(ctx, browser.endpoint(), rawTarget.ID)
	if err != nil {
		t.Fatalf("inspect direct Cubecade DOM state after participant A close: %v", err)
	}
	if directState.URL != fixtureURL || directState.MoveCount != 6 || directState.QueueDepth != 0 || directState.Solved || directState.VisibleText == "" {
		t.Fatalf("direct Cubecade DOM state = %+v, want same completed six-move page state", directState)
	}

	if err := brokerB.Close(); err != nil {
		t.Fatalf("close participant B broker: %v", err)
	}
	closedB = true
	if _, err := waitForFixtureTarget(ctx, baseURL, targetID, fixtureURL, true); err != nil {
		t.Fatalf("shared Cubecade target after both participant detach: %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Logf("close test-owned Chrome returned: %v", err)
	}
	closedBrowser = true

	assertSharedBrowserWireTraces(t, wireA.snapshot(), "A", candidate.ID, targetID)
	assertSharedBrowserWireTraces(t, wireB.snapshot(), "B", candidate.ID, targetID)
	aSessionID := sharedBrowserWireSessionID(wireA.snapshot())
	bSessionID := sharedBrowserWireSessionID(wireB.snapshot())
	if aSessionID == "" || bSessionID == "" || aSessionID == bSessionID {
		t.Fatalf("target session identities = A:%q B:%q, want distinct attached sessions", aSessionID, bSessionID)
	}

	t.Logf("WEBMCP_CUBECADE_SHARED_PASS chrome=%s revision=%s browser=%s target=%s participant_a_queue_receipt=%s participant_a_queue_status=%s participant_a_read_receipt=%s participant_b_read_receipt=%s participant_b_post_close_receipt=%s state_moves=%d queue_depth=%d solved=%t session_ids_distinct=true external_target_survived=true provider=false credentials=false", pinned.Lock.Version, pinned.Lock.Revision, candidate.ID, targetID, queueInvocation, queueTerminal.State, reads[0].terminal.InvocationID, reads[1].terminal.InvocationID, postCloseTerminal.InvocationID, directState.MoveCount, directState.QueueDepth, directState.Solved)
}

func newCubecadeSharedBrowserBroker(candidate webmcp.BrowserCandidate, wire *wireTraceRecorder) *webmcp.StatefulBroker {
	return webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            NewRuntime(WithEventBuffer(256), WithCommandTimeout(15*time.Second), WithWireTraceSink(wire)),
		Discoverer:         pinnedCatalogDiscoverer{candidate: candidate},
		CatalogWait:        10 * time.Second,
		LoadingCatalogWait: 10 * time.Second,
		InvocationTimeout:  20 * time.Second,
	})
}

func assertSharedBrowserSelection(t *testing.T, participant string, selected webmcp.PageContext, browserID webmcp.BrowserID, targetID webmcp.TargetID) {
	t.Helper()
	if selected.Key.BrowserID != browserID || selected.Key.TargetID != targetID || !selected.Connected || !selected.Ready || !selected.CatalogReady {
		t.Fatalf("participant %s selection = %+v, want connected ready browser=%s target=%s", participant, selected, browserID, targetID)
	}
}

func assertSharedBrowserFirstClassTools(t *testing.T, ctx context.Context, broker webmcp.Broker, participant string) {
	t.Helper()
	set := webmcpTools.NewBrokerToolSet(broker)
	if len(set.Definitions()) == 0 {
		t.Fatalf("participant %s stable WebMCP definitions are empty", participant)
	}
	definitions, err := set.PageToolDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("participant %s first-class page definitions: %v", participant, err)
	}
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		seen[definition.Name] = true
	}
	if !seen[cubecadeSharedBrowserQueueTool] || !seen[cubecadeSharedBrowserStateTool] {
		t.Fatalf("participant %s first-class page definitions = %+v, want queue_cube_moves/get_cube_state", participant, definitions)
	}
}

func requireSharedBrowserTool(t *testing.T, participant string, catalog webmcp.ToolCatalogSnapshot, name string) webmcp.ToolDescriptor {
	t.Helper()
	for _, tool := range catalog.Tools {
		if tool.Name == name {
			if tool.Ref == "" || tool.BrowserID == "" || tool.TargetID == "" || tool.FrameID == "" || tool.Generation != catalog.Generation {
				t.Fatalf("participant %s %s descriptor = %+v, want complete current-generation descriptor", participant, name, tool)
			}
			return tool
		}
	}
	t.Fatalf("participant %s catalog = %+v, missing %s", participant, catalog, name)
	return webmcp.ToolDescriptor{}
}

func hasSharedBrowserTool(tools []webmcp.ToolDescriptor, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func invokeSharedBrowserTool(ctx context.Context, broker *webmcp.StatefulBroker, tool webmcp.ToolDescriptor, sessionID, responseID, input string) (webmcp.InvocationID, webmcp.Invocation, webmcp.InvokeResult, error) {
	admitted, err := broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef:     tool.Ref,
		Input:       json.RawMessage(input),
		Reason:      "cubecade_shared_browser_proof",
		ModelCallID: responseID + "-call",
		SessionID:   sessionID,
		ResponseID:  responseID,
	})
	if err != nil {
		return "", webmcp.Invocation{}, webmcp.InvokeResult{}, err
	}
	record, ok := broker.Invocation(admitted.InvocationID)
	if !ok {
		return admitted.InvocationID, webmcp.Invocation{}, webmcp.InvokeResult{}, fmt.Errorf("invocation %s was not retained for receipt inspection", admitted.InvocationID)
	}
	waiter, ok := any(broker).(webmcp.InvocationWaiter)
	if !ok {
		return admitted.InvocationID, record, webmcp.InvokeResult{}, fmt.Errorf("broker does not expose terminal invocation waiting")
	}
	terminal, err := waiter.WaitInvocation(ctx, admitted.InvocationID)
	return admitted.InvocationID, record, terminal, err
}

func assertSharedBrowserTerminal(t *testing.T, label string, record webmcp.Invocation, terminal webmcp.InvokeResult, browserID webmcp.BrowserID, targetID webmcp.TargetID, sessionID string) {
	t.Helper()
	if record.ID == "" || record.ID != terminal.InvocationID || record.Tool.BrowserID != browserID || record.Tool.TargetID != targetID || record.Tool.Generation == 0 || record.SessionID != sessionID || terminal.ErrorCode != "" {
		t.Fatalf("%s receipt = record:%+v terminal:%+v, want caller/browser/target-correlated successful receipt", label, record, terminal)
	}
}

func assertSharedBrowserWireTraces(t *testing.T, traces []webmcp.WebMCPWireTrace, participant string, browserID webmcp.BrowserID, targetID webmcp.TargetID) {
	t.Helper()
	if len(traces) < 2 {
		t.Fatalf("participant %s wire traces = %+v, want enable and invoke evidence", participant, traces)
	}
	hasEnable := false
	hasInvoke := false
	for _, trace := range traces {
		if trace.BrowserID != browserID || trace.TargetID != targetID || trace.TargetSessionID == "" || trace.Phase != webmcp.WebMCPWirePhaseBeforeDispatch || !trace.ListenerReady {
			t.Fatalf("participant %s wire trace = %+v, want sanitized listener-ready shared-target identity", participant, trace)
		}
		switch trace.Method {
		case webmcp.WebMCPEnableMethod:
			hasEnable = true
		case webmcp.WebMCPInvokeToolMethod:
			hasInvoke = true
		}
	}
	if !hasEnable || !hasInvoke {
		t.Fatalf("participant %s wire traces = %+v, want WebMCP.enable and WebMCP.invokeTool", participant, traces)
	}
}

func sharedBrowserWireSessionID(traces []webmcp.WebMCPWireTrace) string {
	for _, trace := range traces {
		if trace.TargetSessionID != "" {
			return trace.TargetSessionID
		}
	}
	return ""
}

type cubecadeSharedBrowserState struct {
	URL         string   `json:"url"`
	Accepted    bool     `json:"accepted"`
	Solved      bool     `json:"solved"`
	QueueDepth  int      `json:"queue_depth"`
	MoveCount   int      `json:"move_count"`
	LastMoves   []string `json:"last_moves"`
	VisibleText string   `json:"visible_text"`
}

func inspectCubecadeSharedBrowserTarget(ctx context.Context, endpoint, targetID string) (state cubecadeSharedBrowserState, err error) {
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
	if err := chromedp.Run(targetContext, chromedp.WaitReady("#cube-state")); err != nil {
		return state, fmt.Errorf("wait for Cubecade shared page: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(cubecadeSharedBrowserStateExpression(), &state)); err != nil {
		return state, fmt.Errorf("read Cubecade shared page state: %w", err)
	}
	return state, nil
}

func cubecadeSharedBrowserStateExpression() string {
	return `(() => {
  const state = window.__cubecadeSharedBrowserState || {};
  const visible = document.querySelector("#cube-state");
  return {
    url: location.href,
    accepted: Boolean(state.accepted),
    solved: Boolean(state.solved),
    queue_depth: Number(state.queueDepth || 0),
    move_count: Number(state.moveCount || 0),
    last_moves: Array.isArray(state.lastMoves) ? state.lastMoves.map((value) => String(value)) : [],
    visible_text: visible ? String(visible.textContent || "") : ""
  };
})()`
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type cubecadeSharedBrowserFixture struct {
	server *httptest.Server
	close  sync.Once
}

func newCubecadeSharedBrowserFixture() *cubecadeSharedBrowserFixture {
	fixture := &cubecadeSharedBrowserFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		_, _ = writer.Write(cubecadeSharedBrowserFixtureHTML)
	}))
	return fixture
}

func (f *cubecadeSharedBrowserFixture) URL() string {
	if f == nil || f.server == nil {
		return ""
	}
	return f.server.URL + "/"
}

func (f *cubecadeSharedBrowserFixture) Close() {
	if f == nil {
		return
	}
	f.close.Do(func() {
		if f.server != nil {
			f.server.Close()
		}
	})
}
