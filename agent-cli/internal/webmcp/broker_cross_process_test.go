package webmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerObservesIndependentClientCatalogAndInvocationEvents(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	otherCandidate := webmcp.BrowserCandidate{ID: "browser-b", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
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
	events := broker.Watch(watchContext)

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select watched target: %v", err)
	}
	selectedEvent := waitForBrokerEvent(t, events, webmcp.BrokerEventSelected)
	if selectedEvent.BrowserID != candidate.ID || selectedEvent.TargetID != "tab-a" || selectedEvent.Generation != 1 {
		t.Fatalf("selected event = %#v, want watched target generation one", selectedEvent)
	}
	initialCatalogEvent := waitForBrokerEvent(t, events, webmcp.BrokerEventCatalogChanged)
	if initialCatalogEvent.BrowserID != candidate.ID || initialCatalogEvent.TargetID != "tab-a" || initialCatalogEvent.Generation != 1 || initialCatalogEvent.Reason != "tools_added" {
		t.Fatalf("initial catalog event = %#v, want watched target catalog admission", initialCatalogEvent)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list initial tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf("initial tools = %#v, want one tool", snapshot.Tools)
	}

	externalHandleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open external client: %v", err)
	}
	externalSessionValue, err := externalHandleValue.Attach(context.Background(), "tab-a", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach external client: %v", err)
	}
	externalSession := externalSessionValue.(*testkit.ScriptedTargetSession)
	if externalSession == nil {
		t.Fatal("external session is nil")
	}
	if err := externalSession.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable external client: %v", err)
	}
	if event := waitForTestkitEvent(t, externalSession.Events()); event.Type != webmcp.EventTargetAttached {
		t.Fatalf("external attach event = %#v, want target_attached", event)
	}
	if event := waitForTestkitEvent(t, externalSession.Events()); event.Type != webmcp.EventToolsAdded {
		t.Fatalf("external enable event = %#v, want tools_added", event)
	}
	// The initial descriptor is already present in the watcher's catalog. The
	// second client's enable echo is therefore a no-op and must not create a
	// duplicate semantic catalog event.
	if _, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true}); err != nil {
		t.Fatalf("flush external initial catalog: %v", err)
	}
	assertNoBrokerEvent(t, events, "duplicate initial catalog descriptor")

	earlyInvocationID := webmcp.InvocationID("external-early")
	if err := externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		Generation:   1,
		InvocationID: earlyInvocationID,
		Status:       "Completed",
		Output:       []byte(`{"early":true}`),
	}); err != nil {
		t.Fatalf("emit response before invocation: %v", err)
	}
	assertNoBrokerEvent(t, events, "response before invocation")
	if err := externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		Generation:   1,
		FrameID:      "frame-1",
		ToolName:     "read_state",
		InvocationID: earlyInvocationID,
	}); err != nil {
		t.Fatalf("emit invocation after response: %v", err)
	}
	earlyCreated := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationCreated)
	if earlyCreated.InvocationID != earlyInvocationID || earlyCreated.ToolRef != snapshot.Tools[0].Ref || earlyCreated.State != webmcp.InvocationDispatched || earlyCreated.Generation != 1 {
		t.Fatalf("early invocation created event = %#v, want catalog-bound observation", earlyCreated)
	}
	earlyTerminal := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationTerminal)
	if earlyTerminal.InvocationID != earlyInvocationID || earlyTerminal.ToolRef != snapshot.Tools[0].Ref || earlyTerminal.State != webmcp.InvocationCompleted || earlyTerminal.Generation != 1 {
		t.Fatalf("early invocation terminal event = %#v, want one buffered completion", earlyTerminal)
	}

	writeTool := pageTool("write_state", "frame-1", `{}`)
	if err := externalSession.EmitToolsAdded(writeTool); err != nil {
		t.Fatalf("emit external catalog change: %v", err)
	}
	catalogEvent := waitForBrokerEvent(t, events, webmcp.BrokerEventCatalogChanged)
	if catalogEvent.BrowserID != candidate.ID || catalogEvent.TargetID != "tab-a" || catalogEvent.Generation != 1 || catalogEvent.Reason != "tools_added" {
		t.Fatalf("catalog event = %#v, want watched target generation-one change", catalogEvent)
	}
	updated, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list updated tools: %v", err)
	}
	var writeRef webmcp.ToolRef
	for _, tool := range updated.Tools {
		if tool.Name == writeTool.Name {
			writeRef = tool.Ref
		}
	}
	if writeRef == "" {
		t.Fatalf("updated tools = %#v, want external tool", updated.Tools)
	}

	externalInvocationID, err := externalSession.InvokeWebMCP(context.Background(), writeTool.FrameID, writeTool.Name, []byte(`{"step":1}`))
	if err != nil {
		t.Fatalf("invoke from external client: %v", err)
	}
	created := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationCreated)
	if created.InvocationID != externalInvocationID || created.ToolRef != writeRef || created.State != webmcp.InvocationDispatched || created.BrowserID != candidate.ID || created.TargetID != "tab-a" || created.Generation != 1 || created.Reason != "browser_observed" {
		t.Fatalf("external invocation created event = %#v, want one correlated observation", created)
	}

	if err := externalSession.EmitToolResponse(externalInvocationID, "Completed", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("respond from external client: %v", err)
	}
	terminal := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationTerminal)
	if terminal.InvocationID != externalInvocationID || terminal.ToolRef != writeRef || terminal.State != webmcp.InvocationCompleted || terminal.BrowserID != candidate.ID || terminal.TargetID != "tab-a" || terminal.Generation != 1 {
		t.Fatalf("external invocation terminal event = %#v, want one correlated completion", terminal)
	}
	if err := externalSession.EmitToolResponse(externalInvocationID, "Completed", []byte(`{"duplicate":true}`)); err != nil {
		t.Fatalf("emit duplicate external response: %v", err)
	}
	assertNoBrokerEvent(t, events, "duplicate external response")

	// A protocol invocation can still be observed when its catalog descriptor
	// has disappeared. The watcher preserves the lifecycle and ID without
	// guessing a stale reference.
	if err := externalSession.EmitToolsRemoved("frame-1", writeTool.Name); err != nil {
		t.Fatalf("emit external catalog removal: %v", err)
	}
	removed := waitForBrokerEvent(t, events, webmcp.BrokerEventCatalogChanged)
	if removed.BrowserID != candidate.ID || removed.TargetID != "tab-a" || removed.Generation != 1 || removed.Reason != "tools_removed" {
		t.Fatalf("catalog removal event = %#v, want watched target removal", removed)
	}
	unresolvedID := webmcp.InvocationID("external-unresolved")
	if err := externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		Generation:   1,
		FrameID:      "frame-1",
		ToolName:     writeTool.Name,
		InvocationID: unresolvedID,
	}); err != nil {
		t.Fatalf("emit unresolved invocation: %v", err)
	}
	unresolvedCreated := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationCreated)
	if unresolvedCreated.InvocationID != unresolvedID || unresolvedCreated.ToolRef != "" {
		t.Fatalf("unresolved created event = %#v, want empty current ref", unresolvedCreated)
	}
	if err := externalSession.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		Generation:   1,
		InvocationID: unresolvedID,
		Status:       "Completed",
		Output:       []byte(`{"unresolved":true}`),
	}); err != nil {
		t.Fatalf("respond to unresolved invocation: %v", err)
	}
	unresolvedTerminal := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationTerminal)
	if unresolvedTerminal.InvocationID != unresolvedID || unresolvedTerminal.ToolRef != "" || unresolvedTerminal.State != webmcp.InvocationCompleted {
		t.Fatalf("unresolved terminal event = %#v, want completed event without ref", unresolvedTerminal)
	}
	if err := externalSession.EmitToolsRemoved("frame-1", writeTool.Name); err != nil {
		t.Fatalf("emit repeated external catalog removal: %v", err)
	}
	assertNoBrokerEvent(t, events, "repeated catalog removal")
	if err := externalSession.EmitToolsAdded(writeTool); err != nil {
		t.Fatalf("re-add external catalog tool: %v", err)
	}
	readded := waitForBrokerEvent(t, events, webmcp.BrokerEventCatalogChanged)
	if readded.BrowserID != candidate.ID || readded.TargetID != "tab-a" || readded.Generation != 1 || readded.Reason != "tools_added" {
		t.Fatalf("catalog re-add event = %#v, want watched target generation-one change", readded)
	}
	if err := externalSession.EmitToolsRemoved("frame-1", writeTool.Name); err != nil {
		t.Fatalf("emit external catalog removal: %v", err)
	}
	removed = waitForBrokerEvent(t, events, webmcp.BrokerEventCatalogChanged)
	if removed.BrowserID != candidate.ID || removed.TargetID != "tab-a" || removed.Generation != 1 || removed.Reason != "tools_removed" {
		t.Fatalf("catalog removal event = %#v, want watched target removal", removed)
	}

	if err := externalSession.Navigate("https://fixture.test/next", "https://fixture.test"); err != nil {
		t.Fatalf("navigate watched target: %v", err)
	}
	generationEvent := waitForBrokerEvent(t, events, webmcp.BrokerEventGenerationChanged)
	if generationEvent.BrowserID != candidate.ID || generationEvent.TargetID != "tab-a" || generationEvent.Generation != 2 {
		t.Fatalf("generation event = %#v, want generation two", generationEvent)
	}
	if err := externalSession.Emit(webmcp.BrowserEvent{
		Type:       webmcp.EventToolsAdded,
		Generation: 1,
		Tools:      []webmcp.ToolDescriptor{pageTool("stale_tool", "frame-1", `{}`)},
	}); err != nil {
		t.Fatalf("emit stale catalog event: %v", err)
	}
	assertNoBrokerEvent(t, events, "stale generation catalog event")
	current, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list catalog after stale event: %v", err)
	}
	if current.Generation != 2 || len(current.Tools) != 0 {
		t.Fatalf("catalog after stale event = %#v, want empty generation-two catalog", current)
	}

	otherTargetValue, err := externalHandleValue.Attach(context.Background(), "tab-b", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach other target: %v", err)
	}
	otherTarget := otherTargetValue.(*testkit.ScriptedTargetSession)
	if err := otherTarget.EmitToolsAdded(pageTool("other_target_tool", "frame-1", `{}`)); err != nil {
		t.Fatalf("emit other-target catalog event: %v", err)
	}
	assertNoBrokerEvent(t, events, "other-target catalog event")

	otherBrowserHandle, err := runtime.Open(context.Background(), otherCandidate)
	if err != nil {
		t.Fatalf("open other browser client: %v", err)
	}
	otherBrowserSessionValue, err := otherBrowserHandle.Attach(context.Background(), "tab-other", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach other browser target: %v", err)
	}
	otherBrowserSession := otherBrowserSessionValue.(*testkit.ScriptedTargetSession)
	if err := otherBrowserSession.EmitToolsAdded(pageTool("other_browser_tool", "frame-1", `{}`)); err != nil {
		t.Fatalf("emit other-browser catalog event: %v", err)
	}
	assertNoBrokerEvent(t, events, "other-browser catalog event")

	cancelWatch()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("watch stream remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("watch stream did not close after cancellation")
	}
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
