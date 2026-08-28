package testkit

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestScriptedRuntimeModelsTargetsCatalogInvocationsAndOwnership(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	readTool := webmcp.ToolDescriptor{
		Name:        "read_state",
		Description: "Read fixture state",
		InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		FrameID:     "frame-1",
	}
	runtime := NewScriptedBrowserRuntimeWithOptions(
		RuntimeOptions{Clock: clock},
		BrowserConfig{
			Candidate: candidate,
			Targets: []TargetConfig{
				NewTargetConfig(webmcp.Target{ID: "tab-a", Type: "page", Title: "A", URL: "https://a.test/"}, WithInitialCatalog(readTool)),
				NewTargetConfig(webmcp.Target{ID: "tab-b", Type: "page", Title: "B", URL: "https://b.test/"}, WithAutoResponse([]byte(`{"ok":true}`))),
			},
		},
	)

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	targets, err := handleValue.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 2 || targets[0].ID != "tab-a" || targets[1].ID != "tab-b" {
		t.Fatalf("targets = %#v, want deterministic tab-a/tab-b order", targets)
	}

	sessionAValue, err := handleValue.Attach(context.Background(), "tab-a", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach tab-a: %v", err)
	}
	sessionBValue, err := handleValue.Attach(context.Background(), "tab-b", webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach tab-b: %v", err)
	}
	sessionA := sessionAValue.(*ScriptedTargetSession)
	sessionB := sessionBValue.(*ScriptedTargetSession)

	if event := nextEvent(t, sessionA.Events()); event.Type != webmcp.EventTargetAttached {
		t.Fatalf("first tab-a event = %q, want target_attached", event.Type)
	}
	if err := sessionA.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable tab-a: %v", err)
	}
	if event := nextEvent(t, sessionA.Events()); event.Type != webmcp.EventToolsAdded || len(event.Tools) != 1 {
		t.Fatalf("catalog event = %#v, want one tools_added descriptor", event)
	}
	if event := nextEvent(t, sessionB.Events()); event.Type != webmcp.EventTargetAttached {
		t.Fatalf("first tab-b event = %q, want target_attached", event.Type)
	}

	sessionA.BlockInvocations()
	largeNumber := []byte(`{"count":90071992547409931234567890}`)
	invoA, err := sessionA.InvokeWebMCP(context.Background(), "frame-1", "read_state", largeNumber)
	if err != nil {
		t.Fatalf("invoke tab-a: %v", err)
	}
	invocationA, err := sessionA.WaitForInvocation(context.Background())
	if err != nil {
		t.Fatalf("observe tab-a invocation: %v", err)
	}
	if string(invocationA.Input) != string(largeNumber) {
		t.Fatalf("input = %s, want byte-preserved number token", invocationA.Input)
	}
	if event := nextEvent(t, sessionA.Events()); event.Type != webmcp.EventToolInvoked || event.InvocationID != invoA {
		t.Fatalf("invocation event = %#v", event)
	}

	// The independent target can complete while tab-a remains blocked.
	invoB, err := sessionB.InvokeWebMCP(context.Background(), "frame-2", "read_state", []byte(`{}`))
	if err != nil {
		t.Fatalf("invoke tab-b: %v", err)
	}
	if event := nextEvent(t, sessionB.Events()); event.Type != webmcp.EventToolInvoked || event.InvocationID != invoB {
		t.Fatalf("tab-b invocation event = %#v", event)
	}
	if event := nextEvent(t, sessionB.Events()); event.Type != webmcp.EventToolResponded || string(event.Output) != `{"ok":true}` {
		t.Fatalf("tab-b response event = %#v", event)
	}
	if len(sessionA.PendingInvocations()) != 1 {
		t.Fatalf("tab-a pending invocations = %d, want one while blocked", len(sessionA.PendingInvocations()))
	}

	if err := sessionA.CancelWebMCP(context.Background(), invoA); err != nil {
		t.Fatalf("cancel tab-a: %v", err)
	}
	if event := nextEvent(t, sessionA.Events()); event.Type != webmcp.EventToolResponded || event.Status != "Canceled" {
		t.Fatalf("cancel response event = %#v", event)
	}
	state, ok := sessionA.Invocation(invoA)
	if !ok || !state.CancellationAcknowledged || !state.Terminal {
		t.Fatalf("canceled invocation = %#v, want acknowledged terminal state", state)
	}
	if len(sessionA.PendingInvocations()) != 0 {
		t.Fatalf("pending after acknowledged cancellation = %#v", sessionA.PendingInvocations())
	}

	// A late page response is still emitted so the broker can exercise its
	// exactly-once reconciliation path.
	if err := sessionA.ReleaseInvocation(invoA, []byte(`{"late":true}`)); err != nil {
		t.Fatalf("release late tab-a response: %v", err)
	}
	if event := nextEvent(t, sessionA.Events()); event.Type != webmcp.EventToolResponded || string(event.Output) != `{"late":true}` {
		t.Fatalf("late response event = %#v", event)
	}

	if err := sessionA.Close(); err != nil {
		t.Fatalf("close external tab-a session: %v", err)
	}
	remaining, err := handleValue.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("list targets after external detach: %v", err)
	}
	if len(remaining) != 2 || remaining[0].Attached || remaining[1].ID != "tab-b" || !remaining[1].Attached {
		t.Fatalf("targets after external detach = %#v, want tab-a preserved and detached", remaining)
	}
	if err := sessionB.Close(); err != nil {
		t.Fatalf("close harness-owned tab-b session: %v", err)
	}
	remaining, err = handleValue.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("list targets after owned close: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "tab-a" {
		t.Fatalf("targets after harness-owned close = %#v, want only external tab-a", remaining)
	}

	if err := handleValue.Close(); err != nil {
		t.Fatalf("close browser handle: %v", err)
	}
	if err := handleValue.Close(); err != nil {
		t.Fatalf("idempotent browser handle close: %v", err)
	}

	ops := runtime.Operations()
	assertOperationOrder(t, ops, OperationOpen, OperationListTargets, OperationAttach, OperationAttach, OperationEnableWebMCP, OperationInvoke, OperationInvoke, OperationCancel, OperationDetach, OperationCloseTarget, OperationListTargets)
	if len(ops) == 0 || ops[0].Sequence != 1 {
		t.Fatalf("operation sequence starts at %#v, want one", ops)
	}
	for i, operation := range ops {
		if operation.Sequence != uint64(i+1) {
			t.Fatalf("operation %d sequence = %d, want %d", i, operation.Sequence, i+1)
		}
	}
}

