package testkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestTopologyChurnDisconnectsBlockedEnableAtDeterministicBoundaries(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture"}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: candidate,
		Targets: []TargetConfig{NewTargetConfig(
			webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Generation: 1},
		)},
	})
	defer func() { _ = runtime.Close() }()

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	handle := handleValue.(*ScriptedBrowserHandle)
	sessionValue, err := handle.Attach(context.Background(), "tab-a", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach target: %v", err)
	}
	session := sessionValue.(*ScriptedTargetSession)
	attached := waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached
	})
	if attached.Event.BrowserID != candidate.ID || attached.Event.TargetID != "tab-a" || attached.Event.Generation != 1 || attached.Event.Sequence != 1 {
		t.Fatalf("attached publication = %#v, want producing browser/target/generation/sequence", attached)
	}

	session.BlockEnableWebMCP()
	operationCursor := runtime.OperationCursor()
	enableDone := make(chan error, 1)
	go func() { enableDone <- session.EnableWebMCP(context.Background()) }()
	if operation, err := runtime.WaitForOperationAdmitted(testContext(t), OperationEnableWebMCP, operationCursor); err != nil {
		t.Fatalf("wait enable admission: %v", err)
	} else if operation.BrowserID != candidate.ID || operation.TargetID != "tab-a" || operation.Generation != 1 {
		t.Fatalf("enable operation = %#v, want target identity and generation", operation)
	}

	eventCursor := runtime.EventCursor()
	if err := handle.Disconnect("transport_lost"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}
	select {
	case err := <-enableDone:
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
			t.Fatalf("blocked enable error = %v, want browser_disconnected", err)
		}
	case <-testContext(t).Done():
		t.Fatal("blocked enable did not unblock after browser disconnect")
	}

	disconnected := waitPublishedEventAfter(t, runtime, eventCursor, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventBrowserDisconnected
	})
	if disconnected.Event.BrowserID != candidate.ID || disconnected.Event.TargetID != "tab-a" || disconnected.Event.Generation != 1 || disconnected.Event.Reason != "transport_lost" {
		t.Fatalf("disconnect publication = %#v, want bounded source identity", disconnected)
	}
	if session.Err() == nil {
		t.Fatal("session did not retain a terminal transport error")
	}
	select {
	case <-session.Done():
	case <-testContext(t).Done():
		t.Fatal("session Done did not close after disconnect")
	}
	if err := handle.Disconnect("duplicate"); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
	if countOperations(runtime.Operations(), OperationDisconnect) != 1 {
		t.Fatalf("disconnect operations = %#v, want one", runtime.Operations())
	}
}

func TestTopologyStageGatesCanBeReleased(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture"}
	target := webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Generation: 1}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: candidate,
		Targets:   []TargetConfig{NewTargetConfig(target)},
	})
	defer func() { _ = runtime.Close() }()
	handleTemplate := runtime.Browser(candidate.ID)
	if handleTemplate == nil {
		t.Fatal("scripted browser handle is nil")
	}

	handleTemplate.BlockOpen()
	openDone := make(chan struct {
		handle webmcp.BrowserHandle
		err    error
	}, 1)
	go func() {
		handle, err := runtime.Open(context.Background(), candidate)
		openDone <- struct {
			handle webmcp.BrowserHandle
			err    error
		}{handle: handle, err: err}
	}()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), OperationOpen); err != nil {
		t.Fatalf("wait for open admission: %v", err)
	}
	handleTemplate.UnblockOpen()
	opened := <-openDone
	if opened.err != nil {
		t.Fatalf("release open gate: %v", opened.err)
	}
	handle := opened.handle.(*ScriptedBrowserHandle)

	handle.BlockActivate()
	activateDone := make(chan error, 1)
	activateCursor := runtime.OperationCursor()
	go func() { activateDone <- handle.Activate(context.Background(), target.ID) }()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), OperationActivate, activateCursor); err != nil {
		t.Fatalf("wait for activate admission: %v", err)
	}
	handle.UnblockActivate()
	if err := <-activateDone; err != nil {
		t.Fatalf("release activate gate: %v", err)
	}

	handle.BlockAttach()
	attachDone := make(chan struct {
		session webmcp.TargetSession
		err     error
	}, 1)
	attachCursor := runtime.OperationCursor()
	go func() {
		session, err := handle.Attach(context.Background(), target.ID, webmcp.TargetOwnershipExternal)
		attachDone <- struct {
			session webmcp.TargetSession
			err     error
		}{session: session, err: err}
	}()
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), OperationAttach, attachCursor); err != nil {
		t.Fatalf("wait for attach admission: %v", err)
	}
	handle.UnblockAttach()
	attached := <-attachDone
	if attached.err != nil {
		t.Fatalf("release attach gate: %v", attached.err)
	}
	if attached.session == nil {
		t.Fatal("attach gate returned nil session")
	}
	if err := attached.session.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	if _, err := runtime.WaitForOperationAdmitted(testContext(t), OperationEnableAcknowledged); err != nil {
		t.Fatalf("wait for enable acknowledgement: %v", err)
	}
}

