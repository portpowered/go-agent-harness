package webmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// Fixture identities shared by the broker tests: primaryTargetID is the
// default scripted page target (and the watched target shared by every
// independent client phase), secondaryTargetID a second page in the same
// browser, and livingRoomCastDevice the scripted cast sink name.
const (
	primaryTargetID      = "tab-a"
	secondaryTargetID    = "tab-second"
	livingRoomCastDevice = "Living Room TV"
)

// crossProcessWatch is the watched broker state shared by the independent
// client phases of the cross-process observation test.
type crossProcessWatch struct {
	runtime   *testkit.ScriptedBrowserRuntime
	broker    *webmcp.StatefulBroker
	events    <-chan webmcp.BrokerEvent
	candidate webmcp.BrowserCandidate
}

func TestStatefulBrokerObservesIndependentClientCatalogAndInvocationEvents(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	otherCandidate := webmcp.BrowserCandidate{ID: "browser-b", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: primaryTargetID, Type: "page"},
					testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
				),
				testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-b", Type: "page"}),
			},
		},
		testkit.BrowserConfig{
			Candidate: otherCandidate,
			Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(webmcp.Target{BrowserID: otherCandidate.ID, ID: "tab-other", Type: "page"})},
		},
	)
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("close runtime: %v", err)
		}
	}()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() {
		if err := broker.Close(); err != nil {
			t.Fatalf("close broker: %v", err)
		}
	}()
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := crossProcessWatch{runtime: runtime, broker: broker, events: broker.Watch(watchContext), candidate: candidate}

	readRef := selectCrossProcessWatchedTarget(t, watch)
	externalHandle, externalSession := attachCrossProcessExternalClient(t, watch)
	assertCrossProcessEarlyResponseBuffered(t, watch, externalSession, readRef)
	writeTool := pageTool("write_state", "frame-1", `{}`)
	writeRef := addCrossProcessExternalTool(t, watch, externalSession, writeTool)
	assertCrossProcessExternalInvocation(t, watch, externalSession, writeTool, writeRef)
	assertCrossProcessUnresolvedInvocation(t, watch, externalSession, writeTool)
	assertCrossProcessStaleGenerationIgnored(t, watch, externalSession)
	assertCrossProcessOtherTargetsIgnored(t, watch, externalHandle, otherCandidate)

	cancelWatch()
	select {
	case _, ok := <-watch.events:
		if ok {
			t.Fatal("watch stream remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("watch stream did not close after cancellation")
	}
}

func requireBrokerStep(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

func requireWatchedCatalogEvent(t *testing.T, watch crossProcessWatch, reason, label string) {
	t.Helper()
	event := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventCatalogChanged)
	if event.BrowserID != watch.candidate.ID || event.TargetID != primaryTargetID || event.Generation != 1 || event.Reason != reason {
		t.Fatalf("%s = %#v, want watched target generation-one %s", label, event, reason)
	}
}

func selectCrossProcessWatchedTarget(t *testing.T, watch crossProcessWatch) webmcp.ToolRef {
	t.Helper()
	if _, err := watch.broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: watch.candidate.ID, TargetID: primaryTargetID}); err != nil {
		t.Fatalf("select watched target: %v", err)
	}
	selectedEvent := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventSelected)
	if selectedEvent.BrowserID != watch.candidate.ID || selectedEvent.TargetID != primaryTargetID || selectedEvent.Generation != 1 {
		t.Fatalf("selected event = %#v, want watched target generation one", selectedEvent)
	}
	requireWatchedCatalogEvent(t, watch, "tools_added", "initial catalog event")
	snapshot, err := watch.broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list initial tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf("initial tools = %#v, want one tool", snapshot.Tools)
	}
	return snapshot.Tools[0].Ref
}