func TestScriptedSessionCloseOrphansBlockedWorkAndWakesWaiters(t *testing.T) {
	runtime := NewScriptedBrowserRuntime(BrowserConfig{
		Candidate: webmcp.BrowserCandidate{ID: "browser-a"},
		Targets:   []TargetConfig{NewTargetConfig(webmcp.Target{ID: "tab-a", Type: "page"})},
	})
	handleValue, err := runtime.Open(context.Background(), webmcp.BrowserCandidate{ID: "browser-a"})
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}
	sessionValue, err := handleValue.Attach(context.Background(), "tab-a", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach target: %v", err)
	}
	session := sessionValue.(*ScriptedTargetSession)
	session.BlockInvocations()
	if _, err := session.InvokeWebMCP(context.Background(), "frame-1", "write_state", []byte(`{}`)); err != nil {
		t.Fatalf("invoke blocked work: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe blocked work: %v", err)
	}

	waitResult := make(chan error, 1)
	go func() {
		_, waitErr := session.WaitForInvocation(context.Background())
		waitResult <- waitErr
	}()
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-waitResult:
		if !errors.Is(err, webmcp.ErrClosed) {
			t.Fatalf("waiter error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked waiter was not released by close")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("session Done channel was not closed")
	}
	if pending := session.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending after close = %#v, want none", pending)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second session close: %v", err)
	}
}

func TestFakeClockAdvancesTimersWithoutHostSleeps(t *testing.T) {
	base := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(base)
	first := clock.NewTimerHandle(5 * time.Second)
	second := clock.NewTimerHandle(5 * time.Second)
	clock.Advance(4 * time.Second)
	assertTimerNotReady(t, first)
	assertTimerNotReady(t, second)
	clock.Advance(time.Second)
	if got := <-first.C(); !got.Equal(base.Add(5 * time.Second)) {
		t.Fatalf("first timer timestamp = %s", got)
	}
	if got := <-second.C(); !got.Equal(base.Add(5 * time.Second)) {
		t.Fatalf("second timer timestamp = %s", got)
	}
	if first.Reset(2 * time.Second) {
		t.Fatal("reset of an expired timer reported active")
	}
	clock.Advance(2 * time.Second)
	if got := <-first.C(); !got.Equal(base.Add(7 * time.Second)) {
		t.Fatalf("reset timer timestamp = %s", got)
	}
}

