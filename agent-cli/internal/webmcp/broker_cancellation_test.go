package webmcp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerCancelsDispatchedWorkOnceAndIgnoresLateResults(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()

	dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if dispatched.State != webmcp.InvocationDispatched {
		t.Fatalf("dispatch result = %#v, want dispatched", dispatched)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}

	if err := broker.Cancel(context.Background(), webmcp.CancelRequest{InvocationID: dispatched.InvocationID, Reason: "user stopped"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait canceled invocation: %v", err)
	}
	assertCanceledResult(t, terminal, dispatched.InvocationID, "broker")

	// The fake deliberately accepts a late page response after acknowledging
	// cancellation. It must not reopen the registry or create another result.
	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`)); err != nil {
		t.Fatalf("late response: %v", err)
	}
	if _, err := broker.Selected(context.Background()); err != nil {
		t.Fatalf("flush late response: %v", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("second terminal delivery error = %v, want ErrInvocationNotFound", err)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("broker pending invocations = %#v, want empty", pending)
	}
	if pending := session.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("target pending invocations = %#v, want empty", pending)
	}

	cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel)
	if len(cancelOperations) != 1 || cancelOperations[0].InvocationID != dispatched.InvocationID || !cancelOperations[0].CancellationAcknowledged {
		t.Fatalf("cancel operations = %#v, want one acknowledged correlated request", cancelOperations)
	}
}

func TestStatefulBrokerDirectCancelUsesExactTargetWithoutLocalRegistry(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithContext(webmcp.PageContext{CatalogReady: true, CatalogEvidence: "test_fixture"}),
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	original, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()

	dispatched, err := original.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if dispatched.BrowserInvocationID == "" {
		t.Fatalf("dispatch result omitted browser invocation ID: %#v", dispatched)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}

	fresh := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               testkit.NewDeterministicIDs(),
		Clock:             clock,
		InvocationTimeout: 30 * time.Second,
	})
	t.Cleanup(func() { _ = fresh.Close() })
	if _, err := fresh.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("fresh select: %v", err)
	}
	if _, ok := fresh.Invocation(dispatched.InvocationID); ok {
		t.Fatalf("fresh broker unexpectedly inherited the original invocation registry")
	}

	wrongTargetErr := fresh.CancelDirect(context.Background(), webmcp.DirectCancelRequest{
		Target:       webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-b"},
		InvocationID: dispatched.BrowserInvocationID,
	})
	var classifiedErr *webmcp.ClassifiedError
	if !errors.As(wrongTargetErr, &classifiedErr) || classifiedErr.Code != webmcp.ErrorStaleSelection {
		t.Fatalf("wrong-target direct cancel error = %v, want stale selection", wrongTargetErr)
	}
	if pending := session.PendingInvocations(); len(pending) != 1 {
		t.Fatalf("wrong-target cancellation changed pending target work = %#v", pending)
	}

	if err := fresh.CancelDirect(context.Background(), webmcp.DirectCancelRequest{
		Target:       webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"},
		InvocationID: dispatched.BrowserInvocationID,
		Reason:       "operator stopped the pending call",
	}); err != nil {
		t.Fatalf("fresh direct cancel: %v", err)
	}
	if pending := session.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("target pending invocations after direct cancel = %#v", pending)
	}
	cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel)
	if len(cancelOperations) != 1 || cancelOperations[0].BrowserID != candidate.ID || cancelOperations[0].TargetID != "tab-a" || cancelOperations[0].InvocationID != dispatched.BrowserInvocationID || !cancelOperations[0].CancellationAcknowledged {
		t.Fatalf("direct cancel operations = %#v", cancelOperations)
	}
}

func TestStatefulBrokerCancelsQueuedWorkWithoutDispatch(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()
	first, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe first target invocation: %v", err)
	}

	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchContext)
	secondDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":2}`)})
		secondDone <- invocationCall{result: result, err: invokeErr}
	}()
	secondCreated := assertInvocationCreated(t, watch, ref)
	if err := broker.Cancel(context.Background(), webmcp.CancelRequest{InvocationID: secondCreated.InvocationID}); err != nil {
		t.Fatalf("cancel queued invocation: %v", err)
	}
	second := receiveInvocationCall(t, secondDone)
	if second.err != nil {
		t.Fatalf("queued invoke error = %v", second.err)
	}
	assertCanceledResult(t, second.result, secondCreated.InvocationID, "broker")
	if invocations := session.Invocations(); len(invocations) != 1 || invocations[0].ID != first.InvocationID {
		t.Fatalf("target invocations after queued cancel = %#v, want first only", invocations)
	}
	if cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel); len(cancelOperations) != 0 {
		t.Fatalf("cancel operations for queued work = %#v, want none", cancelOperations)
	}

	if err := session.ReleaseInvocation(first.InvocationID, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("release first invocation: %v", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), first.InvocationID); err != nil {
		t.Fatalf("wait first invocation: %v", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), secondCreated.InvocationID); err != nil {
		t.Fatalf("wait queued cancellation: %v", err)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after queued cancellation = %#v, want empty", pending)
	}
}

