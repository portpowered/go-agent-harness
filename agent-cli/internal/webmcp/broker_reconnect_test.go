package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// TestStatefulBrokerRecoversThroughTwoExplicitFreshSelections proves the
// complete reconnect boundary in one broker. Each replacement reuses the
// endpoint and target ID, but receives a new browser identity, session, tool
// catalog, and invocation namespace. The old session is retained only as a
// source for late-event injection.
func TestStatefulBrokerRecoversThroughTwoExplicitFreshSelections(t *testing.T) {
	const (
		targetID = webmcp.TargetID("tab-reconnect")
		endpoint = "http://127.0.0.1:9222"
	)
	oldCandidate := reconnectCandidate("browser-old", endpoint, "old")
	newCandidate := reconnectCandidate("browser-new", endpoint, "new")
	finalCandidate := reconnectCandidate("browser-final", endpoint, "final")

	discoverer := &replacementDiscoverer{candidates: []webmcp.BrowserCandidate{oldCandidate}}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: oldCandidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: oldCandidate.ID, ID: targetID, Type: "page", Title: "same tab", URL: "https://recover.test/old", Origin: "https://recover.test", Eligible: true},
					testkit.WithInitialCatalog(pageTool("old_tool", "frame-old", `{}`)),
				),
			},
		},
	)
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: discoverer,
		IDs:        testkit.NewDeterministicIDs(),
	})
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: oldCandidate.ID, TargetID: targetID}); err != nil {
		t.Fatalf("select initial target: %v", err)
	}
	oldCatalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list initial catalog: %v", err)
	}
	if len(oldCatalog.Tools) != 1 || oldCatalog.Tools[0].Name != "old_tool" {
		t.Fatalf("initial catalog = %#v, want old tool", oldCatalog.Tools)
	}
	oldRef := oldCatalog.Tools[0].Ref
	oldHandle := runtime.Browser(oldCandidate.ID)
	if oldHandle == nil {
		t.Fatal("initial browser handle is nil")
	}
	oldSession := oldHandle.TargetSession(targetID)
	if oldSession == nil {
		t.Fatal("initial target session is nil")
	}

	// Leave an invocation admitted on the retired session so disconnect
	// reconciliation is exercised before the first replacement.
	oldSession.BlockInvocations()
	first, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: oldRef, Input: json.RawMessage(`{}`)})
	if err != nil || first.InvocationID == "" || first.State != webmcp.InvocationDispatched {
		t.Fatalf("initial invocation = %#v err=%v, want dispatched", first, err)
	}
	if _, err := oldSession.WaitForInvocation(testContext(t)); err != nil {
		t.Fatalf("wait initial invocation admission: %v", err)
	}
	if err := oldHandle.Disconnect("first_reconnect"); err != nil {
		t.Fatalf("disconnect initial browser: %v", err)
	}
	firstTerminal, err := broker.WaitInvocation(testContext(t), first.InvocationID)
	if err != nil {
		t.Fatalf("wait initial disconnect terminal: %v", err)
	}
	if firstTerminal.ErrorCode != string(webmcp.ErrorBrowserDisconnected) || firstTerminal.State != webmcp.InvocationError {
		t.Fatalf("initial disconnect terminal = %#v, want browser_disconnected", firstTerminal)
	}
	select {
	case <-oldSession.Done():
	default:
		t.Fatal("initial session remained live after browser loss")
	}

	newHandle, newSession, newRef := replaceAndSelect(t, broker, runtime, discoverer, oldCandidate, newCandidate, targetID, "new_tool", "frame-new", `{"cycle":1}`)
	if oldHandle.TargetSession(targetID) == nil {
		t.Fatal("retired old session was not retained for late-event inspection")
	}
	assertStaleToolRef(t, broker, oldRef, "old ref after first fresh selection")
	assertNoCancelForInvocation(t, runtime, first.InvocationID, "first invocation after first fresh selection")

	// A retired session can still produce a late event, but the browser/target
	// identity fence must prevent it from changing the replacement catalog.
	if err := oldSession.InjectLateEventInto(newSession, webmcp.BrowserEvent{
		Type:       webmcp.EventToolsAdded,
		Generation: 1,
		Tools:      []webmcp.ToolDescriptor{{Name: "late_old_tool", FrameID: "frame-old"}},
	}); err != nil {
		t.Fatalf("inject first retired-session event: %v", err)
	}
	if _, err := broker.Selected(testContext(t)); err != nil {
		t.Fatalf("flush first retired-session event: %v", err)
	}
	current, err := broker.ListTools(testContext(t), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("list first replacement catalog: %v", err)
	}
	if len(current.Tools) != 1 || current.Tools[0].Name != "new_tool" || current.Tools[0].Ref != newRef {
		t.Fatalf("catalog after first late event = %#v, want only new tool/ref %q", current.Tools, newRef)
	}

	firstFresh, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: newRef, Input: json.RawMessage(`{"value":1}`)})
	if err != nil || firstFresh.InvocationID == first.InvocationID {
		t.Fatalf("first fresh invocation = %#v err=%v, want a new ID", firstFresh, err)
	}
	firstFreshTerminal, err := broker.WaitInvocation(testContext(t), firstFresh.InvocationID)
	if err != nil || firstFreshTerminal.ErrorCode != "" || firstFreshTerminal.State != webmcp.InvocationCompleted {
		t.Fatalf("first fresh terminal = %#v err=%v, want completed", firstFreshTerminal, err)
	}
	if record, ok := newSession.Invocation(firstFresh.InvocationID); !ok || record.BrowserID != newCandidate.ID || record.Generation != 1 {
		t.Fatalf("first fresh session invocation = %#v ok=%v, want new identity", record, ok)
	}

	// Repeat the same lifecycle with the first replacement. This catches
	// accidental reuse of the first replacement's selected session, catalog,
	// terminal cache, or persistence-like browser lookup state.
	newSession.BlockInvocations()
	second, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: newRef, Input: json.RawMessage(`{}`)})
	if err != nil || second.InvocationID == first.InvocationID || second.State != webmcp.InvocationDispatched {
		t.Fatalf("second blocked invocation = %#v err=%v, want a distinct dispatched ID", second, err)
	}
	if _, err := newSession.WaitForInvocation(testContext(t)); err != nil {
		t.Fatalf("wait second invocation admission: %v", err)
	}
	if err := newHandle.Disconnect("second_reconnect"); err != nil {
		t.Fatalf("disconnect first replacement browser: %v", err)
	}
	secondTerminal, err := broker.WaitInvocation(testContext(t), second.InvocationID)
	if err != nil || secondTerminal.ErrorCode != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("second disconnect terminal = %#v err=%v, want browser_disconnected", secondTerminal, err)
	}
	select {
	case <-newSession.Done():
	default:
		t.Fatal("first replacement session remained live after browser loss")
	}

	_, finalSession, finalRef := replaceAndSelect(t, broker, runtime, discoverer, newCandidate, finalCandidate, targetID, "final_tool", "frame-final", `{"cycle":2}`)
	assertStaleToolRef(t, broker, newRef, "first replacement ref after second fresh selection")
	assertNoCancelForInvocation(t, runtime, second.InvocationID, "second invocation after second fresh selection")
	if err := newSession.InjectLateEventInto(finalSession, webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		Generation:   1,
		InvocationID: second.InvocationID,
		Status:       "Completed",
		Output:       json.RawMessage(`{"poison":true}`),
	}); err != nil {
		t.Fatalf("inject second retired-session response: %v", err)
	}
	if _, err := broker.Selected(testContext(t)); err != nil {
		t.Fatalf("flush second retired-session response: %v", err)
	}
	finalCatalog, err := broker.ListTools(testContext(t), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("list final catalog: %v", err)
	}
	if len(finalCatalog.Tools) != 1 || finalCatalog.Tools[0].Name != "final_tool" || finalCatalog.Tools[0].Ref != finalRef {
		t.Fatalf("final catalog after late response = %#v, want only final tool/ref %q", finalCatalog.Tools, finalRef)
	}

	finalInvocation, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: finalRef, Input: json.RawMessage(`{"value":2}`)})
	if err != nil || finalInvocation.InvocationID == first.InvocationID || finalInvocation.InvocationID == second.InvocationID {
		t.Fatalf("final invocation = %#v err=%v, want fresh ID", finalInvocation, err)
	}
	finalTerminal, err := broker.WaitInvocation(testContext(t), finalInvocation.InvocationID)
	if err != nil || finalTerminal.State != webmcp.InvocationCompleted || string(finalTerminal.Output) != `{"cycle":2}` {
		t.Fatalf("final terminal = %#v err=%v, want fresh completed result", finalTerminal, err)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending invocations after two reconnects = %#v, want empty", pending)
	}
	if oldPending := oldSession.PendingInvocations(); len(oldPending) != 0 {
		t.Fatalf("old session pending invocations = %#v, want empty after loss", oldPending)
	}
	if firstPending := newSession.PendingInvocations(); len(firstPending) != 0 {
		t.Fatalf("first replacement pending invocations = %#v, want empty after loss", firstPending)
	}
	if got := countReconnectOperations(runtime.Operations(), testkit.OperationAttach); got != 3 {
		t.Fatalf("attach operations = %d, want one per fresh selection", got)
	}
	if got := countReconnectOperations(runtime.Operations(), testkit.OperationInvoke); got != 4 {
		t.Fatalf("invoke operations = %d, want initial plus one per fresh session", got)
	}
	if got := countReconnectOperations(runtime.Operations(), testkit.OperationCancel); got != 0 {
		t.Fatalf("cancel operations = %d, want no cancellation routed to a replacement", got)
	}
	assertReconnectOperationOrder(t, runtime.Operations(), []webmcp.BrowserID{oldCandidate.ID, newCandidate.ID, newCandidate.ID, finalCandidate.ID})

	// The broker and runtime teardown paths are both intentionally idempotent;
	// the deferred calls above make the first close observable, while these
	// explicit calls prove repeated cleanup does not reattach or panic.
	if err := broker.Close(); err != nil {
		t.Fatalf("first explicit broker close: %v", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("second explicit broker close: %v", err)
	}
}