func TestTopologyChurnSupportsBlockedInvocationTargetCloseAndTerminalBarrier(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture"}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: candidate,
		Targets: []TargetConfig{
			NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"}),
			NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-b", Type: "page"}),
		},
	})
	defer func() { _ = runtime.Close() }()
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	handle := handleValue.(*ScriptedBrowserHandle)
	firstValue, err := handle.Attach(context.Background(), "tab-a", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach first target: %v", err)
	}
	first := firstValue.(*ScriptedTargetSession)
	secondValue, err := handle.Attach(context.Background(), "tab-b", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach second target: %v", err)
	}
	second := secondValue.(*ScriptedTargetSession)
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.TargetID == "tab-a"
	})
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.TargetID == "tab-b"
	})

	first.BlockInvocations()
	invocation, err := first.InvokeWebMCP(context.Background(), "frame-1", "write_state", []byte(`{"value":1}`))
	if err != nil {
		t.Fatalf("admit blocked invocation: %v", err)
	}
	if _, err := first.WaitForInvocationAdmission(testContext(t)); err != nil {
		t.Fatalf("wait invocation admission: %v", err)
	}
	if record, ok := first.Invocation(invocation); !ok || record.Generation != 1 || record.State != webmcp.InvocationDispatched || record.Terminal {
		t.Fatalf("blocked invocation = %#v, present=%v", record, ok)
	}

	eventCursor := runtime.EventCursor()
	if err := handle.Disconnect("browser_exit"); err != nil {
		t.Fatalf("disconnect browser: %v", err)
	}
	terminal, err := first.WaitForTerminalObservation(testContext(t), invocation)
	if err != nil {
		t.Fatalf("wait disconnected terminal: %v", err)
	}
	if terminal.Invocation.State != webmcp.InvocationOrphaned || terminal.Event.Type != webmcp.EventBrowserDisconnected || terminal.Event.BrowserID != candidate.ID || terminal.Event.TargetID != "tab-a" {
		t.Fatalf("terminal observation = %#v, want one orphaned browser event", terminal)
	}
	if terminal.PublicationSequence <= eventCursor {
		t.Fatalf("terminal publication = %#v, want after cursor %d", terminal, eventCursor)
	}
	if observations := first.TerminalObservations(); len(observations) != 1 {
		t.Fatalf("terminal observations = %#v, want exactly one", observations)
	}
	if pending := first.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after disconnect = %#v, want none", pending)
	}
	_ = second
}

func TestTopologyChurnClosesOneTargetWithoutDisconnectingBrowser(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture"}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: candidate,
		Targets: []TargetConfig{
			NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-external", Type: "page"}),
			NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-keep", Type: "page"}),
		},
	})
	defer func() { _ = runtime.Close() }()
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	handle := handleValue.(*ScriptedBrowserHandle)
	_, err = handle.Attach(context.Background(), "tab-external", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach external target: %v", err)
	}
	keepValue, err := handle.Attach(context.Background(), "tab-keep", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach retained target: %v", err)
	}
	keep := keepValue.(*ScriptedTargetSession)
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.TargetID == "tab-external"
	})
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.TargetID == "tab-keep"
	})

	oldCursor := runtime.EventCursor()
	if err := handle.CloseTarget(testContext(t), "tab-external"); err != nil {
		t.Fatalf("close external target: %v", err)
	}
	closedEvent := waitPublishedEventAfter(t, runtime, oldCursor, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetDetached && event.TargetID == "tab-external"
	})
	if closedEvent.Event.Reason != "target_closed" || closedEvent.Event.ErrorCode != string(webmcp.ErrorTargetDetached) {
		t.Fatalf("closed target event = %#v, want target_detached target_closed", closedEvent)
	}
	targets, err := handle.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("list after target close: %v", err)
	}
	if len(targets) != 1 || targets[0].ID != "tab-keep" || handle.IsDisconnected() {
		t.Fatalf("targets after close = %#v, disconnected=%v, want retained browser target", targets, handle.IsDisconnected())
	}
	if countOperations(runtime.Operations(), OperationDisconnect) != 0 {
		t.Fatalf("disconnect operations = %#v, want none", runtime.Operations())
	}
	if countOperationsForTarget(runtime.Operations(), OperationDetach, "tab-external") != 0 {
		t.Fatalf("external close emitted detach cleanup = %#v", runtime.Operations())
	}
	if countOperationsForTarget(runtime.Operations(), OperationCloseTarget, "tab-external") != 1 {
		t.Fatalf("target close operations = %#v, want one", runtime.Operations())
	}
	if err := keep.Close(); err != nil {
		t.Fatalf("detach retained external target: %v", err)
	}
}

