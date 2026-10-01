//go:build e2e

package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestPinnedChromeWebMCPAdapterIntegration(t *testing.T) {
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
