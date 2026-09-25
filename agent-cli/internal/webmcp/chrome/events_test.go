package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
	cdpTarget "github.com/chromedp/cdproto/target"
	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type callbackExecutor struct {
	base      *recordingExecutor
	onExecute func(string) error
	onResult  func(string, any)
}

func (e *callbackExecutor) Execute(ctx context.Context, method string, params, result any) error {
	if e.onExecute != nil {
		if err := e.onExecute(method); err != nil {
			return err
		}
	}
	if err := e.base.Execute(ctx, method, params, result); err != nil {
		return err
	}
	if e.onResult != nil {
		e.onResult(method, result)
	}
	return nil
}

func nextBrowserEvent(t *testing.T, events <-chan webmcp.BrowserEvent) webmcp.BrowserEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("browser event channel closed before the expected event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for browser event")
		return webmcp.BrowserEvent{}
	}
}

func TestWebMCPEnablePublishesExplicitEmptyCatalogEvidence(t *testing.T) {
	session := newProbeTargetSession(t, []byte(`{"producer_present":true,"catalog_ready":true,"tool_count":0}`), "session-empty-catalog", "target-empty-catalog", "https://example.test/empty")
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close empty-catalog session: %v", err)
		}
	}()

	if err := session.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	page := session.Context()
	if !page.WebMCPDomainSupported || !page.CatalogReady || !page.Ready {
		t.Fatalf("page readiness = %+v, want supported domain and explicit empty catalog readiness", page)
	}
	event := nextBrowserEvent(t, session.Events())
	if event.Type != webmcp.EventCatalogReady || !event.CatalogReady || !event.ToolCountKnown || event.ToolCount != 0 {
		t.Fatalf("catalog event = %+v, want explicit empty catalog evidence", event)
	}
}

type documentReadinessCase struct {
	name            string
	readyState      string
	loading         bool
	loadingKnown    bool
	producerPresent bool
}

func TestWebMCPEnableReportsDocumentReadinessWithoutPageData(t *testing.T) {
	tests := []documentReadinessCase{
		{
			name:            "loading_without_producer",
			readyState:      webmcp.DocumentReadyStateLoading,
			loading:         true,
			loadingKnown:    true,
			producerPresent: false,
		},
		{
			name:            "loading_empty_catalog_is_not_ready",
			readyState:      webmcp.DocumentReadyStateLoading,
			loading:         true,
			loadingKnown:    true,
			producerPresent: true,
		},
		{
			name:            "complete_without_producer",
			readyState:      webmcp.DocumentReadyStateComplete,
			loading:         false,
			loadingKnown:    true,
			producerPresent: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertDocumentReadinessWithoutPageData(t, test)
		})
	}
}

// executeTargetActions runs chromedp actions directly against a fake executor.
func executeTargetActions(ctx context.Context, executor cdp.Executor, actions []chromedp.Action) error {
	actionContext := cdp.WithExecutor(ctx, executor)
	for _, action := range actions {
		if err := action.Do(actionContext); err != nil {
			return err
		}
	}
	return nil
}

// injectEvaluateResult replaces every Runtime.evaluate result with value.
func injectEvaluateResult(t *testing.T, value []byte) func(string, any) {
	return func(method string, result any) {
		if method != runtime.CommandEvaluate {
			return
		}
		returns, ok := result.(*runtime.EvaluateReturns)
		if !ok {
			t.Fatalf("Runtime.evaluate result = %T, want *runtime.EvaluateReturns", result)
		}
		returns.Result = &runtime.RemoteObject{Value: jsontext.Value(value)}
	}
}