func TestStatefulBrokerContextCancellationLeavesUnknownMutationForReconciliation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()
	invokeContext, cancelInvoke := context.WithCancel(context.Background())
	dispatched, err := broker.Invoke(invokeContext, webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}
	cancelInvoke()
	if cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel); len(cancelOperations) != 0 {
		t.Fatalf("context cancellation operations before reconciliation = %#v, want none for unknown mutation", cancelOperations)
	}

	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"committed":true}`)); err != nil {
		t.Fatalf("release invocation: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait reconciled invocation: %v", err)
	}
	if terminal.State != webmcp.InvocationCompleted || string(terminal.Output) != `{"committed":true}` {
		t.Fatalf("reconciled result = %#v, want completed page output", terminal)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after context cancellation reconciliation = %#v, want empty", pending)
	}
}

func TestStatefulBrokerTimeoutsDispatchedWorkAndBoundsLateReconciliation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 5*time.Second)
	session.BlockInvocations()
	dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}

	clock.Advance(5 * time.Second)
	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait timed-out invocation: %v", err)
	}
	if terminal.State != webmcp.InvocationTimedOut || terminal.ErrorCode != string(webmcp.ErrorInvocationTimedOut) {
		t.Fatalf("timeout result = %#v, want invocation_timed_out", terminal)
	}
	if terminal.ErrorDetails["invocation_id"] != string(dispatched.InvocationID) || terminal.ErrorDetails["timeout_ms"] != int64(5000) || terminal.ErrorDetails["phase"] != "result" || terminal.ErrorDetails["side_effect_unknown"] != true {
		t.Fatalf("timeout details = %#v, want frozen safe details", terminal.ErrorDetails)
	}
	if cancelOperations := operationsOfKind(runtime.Operations(), testkit.OperationCancel); len(cancelOperations) != 1 || cancelOperations[0].InvocationID != dispatched.InvocationID {
		t.Fatalf("timeout cancel operations = %#v, want one correlated request", cancelOperations)
	}

	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`)); err != nil {
		t.Fatalf("late timeout response: %v", err)
	}
	if _, err := broker.Selected(context.Background()); err != nil {
		t.Fatalf("flush late timeout response: %v", err)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after timeout reconciliation = %#v, want empty", pending)
	}
}