func replaceAndSelect(t *testing.T, broker *webmcp.StatefulBroker, runtime *testkit.ScriptedBrowserRuntime, discoverer *replacementDiscoverer, previous, replacement webmcp.BrowserCandidate, targetID webmcp.TargetID, toolName string, frame webmcp.FrameID, output string) (*testkit.ScriptedBrowserHandle, *testkit.ScriptedTargetSession, webmcp.ToolRef) {
	t.Helper()
	target := webmcp.Target{BrowserID: replacement.ID, ID: targetID, Type: "page", Title: "same tab", URL: "https://recover.test/" + strings.TrimPrefix(string(replacement.ID), "browser-"), Origin: "https://recover.test", Eligible: true}
	handle, err := runtime.ReplaceEndpoint(previous, replacement, testkit.NewTargetConfig(target, testkit.WithInitialCatalog(pageTool(toolName, frame, `{}`)), testkit.WithAutoResponse(json.RawMessage(output))))
	if err != nil {
		t.Fatalf("replace %s with %s: %v", previous.ID, replacement.ID, err)
	}
	discoverer.Replace(replacement)
	candidates, err := broker.Discover(testContext(t), webmcp.DiscoverOptions{})
	if err != nil || len(candidates) != 1 || candidates[0].ID != replacement.ID {
		t.Fatalf("rediscover %s = %#v err=%v", replacement.ID, candidates, err)
	}
	if _, err := broker.Select(testContext(t), webmcp.TargetSelector{BrowserID: replacement.ID, TargetID: targetID}); err != nil {
		t.Fatalf("explicitly select %s: %v", replacement.ID, err)
	}
	catalog, err := broker.ListTools(testContext(t), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list %s catalog: %v", replacement.ID, err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != toolName || catalog.Tools[0].Generation != 1 {
		t.Fatalf("%s catalog = %#v, want fresh %s at generation one", replacement.ID, catalog.Tools, toolName)
	}
	newSession := handle.TargetSession(targetID)
	if newSession == nil {
		t.Fatalf("%s target session is nil", replacement.ID)
	}
	return handle, newSession, catalog.Tools[0].Ref
}