// newProbeTargetSession builds an attached target session whose
// Runtime.evaluate catalog probe always returns probeResult.
func newProbeTargetSession(t *testing.T, probeResult []byte, sessionID, targetID, pageURL string) *targetSession {
	t.Helper()
	baseExecutor := &recordingExecutor{}
	protocolExecutor := &callbackExecutor{base: baseExecutor}
	protocolExecutor.onResult = injectEvaluateResult(t, probeResult)
	handle := testHandle(baseExecutor)
	handle.browserExecutor = protocolExecutor
	targetContext, rawCancel := chromedp.NewContext(context.Background())
	protocolTarget := &chromedp.Target{SessionID: cdpTarget.SessionID(sessionID), TargetID: cdpTarget.ID(targetID)}
	chromedp.FromContext(targetContext).Target = protocolTarget
	session := newTargetSession(handle, targetContext, rawCancel, webmcp.Target{
		BrowserID: handle.candidate.ID,
		ID:        webmcp.TargetID(protocolTarget.TargetID),
		Type:      pageTargetType,
		URL:       pageURL,
	}, webmcp.TargetOwnershipExternal)
	session.setProtocolTarget(protocolTarget)
	session.runAction = func(ctx context.Context, actions ...chromedp.Action) error {
		return executeTargetActions(ctx, protocolExecutor, actions)
	}
	handle.sessions[session] = struct{}{}
	return session
}

func assertDocumentReadinessWithoutPageData(t *testing.T, test documentReadinessCase) {
	t.Helper()
	probeResult, err := json.Marshal(map[string]any{
		"producer_present":       test.producerPresent,
		"catalog_ready":          test.producerPresent,
		"tool_count":             0,
		"document_ready_state":   test.readyState,
		"document_loading":       test.loading,
		"document_loading_known": test.loadingKnown,
	})
	if err != nil {
		t.Fatalf("marshal probe result: %v", err)
	}
	session := newProbeTargetSession(t, probeResult, "session-readiness-"+test.name, "target-readiness-"+test.name, "https://example.test/readiness/"+test.name)
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close readiness session: %v", err)
		}
	}()

	if err := session.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	page := session.Context()
	if page.DocumentReadyState != test.readyState || page.DocumentLoading != test.loading || page.DocumentLoadingKnown != test.loadingKnown {
		t.Fatalf("document readiness = %+v, want state=%q loading=%t known=%t", page, test.readyState, test.loading, test.loadingKnown)
	}
	if page.CatalogReady || page.Ready {
		t.Fatalf("page readiness = %+v, want no catalog readiness from producer-less probe", page)
	}
}

func assertIncreasingSequence(t *testing.T, previous, current webmcp.BrowserEvent) {
	t.Helper()
	if current.Sequence <= previous.Sequence {
		t.Fatalf("event sequence = %d after %d, want strictly increasing", current.Sequence, previous.Sequence)
	}
}

const (
	eventsTestTargetID  = "target-events"
	eventsTestFrameID   = "frame-events"
	eventsTestToolName  = "submit_form"
	eventsTestInputJSON = `{"count":9007199254740993,"nested":{"ok":true}}`
	eventsTestSchema    = `{"type":"object","properties":{"count":{"type":"integer"}}}`
	eventsTestOutput    = `{"ok":true,"value":9007199254740993}`
)

func TestWebMCPEventsListenBeforeEnableAndPreserveOrder(t *testing.T) {
	h := newListenBeforeEnableHarness(t)
	sessionValue, err := h.handle.Attach(context.Background(), eventsTestTargetID, webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach event target: %v", err)
	}
	session, ok := sessionValue.(*targetSession)
	if !ok {
		t.Fatalf("attached session = %T, want *targetSession", sessionValue)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close event target: %v", err)
		}
	}()

	if err := session.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	if h.runCalls != 2 {
		t.Fatalf("target action runs = %d, want attach/bootstrap and enable", h.runCalls)
	}
	if len(h.phases) < 3 || h.phases[0] != "listen" || h.phases[len(h.phases)-1] != "enable-command" {
		t.Fatalf("listener/enable phases = %v, want listener before enable command", h.phases)
	}
	if h.registeredListener == nil || h.listener == nil {
		t.Fatal("target listener was not retained")
	}

	initialAdded := h.assertInitialToolsAdded(t, session)
	h.assertCopiedToolEvents(t, session, initialAdded)
	h.assertBrowserDetachClosesEvents(t, session)
}