func attachCrossProcessExternalClient(t *testing.T, watch crossProcessWatch) (webmcp.BrowserHandle, *testkit.ScriptedTargetSession) {
	t.Helper()
	externalHandleValue, err := watch.runtime.Open(context.Background(), watch.candidate)
	requireBrokerStep(t, err, "open external client")
	externalSessionValue, err := externalHandleValue.Attach(context.Background(), primaryTargetID, webmcp.TargetOwnershipExternal)
	requireBrokerStep(t, err, "attach external client")
	externalSession := externalSessionValue.(*testkit.ScriptedTargetSession)
	if externalSession == nil {
		t.Fatal("external session is nil")
	}
	requireBrokerStep(t, externalSession.EnableWebMCP(context.Background()), "enable external client")
	if event := waitForTestkitEvent(t, externalSession.Events()); event.Type != webmcp.EventTargetAttached {
		t.Fatalf("external attach event = %#v, want target_attached", event)
	}
	if event := waitForTestkitEvent(t, externalSession.Events()); event.Type != webmcp.EventToolsAdded {
		t.Fatalf("external enable event = %#v, want tools_added", event)
	}
	// The initial descriptor is already present in the watcher's catalog. The
	// second client's enable echo is therefore a no-op and must not create a
	// duplicate semantic catalog event.
	_, err = watch.broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	requireBrokerStep(t, err, "flush external initial catalog")
	assertNoBrokerEvent(t, watch.events, "duplicate initial catalog descriptor")
	return externalHandleValue, externalSession
}

func assertCrossProcessEarlyResponseBuffered(t *testing.T, watch crossProcessWatch, externalSession *testkit.ScriptedTargetSession, readRef webmcp.ToolRef) {
	t.Helper()
	earlyInvocationID := webmcp.InvocationID("external-early")
	requireBrokerStep(t, externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		Generation:   1,
		InvocationID: earlyInvocationID,
		Status:       "Completed",
		Output:       []byte(`{"early":true}`),
	}), "emit response before invocation")
	assertNoBrokerEvent(t, watch.events, "response before invocation")
	requireBrokerStep(t, externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		Generation:   1,
		FrameID:      "frame-1",
		ToolName:     "read_state",
		InvocationID: earlyInvocationID,
	}), "emit invocation after response")
	earlyCreated := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationCreated)
	if earlyCreated.InvocationID != earlyInvocationID || earlyCreated.ToolRef != readRef || earlyCreated.State != webmcp.InvocationDispatched || earlyCreated.Generation != 1 {
		t.Fatalf("early invocation created event = %#v, want catalog-bound observation", earlyCreated)
	}
	earlyTerminal := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationTerminal)
	if earlyTerminal.InvocationID != earlyInvocationID || earlyTerminal.ToolRef != readRef || earlyTerminal.State != webmcp.InvocationCompleted || earlyTerminal.Generation != 1 {
		t.Fatalf("early invocation terminal event = %#v, want one buffered completion", earlyTerminal)
	}
}

func addCrossProcessExternalTool(t *testing.T, watch crossProcessWatch, externalSession *testkit.ScriptedTargetSession, writeTool webmcp.ToolDescriptor) webmcp.ToolRef {
	t.Helper()
	requireBrokerStep(t, externalSession.EmitToolsAdded(writeTool), "emit external catalog change")
	requireWatchedCatalogEvent(t, watch, "tools_added", "catalog event")
	updated, err := watch.broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	requireBrokerStep(t, err, "list updated tools")
	var writeRef webmcp.ToolRef
	for _, tool := range updated.Tools {
		if tool.Name == writeTool.Name {
			writeRef = tool.Ref
		}
	}
	if writeRef == "" {
		t.Fatalf("updated tools = %#v, want external tool", updated.Tools)
	}
	return writeRef
}