func reconnectCandidate(id, endpoint, instance string) webmcp.BrowserCandidate {
	return webmcp.BrowserCandidate{
		ID:                webmcp.BrowserID(id),
		HTTPURL:           endpoint,
		BrowserWSURL:      "ws://127.0.0.1:9222/devtools/browser/" + instance,
		BrowserInstanceID: "incarnation-" + strings.Repeat(instance[:1], 24),
		Product:           "Chrome/fixture",
		Loopback:          true,
	}
}

func assertStaleToolRef(t *testing.T, broker *webmcp.StatefulBroker, ref webmcp.ToolRef, label string) {
	t.Helper()
	err := func() error {
		_, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: json.RawMessage(`{}`)})
		return invokeErr
	}()
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorStaleToolRef {
		t.Fatalf("%s: error = %v (%T), want stale_tool_ref", label, err, err)
	}
}

func assertNoCancelForInvocation(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, id webmcp.InvocationID, label string) {
	t.Helper()
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationCancel && operation.InvocationID == id {
			t.Fatalf("%s: found cancellation routed to replacement: %#v", label, operation)
		}
	}
}

func countReconnectOperations(operations []testkit.Operation, kind testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

func assertReconnectOperationOrder(t *testing.T, operations []testkit.Operation, wantInvocations []webmcp.BrowserID) {
	t.Helper()
	var attaches []webmcp.BrowserID
	var invokes []webmcp.BrowserID
	var disconnects []webmcp.BrowserID
	var replacements []webmcp.BrowserID
	for _, operation := range operations {
		switch operation.Kind {
		case testkit.OperationAttach:
			attaches = append(attaches, operation.BrowserID)
		case testkit.OperationInvoke:
			invokes = append(invokes, operation.BrowserID)
		case testkit.OperationDisconnect:
			disconnects = append(disconnects, operation.BrowserID)
		case testkit.OperationReplace:
			replacements = append(replacements, operation.BrowserID)
		}
	}
	if len(attaches) != 3 || attaches[0] != wantInvocations[0] || attaches[1] != wantInvocations[1] || attaches[2] != wantInvocations[3] {
		t.Fatalf("attach browser order = %v, want [%s %s %s]", attaches, wantInvocations[0], wantInvocations[1], wantInvocations[3])
	}
	if len(invokes) != len(wantInvocations) {
		t.Fatalf("invoke browser order = %v, want %v", invokes, wantInvocations)
	}
	for i := range wantInvocations {
		if invokes[i] != wantInvocations[i] {
			t.Fatalf("invoke browser order = %v, want %v", invokes, wantInvocations)
		}
	}
	if len(disconnects) != 2 || disconnects[0] != wantInvocations[0] || disconnects[1] != wantInvocations[1] {
		t.Fatalf("disconnect browser order = %v, want [%s %s]", disconnects, wantInvocations[0], wantInvocations[1])
	}
	if len(replacements) != 2 || replacements[0] != wantInvocations[1] || replacements[1] != wantInvocations[3] {
		t.Fatalf("replacement browser order = %v, want [%s %s]", replacements, wantInvocations[1], wantInvocations[3])
	}
}