func TestStatefulBrokerNavigationTerminalizesCurrentInvocation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()
	dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}
	if err := session.Navigate("https://fixture.test/next", "https://fixture.test"); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait navigation result: %v", err)
	}
	if terminal.State != webmcp.InvocationError || terminal.ErrorCode != string(webmcp.ErrorPageNavigated) {
		t.Fatalf("navigation result = %#v, want page_navigated", terminal)
	}
	if terminal.ErrorDetails["browser_id"] != string(candidate.ID) || terminal.ErrorDetails["target_id"] != "tab-a" || terminal.ErrorDetails["previous_generation"] != uint64(1) || terminal.ErrorDetails["current_generation"] != uint64(2) {
		t.Fatalf("navigation details = %#v, want generation transition", terminal.ErrorDetails)
	}
	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`)); err != nil {
		t.Fatalf("late navigation response: %v", err)
	}
	if _, err := broker.Selected(context.Background()); err != nil {
		t.Fatalf("flush late navigation response: %v", err)
	}
	if _, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
		t.Fatalf("second navigation result error = %v, want ErrInvocationNotFound", err)
	}
}

func TestStatefulBrokerSeparatesTargetClosureFromPageNavigation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-lifecycle", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-lifecycle", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()
	broker, session, oldRef := newRecoveryInvocationBroker(t, runtime, candidate, "tab-lifecycle")
	defer func() { _ = broker.Close() }()

	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchContext)

	if err := session.Navigate("https://fixture.test/next", "https://fixture.test"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("list after navigation: %v", err)
	}
	if snapshot.Generation != 2 || len(snapshot.Tools) != 0 || !snapshot.Context.Connected {
		t.Fatalf("post-navigation snapshot = %#v, want connected generation two with an empty catalog", snapshot)
	}
	navigation := nextRecoveryBrokerEvent(t, watch)
	if navigation.Type != webmcp.BrokerEventGenerationChanged || navigation.Generation != 2 {
		t.Fatalf("navigation broker event = %#v, want generation_changed at generation two", navigation)
	}

	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	if err := handle.CloseTarget(context.Background(), "tab-lifecycle"); err != nil {
		t.Fatalf("close target: %v", err)
	}
	closed := nextRecoveryBrokerEvent(t, watch)
	if closed.Type != webmcp.BrokerEventSessionClosed || closed.BrowserID != candidate.ID || closed.TargetID != "tab-lifecycle" || closed.Generation != 2 || closed.Reason != "target_closed" {
		t.Fatalf("target close broker event = %#v, want session_closed at the current generation", closed)
	}

	if _, err := broker.Selected(context.Background()); !isClassifiedCode(err, webmcp.ErrorStaleSelection) {
		t.Fatalf("selected after target close = %v, want stale_selection", err)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: oldRef, Input: []byte(`{}`)})
		return err
	}, webmcp.ErrorStaleSelection, "closed target selection")
	if operations := operationsOfKind(runtime.Operations(), testkit.OperationInvoke); len(operations) != 0 {
		t.Fatalf("post-close invoke operations = %#v, want none", operations)
	}
	targets, err := handle.ListTargets(context.Background())
	if err != nil || len(targets) != 0 || handle.IsDisconnected() {
		t.Fatalf("browser after target close = targets %#v, err %v, disconnected %v; want no target and live browser", targets, err, handle.IsDisconnected())
	}
}

func TestStatefulBrokerOrphansInvocationWhenTargetClosesBeforeReconciliation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-orphan", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-orphan", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()
	broker, _, ref := newRecoveryInvocationBroker(t, runtime, candidate, "tab-orphan")
	defer func() { _ = broker.Close() }()

	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime browser handle is nil")
	}
	handle.BlockListTargets()
	defer handle.UnblockListTargets()
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchContext)
	operationCursor := runtime.OperationCursor()
	invokeDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"value":1}`)})
		invokeDone <- invocationCall{result: result, err: invokeErr}
	}()
	created := assertInvocationCreated(t, watch, ref)
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), testkit.OperationListTargets, operationCursor); err != nil {
		t.Fatalf("wait blocked target check: %v", err)
	}

	if err := handle.CloseTarget(context.Background(), "tab-orphan"); err != nil {
		t.Fatalf("close target: %v", err)
	}
	handle.UnblockListTargets()

	call := receiveInvocationCall(t, invokeDone)
	if call.err == nil || call.result.InvocationID != created.InvocationID || call.result.State != webmcp.InvocationOrphaned || call.result.ErrorCode != string(webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("closed-target invocation = %#v, %v; want one invocation_orphaned result", call.result, call.err)
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(call.err, &classified) || classified.Code != webmcp.ErrorInvocationOrphaned {
		t.Fatalf("closed-target invocation error = %v, want invocation_orphaned", call.err)
	}
	if call.result.ErrorDetails["invocation_id"] != string(created.InvocationID) || call.result.ErrorDetails["target_id"] != "tab-orphan" || call.result.ErrorDetails["generation"] != uint64(1) || call.result.ErrorDetails["terminal_observed"] != false {
		t.Fatalf("closed-target invocation details = %#v, want bounded orphan metadata", call.result.ErrorDetails)
	}

	terminal := nextRecoveryBrokerEvent(t, watch)
	if terminal.Type != webmcp.BrokerEventInvocationTerminal || terminal.InvocationID != created.InvocationID || terminal.Reason != string(webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("orphan terminal event = %#v, want one correlated invocation terminal", terminal)
	}
	closed := nextRecoveryBrokerEvent(t, watch)
	if closed.Type != webmcp.BrokerEventSessionClosed || closed.TargetID != "tab-orphan" || closed.Reason != "target_closed" {
		t.Fatalf("orphan target lifecycle event = %#v, want session_closed after invocation reconciliation", closed)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after closed-target reconciliation = %#v, want empty", pending)
	}
	if operations := operationsOfKind(runtime.Operations(), testkit.OperationInvoke); len(operations) != 0 {
		t.Fatalf("closed-target invoke operations = %#v, want none", operations)
	}
}