func assertCrossProcessExternalInvocation(t *testing.T, watch crossProcessWatch, externalSession *testkit.ScriptedTargetSession, writeTool webmcp.ToolDescriptor, writeRef webmcp.ToolRef) {
	t.Helper()
	externalInvocationID, err := externalSession.InvokeWebMCP(context.Background(), writeTool.FrameID, writeTool.Name, []byte(`{"step":1}`))
	requireBrokerStep(t, err, "invoke from external client")
	created := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationCreated)
	if created.InvocationID != externalInvocationID || created.ToolRef != writeRef || created.State != webmcp.InvocationDispatched || created.BrowserID != watch.candidate.ID || created.TargetID != primaryTargetID || created.Generation != 1 || created.Reason != "browser_observed" {
		t.Fatalf("external invocation created event = %#v, want one correlated observation", created)
	}

	requireBrokerStep(t, externalSession.EmitToolResponse(externalInvocationID, "Completed", []byte(`{"ok":true}`)), "respond from external client")
	terminal := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationTerminal)
	if terminal.InvocationID != externalInvocationID || terminal.ToolRef != writeRef || terminal.State != webmcp.InvocationCompleted || terminal.BrowserID != watch.candidate.ID || terminal.TargetID != primaryTargetID || terminal.Generation != 1 {
		t.Fatalf("external invocation terminal event = %#v, want one correlated completion", terminal)
	}
	requireBrokerStep(t, externalSession.EmitToolResponse(externalInvocationID, "Completed", []byte(`{"duplicate":true}`)), "emit duplicate external response")
	assertNoBrokerEvent(t, watch.events, "duplicate external response")
}

// assertCrossProcessUnresolvedInvocation covers a protocol invocation that
// is observed after its catalog descriptor has disappeared. The watcher
// preserves the lifecycle and ID without guessing a stale reference.
func assertCrossProcessUnresolvedInvocation(t *testing.T, watch crossProcessWatch, externalSession *testkit.ScriptedTargetSession, writeTool webmcp.ToolDescriptor) {
	t.Helper()
	requireBrokerStep(t, externalSession.EmitToolsRemoved("frame-1", writeTool.Name), "emit external catalog removal")
	requireWatchedCatalogEvent(t, watch, "tools_removed", "catalog removal event")
	unresolvedID := webmcp.InvocationID("external-unresolved")
	requireBrokerStep(t, externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		Generation:   1,
		FrameID:      "frame-1",
		ToolName:     writeTool.Name,
		InvocationID: unresolvedID,
	}), "emit unresolved invocation")
	unresolvedCreated := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationCreated)
	if unresolvedCreated.InvocationID != unresolvedID || unresolvedCreated.ToolRef != "" {
		t.Fatalf("unresolved created event = %#v, want empty current ref", unresolvedCreated)
	}
	requireBrokerStep(t, externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		Generation:   1,
		InvocationID: unresolvedID,
		Status:       "Completed",
		Output:       []byte(`{"unresolved":true}`),
	}), "respond to unresolved invocation")
	unresolvedTerminal := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventInvocationTerminal)
	if unresolvedTerminal.InvocationID != unresolvedID || unresolvedTerminal.ToolRef != "" || unresolvedTerminal.State != webmcp.InvocationCompleted {
		t.Fatalf("unresolved terminal event = %#v, want completed event without ref", unresolvedTerminal)
	}
	requireBrokerStep(t, externalSession.EmitToolsRemoved("frame-1", writeTool.Name), "emit repeated external catalog removal")
	assertNoBrokerEvent(t, watch.events, "repeated catalog removal")
	requireBrokerStep(t, externalSession.EmitToolsAdded(writeTool), "re-add external catalog tool")
	requireWatchedCatalogEvent(t, watch, "tools_added", "catalog re-add event")
	requireBrokerStep(t, externalSession.EmitToolsRemoved("frame-1", writeTool.Name), "emit external catalog removal")
	requireWatchedCatalogEvent(t, watch, "tools_removed", "catalog removal event")
}

