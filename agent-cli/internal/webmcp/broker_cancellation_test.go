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