func TestStatefulBrokerDetachAndDisconnectClassifyUnresolvedWork(t *testing.T) {
	tests := []struct {
		name       string
		terminate  func(*testkit.ScriptedTargetSession) error
		wantCode   webmcp.ErrorCode
		wantReason string
	}{
		{name: "detach", terminate: func(session *testkit.ScriptedTargetSession) error { return session.Detach("tab_closed") }, wantCode: webmcp.ErrorTargetDetached, wantReason: "tab_closed"},
		{name: "disconnect", terminate: func(session *testkit.ScriptedTargetSession) error { return session.Disconnect("transport_lost") }, wantCode: webmcp.ErrorBrowserDisconnected, wantReason: "transport_lost"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
			ids := testkit.NewDeterministicIDs()
			candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
			runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
				testkit.RuntimeOptions{Clock: clock, IDs: ids},
				testkit.BrowserConfig{
					Candidate: candidate,
					Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
						webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
						testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
					)},
				},
			)
			broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
			session.BlockInvocations()
			dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if _, err := session.WaitForInvocation(context.Background()); err != nil {
				t.Fatalf("observe target invocation: %v", err)
			}
			if err := testCase.terminate(session); err != nil {
				t.Fatalf("terminate session: %v", err)
			}

			terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
			if err != nil {
				t.Fatalf("wait lifecycle result: %v", err)
			}
			if terminal.State != webmcp.InvocationError || terminal.ErrorCode != string(testCase.wantCode) {
				t.Fatalf("lifecycle result = %#v, want %s", terminal, testCase.wantCode)
			}
			switch testCase.wantCode {
			case webmcp.ErrorTargetDetached:
				if terminal.ErrorDetails["browser_id"] != string(candidate.ID) || terminal.ErrorDetails["target_id"] != "tab-a" || terminal.ErrorDetails["generation"] != uint64(1) || terminal.ErrorDetails["reason"] != testCase.wantReason {
					t.Fatalf("detach details = %#v, want frozen safe details", terminal.ErrorDetails)
				}
			case webmcp.ErrorBrowserDisconnected:
				if terminal.ErrorDetails["browser_id"] != string(candidate.ID) || terminal.ErrorDetails["target_id"] != "tab-a" || terminal.ErrorDetails["phase"] != "lifecycle" || terminal.ErrorDetails["reconnect_required"] != true {
					t.Fatalf("disconnect details = %#v, want frozen safe details", terminal.ErrorDetails)
				}
			}
			if pending := broker.PendingInvocations(); len(pending) != 0 {
				t.Fatalf("pending after %s = %#v, want empty", testCase.name, pending)
			}
		})
	}
}