// listenBeforeEnableHarness records the order in which the target session
// registers listeners and runs protocol actions against a fake executor.
type listenBeforeEnableHarness struct {
	handle             *handle
	protocolTarget     *chromedp.Target
	added              *cdpWebMCP.EventToolsAdded
	listener           func(any)
	registeredListener func(any)
	browserListener    func(any)
	phases             []string
	runCalls           int
}

func newListenBeforeEnableHarness(t *testing.T) *listenBeforeEnableHarness {
	t.Helper()
	baseExecutor := &recordingExecutor{}
	protocolExecutor := &callbackExecutor{base: baseExecutor}
	h := &listenBeforeEnableHarness{handle: testHandle(baseExecutor)}
	h.handle.browserExecutor = protocolExecutor
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != jsonListPath {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`[{"id":"target-events","type":"page","title":"Events","url":"https://example.test/events","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/target-events"}]`)); err != nil {
			t.Errorf("write target list: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	h.handle.candidate.HTTPURL = server.URL

	h.protocolTarget = &chromedp.Target{SessionID: "session-events", TargetID: eventsTestTargetID}
	h.added = &cdpWebMCP.EventToolsAdded{Tools: []*cdpWebMCP.Tool{{
		Name:        eventsTestToolName,
		Description: "Submit the page form",
		InputSchema: jsontext.Value([]byte(eventsTestSchema)),
		Annotations: &cdpWebMCP.Annotation{ReadOnly: false, UntrustedContent: true, Autosubmit: true},
		FrameID:     eventsTestFrameID,
	}}}
	protocolExecutor.onExecute = func(method string) error {
		if method != cdpWebMCP.CommandEnable {
			return nil
		}
		h.phases = append(h.phases, "enable-command")
		if h.listener == nil {
			return errors.New("WebMCP.enable ran before target listener registration")
		}
		h.listener(h.added)
		return nil
	}
	h.handle.targetOps = targetContextOps{
		newContext: func(context.Context, cdpTarget.ID) (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		run: func(ctx context.Context, actions ...chromedp.Action) error {
			h.runCalls++
			h.phases = append(h.phases, "run")
			return executeTargetActions(ctx, protocolExecutor, actions)
		},
		target: func(context.Context) *chromedp.Target {
			return h.protocolTarget
		},
		listen: func(_ context.Context, callback func(any)) {
			h.phases = append(h.phases, "listen")
			h.registeredListener = callback
			h.listener = callback
		},
		listenBrowser: func(_ context.Context, callback func(any)) {
			h.phases = append(h.phases, "listen-browser")
			h.browserListener = callback
		},
	}
	return h
}

// assertInitialToolsAdded checks the add that arrives while WebMCP.enable is
// in flight. Mutating the published value must not affect a later conversion
// from the generated event storage.
func (h *listenBeforeEnableHarness) assertInitialToolsAdded(t *testing.T, session *targetSession) webmcp.BrowserEvent {
	t.Helper()
	attached := nextBrowserEvent(t, session.Events())
	initialAdded := nextBrowserEvent(t, session.Events())
	if attached.Type != webmcp.EventTargetAttached || initialAdded.Type != webmcp.EventToolsAdded || initialAdded.FrameID != eventsTestFrameID || initialAdded.Generation != 1 {
		t.Fatalf("initial event order = %s, %s; want target_attached, tools_added", attached.Type, initialAdded.Type)
	}
	assertIncreasingSequence(t, attached, initialAdded)
	if len(initialAdded.Tools) != 1 {
		t.Fatalf("initial tools = %+v, want one tool", initialAdded.Tools)
	}
	initialTool := initialAdded.Tools[0]
	if initialTool.BrowserID != h.handle.candidate.ID || initialTool.TargetID != eventsTestTargetID || initialTool.FrameID != eventsTestFrameID || initialTool.Name != eventsTestToolName {
		t.Fatalf("initial tool identity = %+v, want browser/target/frame/name preserved", initialTool)
	}
	if string(initialTool.InputSchema) != eventsTestSchema || initialTool.Description != "Submit the page form" || initialTool.Generation != 1 {
		t.Fatalf("initial tool metadata = %+v, want schema/description/generation preserved", initialTool)
	}
	assertGeneratedEventAnnotations(t, initialTool.Annotations)
	initialTool.InputSchema[0] = 'X'
	*initialTool.Annotations.ReadOnly = true
	if string(h.added.Tools[0].InputSchema) != eventsTestSchema || h.added.Tools[0].Annotations.ReadOnly {
		t.Fatal("consumer mutation leaked into generated toolsAdded storage")
	}
	return initialAdded
}

func assertGeneratedEventAnnotations(t *testing.T, annotations webmcp.ToolAnnotations) {
	t.Helper()
	if annotations.ReadOnly == nil || *annotations.ReadOnly || annotations.UntrustedContent == nil || !*annotations.UntrustedContent || annotations.AutoSubmit == nil || !*annotations.AutoSubmit {
		t.Fatalf("initial annotations = %+v, want all generated annotation values", annotations)
	}
}

func (h *listenBeforeEnableHarness) assertCopiedToolEvents(t *testing.T, session *targetSession, initialAdded webmcp.BrowserEvent) {
	t.Helper()
	removed := &cdpWebMCP.EventToolsRemoved{Tools: []*cdpWebMCP.RemovedTool{{Name: eventsTestToolName, FrameID: eventsTestFrameID}}}
	invoked := &cdpWebMCP.EventToolInvoked{
		ToolName:     eventsTestToolName,
		FrameID:      eventsTestFrameID,
		InvocationID: "invocation-events",
		Input:        eventsTestInputJSON,
	}
	respondedOutput := jsontext.Value([]byte(eventsTestOutput))
	responded := &cdpWebMCP.EventToolResponded{
		InvocationID: "invocation-events",
		Status:       cdpWebMCP.InvocationStatusCompleted,
		Output:       respondedOutput,
	}
	h.listener(h.added)
	h.listener(removed)
	h.listener(invoked)
	h.listener(responded)

	repeatedAdded := nextBrowserEvent(t, session.Events())
	removedEvent := nextBrowserEvent(t, session.Events())
	invokedEvent := nextBrowserEvent(t, session.Events())
	respondedEvent := nextBrowserEvent(t, session.Events())
	assertIncreasingSequence(t, initialAdded, repeatedAdded)
	assertIncreasingSequence(t, repeatedAdded, removedEvent)
	assertIncreasingSequence(t, removedEvent, invokedEvent)
	assertIncreasingSequence(t, invokedEvent, respondedEvent)
	if repeatedAdded.Type != webmcp.EventToolsAdded || string(repeatedAdded.Tools[0].InputSchema) != eventsTestSchema || *repeatedAdded.Tools[0].Annotations.ReadOnly {
		t.Fatalf("repeated toolsAdded = %+v, want an unchanged defensive copy", repeatedAdded)
	}
	if removedEvent.Type != webmcp.EventToolsRemoved || removedEvent.FrameID != eventsTestFrameID || removedEvent.Generation != 1 || len(removedEvent.RemovedToolNames) != 1 || removedEvent.RemovedToolNames[0] != eventsTestToolName {
		t.Fatalf("toolsRemoved event = %+v, want frame and tool name", removedEvent)
	}
	assertCopiedInvocationEvents(t, invokedEvent, respondedEvent, respondedOutput)
}

func assertCopiedInvocationEvents(t *testing.T, invokedEvent, respondedEvent webmcp.BrowserEvent, respondedOutput jsontext.Value) {
	t.Helper()
	if invokedEvent.Type != webmcp.EventToolInvoked || invokedEvent.Generation != 1 || invokedEvent.FrameID != eventsTestFrameID || invokedEvent.ToolName != eventsTestToolName || invokedEvent.InvocationID != "invocation-events" || string(invokedEvent.Input) != eventsTestInputJSON {
		t.Fatalf("toolInvoked event = %+v, want correlated copied input", invokedEvent)
	}
	if respondedEvent.Type != webmcp.EventToolResponded || respondedEvent.Generation != 1 || respondedEvent.InvocationID != "invocation-events" || respondedEvent.Status != string(cdpWebMCP.InvocationStatusCompleted) || string(respondedEvent.Output) != eventsTestOutput || respondedEvent.ErrorCode != "" {
		t.Fatalf("toolResponded event = %+v, want completed copied output", respondedEvent)
	}
	respondedEvent.Output[0] = 'X'
	if string(respondedOutput) != eventsTestOutput {
		t.Fatal("consumer mutation leaked into generated toolResponded output")
	}
}

func (h *listenBeforeEnableHarness) assertBrowserDetachClosesEvents(t *testing.T, session *targetSession) {
	t.Helper()
	if h.browserListener == nil {
		t.Fatal("browser lifecycle listener was not registered")
	}
	h.browserListener(&cdpTarget.EventDetachedFromTarget{SessionID: h.protocolTarget.SessionID})
	detached := nextBrowserEvent(t, session.Events())
	if detached.Type != webmcp.EventTargetDetached || detached.ErrorCode != string(webmcp.ErrorTargetDetached) {
		t.Fatalf("browser listener detach event = %+v, want target_detached terminal event", detached)
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("event channel emitted after browser listener detach")
		}
	case <-time.After(time.Second):
		t.Fatal("event channel did not close after browser listener detach")
	}
}

func newLifecycleTestSession(t *testing.T) (*handle, *targetSession, *chromedp.Target, *int) {
	t.Helper()
	executor := &recordingExecutor{}
	handle := testHandle(executor)
	targetContext, rawCancel := chromedp.NewContext(context.Background())
	protocolTarget := &chromedp.Target{SessionID: "session-lifecycle", TargetID: "target-lifecycle"}
	chromedp.FromContext(targetContext).Target = protocolTarget
	cancelCount := 0
	cancelTarget := func() {
		cancelCount++
		rawCancel()
	}
	session := newTargetSession(handle, targetContext, cancelTarget, webmcp.Target{
		BrowserID: handle.candidate.ID,
		ID:        webmcp.TargetID(protocolTarget.TargetID),
		Type:      pageTargetType,
		URL:       "https://example.test/lifecycle",
	}, webmcp.TargetOwnershipExternal)
	session.setProtocolTarget(protocolTarget)
	handle.sessions[session] = struct{}{}
	return handle, session, protocolTarget, &cancelCount
}

func TestTargetDetachPublishesTerminalEventAndClosesLifecycleOnce(t *testing.T) {
	_, session, protocolTarget, cancelCount := newLifecycleTestSession(t)
	session.enqueueProtocolEvent(&cdpTarget.EventDetachedFromTarget{SessionID: protocolTarget.SessionID})

	terminal := nextBrowserEvent(t, session.Events())
	if terminal.Type != webmcp.EventTargetDetached || terminal.ErrorCode != string(webmcp.ErrorTargetDetached) || terminal.Reason != "target_detached" {
		t.Fatalf("detach terminal event = %+v, want target_detached semantics", terminal)
	}
	if session.Context().Connected {
		t.Fatal("detached session still reports a connected page")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(session.Err(), &classified) || classified.Code != webmcp.ErrorTargetDetached {
		t.Fatalf("detach session error = %v, want target_detached", session.Err())
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("detach event channel emitted an event after its terminal event")
		}
	case <-time.After(time.Second):
		t.Fatal("detach event channel did not close")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("detach Done channel did not close")
	}
	select {
	case <-session.routerDone:
	case <-time.After(time.Second):
		t.Fatal("detach event router did not stop")
	}
	if *cancelCount != 1 || protocolTarget.SessionID != "" || protocolTarget.TargetID != "" {
		t.Fatalf("detach cleanup = cancel count %d, protocol IDs %q/%q; want one cancel and cleared IDs", *cancelCount, protocolTarget.SessionID, protocolTarget.TargetID)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent close after detach: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second idempotent close after detach: %v", err)
	}
}

func TestTargetDestroyedPublishesTargetClosureNotNavigation(t *testing.T) {
	_, session, protocolTarget, _ := newLifecycleTestSession(t)
	session.enqueueProtocolEvent(&cdpTarget.EventTargetDestroyed{TargetID: protocolTarget.TargetID})

	terminal := nextBrowserEvent(t, session.Events())
	if terminal.Type != webmcp.EventTargetDetached || terminal.ErrorCode != string(webmcp.ErrorTargetDetached) || terminal.Reason != "target_closed" {
		t.Fatalf("destroyed terminal event = %+v, want target_closed target_detached semantics", terminal)
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("destroyed session emitted an event after its terminal event")
		}
	case <-time.After(time.Second):
		t.Fatal("destroyed session event channel did not close")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("destroyed session Done channel did not close")
	}
}