func TestSharedClockOptionConfiguresRecorderAndTargetSession(t *testing.T) {
	clock := NewFakeClock(123)
	config := NewTargetConfig(webmcp.Target{ID: "tab-1"}, WithClock(clock))
	if config.Session.Clock == nil || !config.Session.Clock.Now().Equal(clock.Now()) {
		t.Fatalf("shared clock option = %#v, want injected clock", config.Session.Clock)
	}

	explicit := NewTargetConfig(webmcp.Target{ID: "tab-2"}, WithSessionClock(clock))
	if explicit.Session.Clock == nil || !explicit.Session.Clock.Now().Equal(clock.Now()) {
		t.Fatalf("explicit session clock option = %#v, want injected clock", explicit.Session.Clock)
	}

	var output bytes.Buffer
	recorder, err := NewRecorder(&output, WithClock(clock))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := recorder.Record(EventInput{
		Type:    EventBrowserDiscoveryStarted,
		Payload: MustJSONValue(map[string]any{"source": "shared-clock"}),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"monotonic_ms":123`)) {
		t.Fatalf("recorded event = %s, want shared clock timestamp", output.Bytes())
	}
}

func TestTargetSessionOptionsPreserveConfiguredRuntimeSeams(t *testing.T) {
	clock := NewFakeClock(1)
	ids := NewDeterministicIDs()
	enableErr := errors.New("enable failed")
	invokeErr := errors.New("invoke failed")
	cancelErr := errors.New("cancel failed")
	page := webmcp.PageContext{Title: "fixture page"}
	tool := webmcp.ToolDescriptor{Name: "read_state"}
	config := NewTargetConfig(
		webmcp.Target{ID: "tab-1"},
		WithEventBuffer(3),
		WithContext(page),
		WithEnableEvents(webmcp.BrowserEvent{Type: webmcp.EventToolsAdded}),
		WithInitialCatalog(tool),
		WithAutoResponseStatus("Queued", []byte(`{"ok":true}`)),
		WithEnableError(enableErr),
		WithInvokeError(invokeErr),
		WithCancelError(cancelErr),
		WithCancellationAcknowledgement(false),
		WithCancellationResponse(false),
		WithIDs(ids),
		WithClock(clock),
	)
	options := config.Session
	if options.EventBuffer != 3 || options.Context.Title != page.Title || len(options.EnableEvents) != 1 {
		t.Fatalf("basic session options = %+v", options)
	}
	if len(options.InitialCatalog) != 1 || options.InitialCatalog[0].Name != tool.Name {
		t.Fatalf("catalog option = %+v", options.InitialCatalog)
	}
	if options.AutoResponseStatus != "Queued" || string(options.AutoResponseOutput) != `{"ok":true}` {
		t.Fatalf("response options = %+v", options)
	}
	if options.EnableError != enableErr || options.InvokeError != invokeErr || options.CancelError != cancelErr {
		t.Fatalf("failure options = %+v", options)
	}
	if options.AcknowledgeCancellation == nil || *options.AcknowledgeCancellation || options.EmitCancellationResponse == nil || *options.EmitCancellationResponse {
		t.Fatalf("cancellation options = %+v", options)
	}
	if options.IDs != ids || options.Clock != clock {
		t.Fatalf("injected seams = ids:%v clock:%v", options.IDs, options.Clock)
	}
}

func TestDeterministicIDsAreValidAndReproducible(t *testing.T) {
	pattern := regexp.MustCompile(`^webmcp\.tool-ref\.v1:[A-Za-z0-9_-]{22}$`)
	left := NewDeterministicIDs()
	right := NewDeterministicIDs()
	for i := 0; i < 3; i++ {
		leftRef, err := left.NewToolRef()
		if err != nil {
			t.Fatalf("left tool ref: %v", err)
		}
		rightRef, err := right.NewToolRef()
		if err != nil {
			t.Fatalf("right tool ref: %v", err)
		}
		if leftRef != rightRef || !pattern.MatchString(string(leftRef)) {
			t.Fatalf("refs = %q and %q, want equal valid refs", leftRef, rightRef)
		}
	}
	leftInvocation, err := left.NewInvocationID()
	if err != nil {
		t.Fatalf("left invocation: %v", err)
	}
	rightInvocation, err := right.NewInvocationID()
	if err != nil {
		t.Fatalf("right invocation: %v", err)
	}
	if leftInvocation != rightInvocation || leftInvocation != "inv-000001" {
		t.Fatalf("invocation IDs = %q and %q, want reproducible inv-000001", leftInvocation, rightInvocation)
	}
}

func nextEvent(t *testing.T, events <-chan webmcp.BrowserEvent) webmcp.BrowserEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed before expected event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fake browser event")
		return webmcp.BrowserEvent{}
	}
}

func assertTimerNotReady(t *testing.T, timer *FakeTimer) {
	t.Helper()
	select {
	case value := <-timer.C():
		t.Fatalf("timer fired early at %s", value)
	default:
	}
}

func assertOperationOrder(t *testing.T, operations []Operation, wanted ...OperationKind) {
	t.Helper()
	position := 0
	for _, operation := range operations {
		if position < len(wanted) && operation.Kind == wanted[position] {
			position++
		}
	}
	if position != len(wanted) {
		t.Fatalf("operation kinds = %#v, missing ordered prefix element %q", operations, wanted[position])
	}
}