func TestStatefulBrokerKeepsBrowserDisconnectClassificationAfterSessionEnds(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	if err := session.Disconnect("browser_exit"); err != nil {
		t.Fatalf("disconnect session: %v", err)
	}

	assertDisconnected := func(label string, operation func() error) {
		t.Helper()
		err := operation()
		if err == nil {
			t.Fatalf("%s succeeded, want browser_disconnected", label)
		}
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
			t.Fatalf("%s error = %v (%T), want browser_disconnected", label, err, err)
		}
		if classified.Details["browser_id"] != string(candidate.ID) || classified.Details["target_id"] != "tab-a" || classified.Details["phase"] == "" || classified.Details["reconnect_required"] != true {
			t.Fatalf("%s details = %#v, want exact disconnected identity", label, classified.Details)
		}
	}

	assertDisconnected("selected context", func() error {
		_, err := broker.Selected(context.Background())
		return err
	})
	assertDisconnected("list tools", func() error {
		_, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
		return err
	})
	assertDisconnected("list targets", func() error {
		_, err := broker.ListTargets(context.Background(), webmcp.BrowserSelector{BrowserID: candidate.ID})
		return err
	})
	assertDisconnected("select exact target", func() error {
		_, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"})
		return err
	})
	assertDisconnected("invoke", func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{}`)})
		return err
	})
}

func TestStatefulBrokerCloseOrphansWorkAndIsIdempotent(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
			)},
		},
	)
	broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
	session.BlockInvocations()
	dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe target invocation: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- broker.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker close did not finish within the bounded test window")
	}
	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait orphaned invocation: %v", err)
	}
	if terminal.State != webmcp.InvocationOrphaned || terminal.ErrorCode != string(webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("close result = %#v, want invocation_orphaned", terminal)
	}
	if terminal.ErrorDetails["invocation_id"] != string(dispatched.InvocationID) || terminal.ErrorDetails["target_id"] != "tab-a" || terminal.ErrorDetails["generation"] != uint64(1) || terminal.ErrorDetails["terminal_observed"] != false {
		t.Fatalf("orphan details = %#v, want frozen safe details", terminal.ErrorDetails)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after close = %#v, want empty", pending)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestStatefulBrokerCloseBoundsNonCooperativeHandle(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	inner := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{Candidate: candidate},
	)
	runtime := &blockingCloseRuntime{
		inner:   inner,
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:      runtime,
		Discoverer:   staticDiscoverer{candidate},
		IDs:          ids,
		Clock:        clock,
		CloseTimeout: 20 * time.Millisecond,
	})
	if _, err := broker.ListTargets(context.Background(), webmcp.BrowserSelector{BrowserID: candidate.ID}); err != nil {
		t.Fatalf("list targets: %v", err)
	}

	closeErr := broker.Close()
	if !errors.Is(closeErr, webmcp.ErrCloseTimeout) {
		t.Fatalf("bounded close error = %v, want ErrCloseTimeout", closeErr)
	}
	select {
	case <-runtime.started:
	default:
		t.Fatal("non-cooperative handle was not asked to close")
	}
	if repeatedErr := broker.Close(); repeatedErr != closeErr {
		t.Fatalf("repeated close error = %v, want recorded error %v", repeatedErr, closeErr)
	}

	close(runtime.release)
	select {
	case <-runtime.done:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative handle did not finish after release")
	}
}

type blockingCloseRuntime struct {
	inner   *testkit.ScriptedBrowserRuntime
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (r *blockingCloseRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	handle, err := r.inner.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return &blockingCloseHandle{
		BrowserHandle: handle,
		started:       r.started,
		release:       r.release,
		done:          r.done,
	}, nil
}

type blockingCloseHandle struct {
	webmcp.BrowserHandle
	started chan struct{}
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (h *blockingCloseHandle) Close() error {
	h.once.Do(func() { close(h.started) })
	<-h.release
	err := h.BrowserHandle.Close()
	close(h.done)
	return err
}

func TestStatefulBrokerCloseLeavesExternalTargetsUsable(t *testing.T) {
	// A session-level SIGINT cleanup uses this same broker close boundary. An
	// externally owned browser must be detached, never closed, so the caller's
	// tabs remain usable after the agent process exits.
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	inner := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
					testkit.WithInitialCatalog(pageTool("read_a", "frame-a", `{}`)),
				),
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-b", Type: "page"},
					testkit.WithInitialCatalog(pageTool("read_b", "frame-b", `{}`)),
				),
			},
		},
	)
	runtime := &externalProbeRuntime{inner: inner}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
		IDs:        ids,
		Clock:      clock,
	})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select external target: %v", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("close external target: %v", err)
	}

	probeHandle, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open independent post-session probe: %v", err)
	}
	targets, err := probeHandle.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("list targets after session close: %v", err)
	}
	if len(targets) != 2 || targets[0].ID != "tab-a" || targets[1].ID != "tab-b" {
		t.Fatalf("post-session targets = %#v, want both external targets", targets)
	}
	for _, targetID := range []webmcp.TargetID{"tab-a", "tab-b"} {
		session, attachErr := probeHandle.Attach(context.Background(), targetID, webmcp.TargetOwnershipExternal)
		if attachErr != nil {
			t.Fatalf("attach post-session target %q: %v", targetID, attachErr)
		}
		if closeErr := session.Close(); closeErr != nil {
			t.Fatalf("detach post-session target %q: %v", targetID, closeErr)
		}
	}
	for _, operation := range inner.Operations() {
		if operation.Kind == testkit.OperationCloseTarget || operation.Kind == testkit.OperationCloseHandle {
			t.Fatalf("external session cleanup issued destructive operation: %#v", operation)
		}
	}
	if closeErr := inner.Close(); closeErr != nil {
		t.Fatalf("close scripted probe runtime: %v", closeErr)
	}
}

type externalProbeRuntime struct {
	inner *testkit.ScriptedBrowserRuntime
}

func (r *externalProbeRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	handle, err := r.inner.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return externalProbeHandle{BrowserHandle: handle}, nil
}

type externalProbeHandle struct {
	webmcp.BrowserHandle
}

func (externalProbeHandle) Close() error { return nil }

func TestStatefulBrokerCancelAndResultRaceHasOneTerminalTransition(t *testing.T) {
	for iteration := 0; iteration < 16; iteration++ {
		clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
		ids := testkit.NewDeterministicIDs()
		candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
		runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
			testkit.RuntimeOptions{Clock: clock, IDs: ids},
			testkit.BrowserConfig{
				Candidate: candidate,
				Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
					testkit.WithInitialCatalog(pageTool("write_state", "frame-1", `{}`)),
				)},
			},
		)
		broker, session, ref := newInvocationBroker(t, runtime, candidate, clock, ids, 30*time.Second)
		session.BlockInvocations()
		dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{"step":1}`)})
		if err != nil {
			t.Fatalf("iteration %d invoke: %v", iteration, err)
		}
		if _, err := session.WaitForInvocation(context.Background()); err != nil {
			t.Fatalf("iteration %d observe invocation: %v", iteration, err)
		}

		watchContext, cancelWatch := context.WithCancel(context.Background())
		watch := broker.Watch(watchContext)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_ = broker.Cancel(context.Background(), webmcp.CancelRequest{InvocationID: dispatched.InvocationID})
		}()
		go func() {
			defer wait.Done()
			<-start
			_ = session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`))
		}()
		close(start)
		wait.Wait()

		select {
		case event := <-watch:
			if event.Type != webmcp.BrokerEventInvocationTerminal || event.InvocationID != dispatched.InvocationID {
				t.Fatalf("iteration %d terminal event = %#v", iteration, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d timed out waiting for terminal event", iteration)
		}
		cancelWatch()
		terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
		if err != nil {
			t.Fatalf("iteration %d wait terminal: %v", iteration, err)
		}
		if terminal.State != webmcp.InvocationCanceled && terminal.State != webmcp.InvocationCompleted {
			t.Fatalf("iteration %d terminal = %#v, want canceled or completed", iteration, terminal)
		}
		if _, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID); !errors.Is(err, webmcp.ErrInvocationNotFound) {
			t.Fatalf("iteration %d second delivery error = %v, want ErrInvocationNotFound", iteration, err)
		}
		if pending := broker.PendingInvocations(); len(pending) != 0 {
			t.Fatalf("iteration %d pending = %#v, want empty", iteration, pending)
		}
		if err := broker.Close(); err != nil {
			t.Fatalf("iteration %d close: %v", iteration, err)
		}
	}
}

func newInvocationBroker(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, candidate webmcp.BrowserCandidate, clock *testkit.FakeClock, ids *testkit.DeterministicIDs, timeout time.Duration) (*webmcp.StatefulBroker, *testkit.ScriptedTargetSession, webmcp.ToolRef) {
	t.Helper()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: timeout,
	})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		broker.Close()
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		broker.Close()
		t.Fatalf("list tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		broker.Close()
		t.Fatalf("tools = %#v, want one page tool", snapshot.Tools)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		broker.Close()
		t.Fatalf("open fixture handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession("tab-a")
	if session == nil {
		broker.Close()
		t.Fatal("fixture session is nil")
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, session, snapshot.Tools[0].Ref
}

func assertCanceledResult(t *testing.T, result webmcp.InvokeResult, id webmcp.InvocationID, source string) {
	t.Helper()
	if result.InvocationID != id || result.State != webmcp.InvocationCanceled || result.ErrorCode != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("canceled result = %#v, want correlated invocation_canceled", result)
	}
	if result.Output != nil || result.ErrorDetails["invocation_id"] != string(id) || result.ErrorDetails["cancel_source"] != source {
		t.Fatalf("canceled details = %#v, output = %s, want frozen safe details", result.ErrorDetails, result.Output)
	}
}

func operationsOfKind(operations []testkit.Operation, kind testkit.OperationKind) []testkit.Operation {
	result := make([]testkit.Operation, 0)
	for _, operation := range operations {
		if operation.Kind == kind {
			result = append(result, operation)
		}
	}
	return result
}