func TestBrowserDisconnectPublishesTerminalEventAndClosesLifecycleOnce(t *testing.T) {
	_, session, protocolTarget, cancelCount := newLifecycleTestSession(t)
	session.transportLost()

	terminal := nextBrowserEvent(t, session.Events())
	if terminal.Type != webmcp.EventBrowserDisconnected || terminal.ErrorCode != string(webmcp.ErrorBrowserDisconnected) || terminal.Reason != "browser_disconnected" {
		t.Fatalf("disconnect terminal event = %+v, want browser_disconnected semantics", terminal)
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(session.Err(), &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
		t.Fatalf("disconnect session error = %v, want browser_disconnected", session.Err())
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("disconnect event channel emitted an event after its terminal event")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect event channel did not close")
	}
	if *cancelCount != 1 || protocolTarget.SessionID != "" || protocolTarget.TargetID != "" {
		t.Fatalf("disconnect cleanup = cancel count %d, protocol IDs %q/%q; want one cancel and cleared IDs", *cancelCount, protocolTarget.SessionID, protocolTarget.TargetID)
	}
	session.transportLost()
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent close after disconnect: %v", err)
	}
	if *cancelCount != 1 {
		t.Fatalf("disconnect repeated cleanup count = %d, want one", *cancelCount)
	}
}