func assertCrossProcessStaleGenerationIgnored(t *testing.T, watch crossProcessWatch, externalSession *testkit.ScriptedTargetSession) {
	t.Helper()
	requireBrokerStep(t, externalSession.Navigate("https://fixture.test/next", "https://fixture.test"), "navigate watched target")
	generationEvent := waitForBrokerEvent(t, watch.events, webmcp.BrokerEventGenerationChanged)
	if generationEvent.BrowserID != watch.candidate.ID || generationEvent.TargetID != primaryTargetID || generationEvent.Generation != 2 {
		t.Fatalf("generation event = %#v, want generation two", generationEvent)
	}
	requireBrokerStep(t, externalSession.Emit(webmcp.BrowserEvent{
		Type:       webmcp.EventToolsAdded,
		Generation: 1,
		Tools:      []webmcp.ToolDescriptor{pageTool("stale_tool", "frame-1", `{}`)},
	}), "emit stale catalog event")
	assertNoBrokerEvent(t, watch.events, "stale generation catalog event")
	current, err := watch.broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	requireBrokerStep(t, err, "list catalog after stale event")
	if current.Generation != 2 || len(current.Tools) != 0 {
		t.Fatalf("catalog after stale event = %#v, want empty generation-two catalog", current)
	}
}

func assertCrossProcessOtherTargetsIgnored(t *testing.T, watch crossProcessWatch, externalHandle webmcp.BrowserHandle, otherCandidate webmcp.BrowserCandidate) {
	t.Helper()
	otherTargetValue, err := externalHandle.Attach(context.Background(), "tab-b", webmcp.TargetOwnershipExternal)
	requireBrokerStep(t, err, "attach other target")
	otherTarget := otherTargetValue.(*testkit.ScriptedTargetSession)
	requireBrokerStep(t, otherTarget.EmitToolsAdded(pageTool("other_target_tool", "frame-1", `{}`)), "emit other-target catalog event")
	assertNoBrokerEvent(t, watch.events, "other-target catalog event")

	otherBrowserHandle, err := watch.runtime.Open(context.Background(), otherCandidate)
	requireBrokerStep(t, err, "open other browser client")
	otherBrowserSessionValue, err := otherBrowserHandle.Attach(context.Background(), "tab-other", webmcp.TargetOwnershipExternal)
	requireBrokerStep(t, err, "attach other browser target")
	otherBrowserSession := otherBrowserSessionValue.(*testkit.ScriptedTargetSession)
	requireBrokerStep(t, otherBrowserSession.EmitToolsAdded(pageTool("other_browser_tool", "frame-1", `{}`)), "emit other-browser catalog event")
	assertNoBrokerEvent(t, watch.events, "other-browser catalog event")
}

func waitForBrokerEvent(t *testing.T, events <-chan webmcp.BrokerEvent, want webmcp.BrokerEventType) webmcp.BrokerEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case event := <-events:
		if event.Type != want {
			t.Fatalf("broker event = %#v, want %q", event, want)
		}
		return event
	case <-timer.C:
		t.Fatalf("timed out waiting for broker event %q", want)
		return webmcp.BrokerEvent{}
	}
}

func assertNoBrokerEvent(t *testing.T, events <-chan webmcp.BrokerEvent, label string) {
	t.Helper()
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case event := <-events:
		t.Fatalf("%s produced broker event %#v", label, event)
	case <-timer.C:
	}
}

func waitForTestkitEvent(t *testing.T, events <-chan webmcp.BrowserEvent) webmcp.BrowserEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case event := <-events:
		return event
	case <-timer.C:
		t.Fatal("timed out waiting for testkit browser event")
		return webmcp.BrowserEvent{}
	}
}

// scriptedTargetSession returns the scripted session for targetID behind a
// handle opened from a testkit runtime, failing the test when the handle is
// not a scripted one.
func scriptedTargetSession(t *testing.T, handle webmcp.BrowserHandle, targetID webmcp.TargetID) *testkit.ScriptedTargetSession {
	t.Helper()
	scripted, ok := handle.(*testkit.ScriptedBrowserHandle)
	if !ok {
		t.Fatalf("fixture handle is %T, want *testkit.ScriptedBrowserHandle", handle)
	}
	return scripted.TargetSession(targetID)
}