func TestTopologyChurnPublishesTargetCloseWhenSessionBufferIsFull(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-buffer", Product: "fixture"}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: candidate,
		Targets: []TargetConfig{NewTargetConfig(
			webmcp.Target{BrowserID: candidate.ID, ID: "tab-buffer", Type: "page"},
			WithEventBuffer(1),
		)},
	})
	defer func() { _ = runtime.Close() }()

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	handle := handleValue.(*ScriptedBrowserHandle)
	sessionValue, err := handle.Attach(context.Background(), "tab-buffer", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach target: %v", err)
	}
	session := sessionValue.(*ScriptedTargetSession)
	if err := handle.CloseTarget(context.Background(), "tab-buffer"); err != nil {
		t.Fatalf("close full-buffer target: %v", err)
	}
	terminal, ok := <-session.Events()
	if !ok || terminal.Type != webmcp.EventTargetDetached || terminal.Reason != "target_closed" || terminal.ErrorCode != string(webmcp.ErrorTargetDetached) {
		t.Fatalf("full-buffer terminal event = %#v, open=%v; want target_closed target_detached", terminal, ok)
	}
	if _, ok := <-session.Events(); ok {
		t.Fatal("session emitted an event after target-close terminal event")
	}
	select {
	case <-session.Done():
	case <-testContext(t).Done():
		t.Fatal("target-close Done channel did not close")
	}
}

