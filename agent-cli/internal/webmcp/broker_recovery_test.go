package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerDisconnectDuringSelectionUnblocksWithBrowserLoss(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-selection", Product: "fixture", Loopback: true}
	runtime := newRecoveryRuntime(candidate, testkit.NewTargetConfig(webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-selection",
		Type:      "page",
	}))
	defer func() { _ = runtime.Close() }()

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	handle := handleValue.(*testkit.ScriptedBrowserHandle)
	handle.BlockListTargets()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() { _ = broker.Close() }()

	operationCursor := runtime.OperationCursor()
	selectionDone := make(chan selectionCall, 1)
	go func() {
		page, selectErr := broker.Select(context.Background(), webmcp.TargetSelector{
			BrowserID: candidate.ID,
			TargetID:  "tab-selection",
		})
		selectionDone <- selectionCall{page: page, err: selectErr}
	}()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), testkit.OperationListTargets, operationCursor); err != nil {
		t.Fatalf("wait blocked selection list: %v", err)
	}

	if err := handle.Disconnect("selection_loss"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}
	selection := receiveSelectionCall(t, selectionDone)
	classified := assertBrowserDisconnected(t, selection.err, "list_targets")
	if selection.page != (webmcp.PageContext{}) {
		t.Fatalf("selection page = %#v, want empty page on failure", selection.page)
	}
	if classified.Details["browser_id"] != string(candidate.ID) || classified.Details["target_id"] != "" || classified.Details["reconnect_required"] != true {
		t.Fatalf("selection loss details = %#v, want bounded browser identity and reconnect guidance", classified.Details)
	}
	if countRecoveryOperations(runtime.Operations(), testkit.OperationAttach) != 0 {
		t.Fatalf("selection operations = %#v, want no attach after list loss", runtime.Operations())
	}
}

func TestStatefulBrokerDisconnectDuringEnableRetiresSelectionAndUnblocksOnce(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-enable", Product: "fixture", Loopback: true}
	runtime := newRecoveryRuntime(candidate, testkit.NewTargetConfig(
		webmcp.Target{BrowserID: candidate.ID, ID: "tab-enable", Type: "page"},
		testkit.WithBlockedEnable(),
	))
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() { _ = broker.Close() }()

	operationCursor := runtime.OperationCursor()
	selectionDone := make(chan selectionCall, 1)
	go func() {
		page, selectErr := broker.Select(context.Background(), webmcp.TargetSelector{
			BrowserID: candidate.ID,
			TargetID:  "tab-enable",
		})
		selectionDone <- selectionCall{page: page, err: selectErr}
	}()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), testkit.OperationEnableWebMCP, operationCursor); err != nil {
		t.Fatalf("wait blocked enable: %v", err)
	}
	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	if err := handle.Disconnect("enable_loss"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}

	selection := receiveSelectionCall(t, selectionDone)
	classified := assertBrowserDisconnected(t, selection.err, "session")
	if selection.page != (webmcp.PageContext{}) {
		t.Fatalf("selection page = %#v, want empty page on failure", selection.page)
	}
	if classified.Details["browser_id"] != string(candidate.ID) || classified.Details["target_id"] != "tab-enable" || classified.Details["reconnect_required"] != true {
		t.Fatalf("enable loss details = %#v, want selected target identity and reconnect guidance", classified.Details)
	}
	if _, err := broker.Selected(context.Background()); !isClassifiedCode(err, webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("selected after enable loss = %v, want browser_disconnected", err)
	}
	if _, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{}); !isClassifiedCode(err, webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("catalog after enable loss = %v, want browser_disconnected", err)
	}
	if countRecoveryOperations(runtime.Operations(), testkit.OperationDisconnect) != 1 {
		t.Fatalf("disconnect operations = %#v, want one", runtime.Operations())
	}
}