func TestTargetSessionEventOverflowPublishesExplicitFailure(t *testing.T) {
	_, session, _, _ := newLifecycleTestSession(t)
	// The helper uses a larger physical channel so this test can exercise the
	// ordinary-capacity guard without depending on a scheduler race. Production
	// construction reserves the same terminal slot for the configured buffer.
	session.eventBuffer = 1

	session.publish(webmcp.BrowserEvent{Type: webmcp.EventToolsAdded})
	session.publish(webmcp.BrowserEvent{Type: webmcp.EventToolInvoked, InvocationID: "overflowed"})

	first := nextBrowserEvent(t, session.Events())
	if first.Type != webmcp.EventToolsAdded {
		t.Fatalf("first buffered event = %+v, want original event", first)
	}
	failure := nextBrowserEvent(t, session.Events())
	if failure.Type != webmcp.EventSessionClosed || failure.Reason != webmcp.BrowserEventBufferFullReason || failure.ErrorCode != string(webmcp.ErrorBrowserProtocol) {
		t.Fatalf("overflow event = %+v, want explicit event-buffer failure", failure)
	}
	if failure.Sequence <= first.Sequence {
		t.Fatalf("overflow sequence = %d after first sequence %d, want increasing", failure.Sequence, first.Sequence)
	}
	if !errors.Is(session.Err(), webmcp.ErrEventBufferFull) {
		t.Fatalf("session error = %v, want ErrEventBufferFull", session.Err())
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("overflowed session did not close")
	}
	select {
	case _, ok := <-session.Events():
		if ok {
			t.Fatal("overflowed session emitted an event after its failure")
		}
	case <-time.After(time.Second):
		t.Fatal("overflowed session event channel did not close")
	}
	select {
	case <-session.routerDone:
	case <-time.After(time.Second):
		t.Fatal("overflowed protocol router did not stop")
	}
}