func TestTopologyChurnReplacesIdentityPreservesLateSourceAndEmitsNavigationBurst(t *testing.T) {
	oldCandidate := webmcp.BrowserCandidate{
		ID:           "browser-old",
		HTTPURL:      "http://127.0.0.1:9222",
		BrowserWSURL: "ws://127.0.0.1:9222/devtools/browser/old",
		Product:      "Chrome/old",
	}
	target := webmcp.Target{BrowserID: oldCandidate.ID, ID: "tab-reused", Type: "page", Title: "Same title", URL: "https://fixture.test/", Origin: "https://fixture.test"}
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: oldCandidate,
		Targets:   []TargetConfig{NewTargetConfig(target)},
	})
	defer func() { _ = runtime.Close() }()
	oldHandleValue, err := runtime.Open(context.Background(), oldCandidate)
	if err != nil {
		t.Fatalf("open old browser: %v", err)
	}
	oldHandle := oldHandleValue.(*ScriptedBrowserHandle)
	oldSessionValue, err := oldHandle.Attach(context.Background(), target.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach old target: %v", err)
	}
	oldSession := oldSessionValue.(*ScriptedTargetSession)
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.BrowserID == oldCandidate.ID
	})
	oldContext := oldSession.Context()
	if err := oldHandle.Disconnect("old_browser_exit"); err != nil {
		t.Fatalf("disconnect old browser: %v", err)
	}

	newCandidate := webmcp.BrowserCandidate{
		ID:           "browser-new",
		HTTPURL:      oldCandidate.HTTPURL,
		BrowserWSURL: "ws://127.0.0.1:9222/devtools/browser/new",
		Product:      "Chrome/new",
	}
	newTarget := target
	newTarget.BrowserID = newCandidate.ID
	replacementTemplate, err := runtime.ReplaceEndpoint(oldCandidate, newCandidate, NewTargetConfig(newTarget, WithEventBuffer(16)))
	if err != nil {
		t.Fatalf("replace endpoint: %v", err)
	}
	if got := oldHandle.Candidate(); got.ID != oldCandidate.ID || got.Product != oldCandidate.Product {
		t.Fatalf("retired candidate = %#v, want unchanged old identity", got)
	}
	if got := oldSession.Context(); got.Key != oldContext.Key || got.Generation != oldContext.Generation || got.Title != oldContext.Title || got.URL != oldContext.URL || got.Origin != oldContext.Origin {
		t.Fatalf("retired session identity context = %#v, want unchanged identity fields from %#v", got, oldContext)
	}
	if _, err := runtime.Open(context.Background(), oldCandidate); err == nil {
		t.Fatal("open retired browser succeeded, want browser disconnected classification")
	} else {
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
			t.Fatalf("open retired browser error = %v, want browser disconnected classification", err)
		}
	}

	newHandleValue, err := runtime.Open(context.Background(), newCandidate)
	if err != nil {
		t.Fatalf("open replacement browser: %v", err)
	}
	newHandle, ok := newHandleValue.(*ScriptedBrowserHandle)
	if !ok {
		t.Fatalf("replacement open returned %T, want scripted browser handle", newHandleValue)
	}
	if newHandle == replacementTemplate {
		t.Fatal("replacement open returned the runtime template, want an independent client handle")
	}
	newSessionValue, err := newHandle.Attach(context.Background(), newTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach replacement target: %v", err)
	}
	newSession := newSessionValue.(*ScriptedTargetSession)
	waitPublishedEvent(t, runtime, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached && event.BrowserID == newCandidate.ID
	})

	lateCursor := runtime.EventCursor()
	if err := oldSession.InjectLateEvent(webmcp.BrowserEvent{
		Type:       webmcp.EventToolsAdded,
		Generation: oldContext.Generation,
		Tools:      []webmcp.ToolDescriptor{{Name: "old_tool", FrameID: "frame-old"}},
	}); err != nil {
		t.Fatalf("inject late old-session event: %v", err)
	}
	late := waitPublishedEventAfter(t, runtime, lateCursor, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolsAdded && event.ToolName == ""
	})
	if late.Event.BrowserID != oldCandidate.ID || late.Event.TargetID != target.ID || late.Event.Generation != oldContext.Generation || late.Event.Sequence <= 2 {
		t.Fatalf("late event = %#v, want old source identity/generation/sequence", late)
	}
	if len(newSession.Catalog()) != 0 {
		t.Fatalf("replacement catalog after late event = %#v, want unchanged empty fake catalog", newSession.Catalog())
	}

	previousSequence := uint64(0)
	previousGeneration := oldContext.Generation
	for _, step := range []Navigation{{URL: "https://fixture.test/one", Origin: "https://fixture.test"}, {URL: "https://fixture.test/two", Origin: "https://fixture.test"}, {URL: "https://fixture.test/three", Origin: "https://fixture.test"}} {
		cursor := runtime.EventCursor()
		if err := newSession.NavigateSequence(step); err != nil {
			t.Fatalf("navigate burst step: %v", err)
		}
		navigation := waitPublishedEventAfter(t, runtime, cursor, func(event webmcp.BrowserEvent) bool {
			return event.Type == webmcp.EventPageNavigated && event.BrowserID == newCandidate.ID
		})
		if navigation.Event.Generation <= previousGeneration || navigation.Event.PreviousGeneration != previousGeneration || navigation.Event.Sequence <= previousSequence || navigation.Event.TargetID != newTarget.ID {
			t.Fatalf("navigation event = %#v, previous generation=%d sequence=%d", navigation, previousGeneration, previousSequence)
		}
		previousGeneration = navigation.Event.Generation
		previousSequence = navigation.Event.Sequence
	}
	if got := newSession.Context().Generation; got != 4 {
		t.Fatalf("replacement generation = %d, want three monotonic navigations from one", got)
	}
	if countOperations(runtime.Operations(), OperationReplace) != 1 {
		t.Fatalf("replace operations = %#v, want one", runtime.Operations())
	}
}

func waitPublishedEvent(t *testing.T, runtime *ScriptedBrowserRuntime, after uint64, match EventMatcher) PublishedEvent {
	t.Helper()
	return waitPublishedEventAfter(t, runtime, after, match)
}

func waitPublishedEventAfter(t *testing.T, runtime *ScriptedBrowserRuntime, after uint64, match EventMatcher) PublishedEvent {
	t.Helper()
	publication, err := runtime.WaitForPublishedEvent(testContext(t), after, match)
	if err != nil {
		t.Fatalf("wait published event after %d: %v", after, err)
	}
	return publication
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func countOperations(operations []Operation, kind OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

func countOperationsForTarget(operations []Operation, kind OperationKind, targetID webmcp.TargetID) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind && operation.TargetID == targetID {
			count++
		}
	}
	return count
}