func TestStatefulBrokerDisconnectBeforeInvocationDispatchReturnsOneBrowserLoss(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-predispatch", Product: "fixture", Loopback: true}
	runtime := newRecoveryRuntime(candidate, testkit.NewTargetConfig(
		webmcp.Target{BrowserID: candidate.ID, ID: "tab-predispatch", Type: "page"},
		testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{"type":"object"}`)),
	))
	defer func() { _ = runtime.Close() }()
	broker, _, ref := newRecoveryInvocationBroker(t, runtime, candidate, "tab-predispatch")
	defer func() { _ = broker.Close() }()

	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	handle.BlockListTargets()
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchContext)
	operationCursor := runtime.OperationCursor()
	invokeDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{
			ToolRef: ref,
			Input:   json.RawMessage(`{"value":1}`),
		})
		invokeDone <- invocationCall{result: result, err: invokeErr}
	}()
	created := assertInvocationCreated(t, watch, ref)
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), testkit.OperationListTargets, operationCursor); err != nil {
		t.Fatalf("wait pre-dispatch target check: %v", err)
	}
	if err := handle.Disconnect("pre_dispatch_loss"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}

	call := receiveInvocationCall(t, invokeDone)
	if call.err == nil || call.result.InvocationID != created.InvocationID || call.result.ErrorCode != string(webmcp.ErrorBrowserDisconnected) || call.result.State != webmcp.InvocationError {
		t.Fatalf("pre-dispatch invocation = %#v, %v; want one browser_disconnected result", call.result, call.err)
	}
	assertBrowserDisconnected(t, call.err, "list_targets")
	terminalEvent := nextRecoveryBrokerEvent(t, watch)
	if terminalEvent.Type != webmcp.BrokerEventInvocationTerminal || terminalEvent.InvocationID != created.InvocationID || terminalEvent.Reason != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("pre-dispatch terminal event = %#v, want one correlated browser loss", terminalEvent)
	}
	if countRecoveryOperations(runtime.Operations(), testkit.OperationInvoke) != 0 {
		t.Fatalf("pre-dispatch operations = %#v, want no page invocation", runtime.Operations())
	}
	if _, err := broker.WaitInvocation(context.Background(), created.InvocationID); err != nil {
		t.Fatalf("consume pre-dispatch terminal result: %v", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), created.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("second pre-dispatch terminal delivery = %v, want ErrInvocationNotFound", err)
	}
}

func TestStatefulBrokerDisconnectDuringCatalogRefreshReturnsBrowserLoss(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-refresh", Product: "fixture", Loopback: true}
	runtime := newRecoveryRuntime(candidate, testkit.NewTargetConfig(
		webmcp.Target{BrowserID: candidate.ID, ID: "tab-refresh", Type: "page"},
		testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{"type":"object"}`)),
	))
	defer func() { _ = runtime.Close() }()
	broker, session, _ := newRecoveryInvocationBroker(t, runtime, candidate, "tab-refresh")
	defer func() { _ = broker.Close() }()
	session.BlockEnableWebMCP()

	operationCursor := runtime.OperationCursor()
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := broker.ListTools(context.Background(), webmcp.ListToolsOptions{Refresh: true})
		refreshDone <- refreshErr
	}()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), testkit.OperationEnableWebMCP, operationCursor); err != nil {
		t.Fatalf("wait blocked catalog refresh: %v", err)
	}
	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	if err := handle.Disconnect("refresh_loss"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}
	assertBrowserDisconnected(t, <-refreshDone, "session")
	if _, err := broker.Selected(context.Background()); !isClassifiedCode(err, webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("selected after refresh loss = %v, want browser_disconnected", err)
	}
}

func TestStatefulBrokerDisconnectAfterDispatchWinsOverLateResponse(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-postdispatch", Product: "fixture", Loopback: true}
	runtime := newRecoveryRuntime(candidate, testkit.NewTargetConfig(
		webmcp.Target{BrowserID: candidate.ID, ID: "tab-postdispatch", Type: "page"},
		testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{"type":"object"}`)),
	))
	defer func() { _ = runtime.Close() }()
	broker, session, ref := newRecoveryInvocationBroker(t, runtime, candidate, "tab-postdispatch")
	defer func() { _ = broker.Close() }()
	session.BlockInvocations()

	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchContext)
	invokeDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{
			ToolRef: ref,
			Input:   json.RawMessage(`{"value":1}`),
		})
		invokeDone <- invocationCall{result: result, err: invokeErr}
	}()
	created := assertInvocationCreated(t, watch, ref)
	dispatched := receiveInvocationCall(t, invokeDone)
	if dispatched.err != nil || dispatched.result.State != webmcp.InvocationDispatched || dispatched.result.InvocationID != created.InvocationID {
		t.Fatalf("post-dispatch admission = %#v, %v; want dispatched", dispatched.result, dispatched.err)
	}
	if _, err := session.WaitForInvocation(testContext(t)); err != nil {
		t.Fatalf("wait page invocation: %v", err)
	}
	assertInvocationStarted(t, watch, created.InvocationID, ref)

	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	if err := handle.Disconnect("post_dispatch_loss"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}
	terminalEvent := nextRecoveryBrokerEvent(t, watch)
	if terminalEvent.Type != webmcp.BrokerEventInvocationTerminal || terminalEvent.InvocationID != created.InvocationID || terminalEvent.Reason != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("post-dispatch terminal event = %#v, want one correlated browser loss", terminalEvent)
	}
	terminal, err := broker.WaitInvocation(context.Background(), created.InvocationID)
	if err != nil {
		t.Fatalf("wait post-dispatch terminal result: %v", err)
	}
	if terminal.InvocationID != created.InvocationID || terminal.State != webmcp.InvocationError || terminal.ErrorCode != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("post-dispatch terminal result = %#v, want browser_disconnected", terminal)
	}
	if terminal.ErrorDetails["browser_id"] != string(candidate.ID) || terminal.ErrorDetails["target_id"] != "tab-postdispatch" || terminal.ErrorDetails["phase"] != "lifecycle" || terminal.ErrorDetails["reconnect_required"] != true {
		t.Fatalf("post-dispatch terminal details = %#v, want frozen lifecycle details", terminal.ErrorDetails)
	}
	if observations := session.TerminalObservations(); len(observations) != 1 || observations[0].Event.Type != webmcp.EventBrowserDisconnected {
		t.Fatalf("page terminal observations = %#v, want one browser disconnect", observations)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("broker pending after disconnect = %#v, want empty", pending)
	}
	if pending := session.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("page pending after disconnect = %#v, want empty", pending)
	}
	if err := session.EmitToolResponse(created.InvocationID, "Completed", json.RawMessage(`{"late":true}`)); !errors.Is(err, webmcp.ErrClosed) && !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("late page response error = %v, want closed/not-found after disconnect", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), created.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("second post-dispatch terminal delivery = %v, want ErrInvocationNotFound", err)
	}
}

func newRecoveryRuntime(candidate webmcp.BrowserCandidate, target testkit.TargetConfig) *testkit.ScriptedBrowserRuntime {
	return testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets:   []testkit.TargetConfig{target},
	})
}

func newRecoveryInvocationBroker(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, candidate webmcp.BrowserCandidate, targetID webmcp.TargetID) (*webmcp.StatefulBroker, *testkit.ScriptedTargetSession, webmcp.ToolRef) {
	t.Helper()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: targetID}); err != nil {
		broker.Close()
		t.Fatalf("select recovery target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		broker.Close()
		t.Fatalf("list recovery tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		broker.Close()
		t.Fatalf("recovery tools = %#v, want one", snapshot.Tools)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		broker.Close()
		t.Fatalf("open recovery handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession(targetID)
	if session == nil {
		broker.Close()
		t.Fatal("recovery session is nil")
	}
	return broker, session, snapshot.Tools[0].Ref
}

type selectionCall struct {
	page webmcp.PageContext
	err  error
}

func receiveSelectionCall(t *testing.T, results <-chan selectionCall) selectionCall {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broker selection")
		return selectionCall{}
	}
}

func nextRecoveryBrokerEvent(t *testing.T, events <-chan webmcp.BrokerEvent) webmcp.BrokerEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broker lifecycle event")
		return webmcp.BrokerEvent{}
	}
}

func assertBrowserDisconnected(t *testing.T, err error, phase string) *webmcp.ClassifiedError {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded, want browser_disconnected")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
		t.Fatalf("error = %v (%T), want browser_disconnected", err, err)
	}
	if phase != "" && classified.Details["phase"] != phase && phase != "session" {
		t.Fatalf("browser loss details = %#v, want phase %q", classified.Details, phase)
	}
	return classified
}

func isClassifiedCode(err error, code webmcp.ErrorCode) bool {
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == code
}

func countRecoveryOperations(operations []testkit.Operation, kind testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
