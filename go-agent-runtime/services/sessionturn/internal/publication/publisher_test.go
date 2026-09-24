package publication

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const burstEvents = 3

type running struct {
	publisher *Publisher
	events    chan sessionturn.BrowserEvent
	timers    *fakeTimers
	catalog   *catalog
	sink      *sink
}

func startRunning(t *testing.T, base, initial []messages.ToolDefinition) *running {
	t.Helper()
	r := &running{events: make(chan sessionturn.BrowserEvent, eventBuffer), timers: newFakeTimers(), catalog: newCatalog(initial), sink: newSink()}
	r.publisher = New(sessionturn.PublicationRequest{
		StaticStableDefinitions: base, InitialDefinitions: concat(base, initial),
		Watch: eventWatch(r.events), Refresh: r.catalog.refresh, Publish: r.sink.publish, TimerFactory: r.timers,
	})
	r.publisher.Start(context.Background())
	t.Cleanup(r.publisher.Stop)
	return r
}

// settle sends one event, waits for its settle boundary, and fires it.
func (r *running) settle(t *testing.T, event sessionturn.BrowserEvent, refresh int) {
	t.Helper()
	r.events <- event
	r.timers.waitArmed(t, 1)
	r.timers.fire(t)
	waitRefresh(t, r.catalog, refresh)
}

func TestReplacesDefinitionsInOneRunningSession(t *testing.T) {
	base := []messages.ToolDefinition{definition(staticTool, "static"), definition("webmcp_select_tab", "stable")}
	pageA := []messages.ToolDefinition{definition("cube_state", "cube A")}
	pageB := []messages.ToolDefinition{definition("create_document", "document B"), definition("get_document", "read document B")}
	r := startRunning(t, base, pageA)
	r.publisher.MarkSessionReady()
	waitRefresh(t, r.catalog, 1)

	r.catalog.set(pageB)
	r.settle(t, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventSelected, Sequence: 1}, 2)
	if got := r.sink.next(t); !reflect.DeepEqual(got, Merge(base, pageB)) {
		t.Fatalf("A-to-B tools = %#v", got)
	}
	r.catalog.set(pageA)
	r.settle(t, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventGenerationChanged, Sequence: 2}, 3)
	if got := r.sink.next(t); !reflect.DeepEqual(got, Merge(base, pageA)) {
		t.Fatalf("B-to-A tools = %#v", got)
	}
	r.catalog.set(nil)
	r.settle(t, sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, Sequence: 3}, 4)
	if got := r.sink.next(t); !reflect.DeepEqual(got, messages.CanonicalToolDefinitions(base)) {
		t.Fatalf("empty catalog tools = %#v, want stable base", got)
	}
	r.publisher.Stop()
	state := r.publisher.State()
	if state.PublicationCount != burstEvents || state.LatestEventSequence != burstEvents || state.Lifecycle != sessionturn.PublicationStopped {
		t.Fatalf("state = %#v", state)
	}
}

// TestCoalescesSelectionCatalogBurst is deterministic: the timer is fired
// only after the publisher armed it once per consumed burst event.
func TestCoalescesSelectionCatalogBurst(t *testing.T) {
	base := []messages.ToolDefinition{definition(staticTool, "static"), definition("webmcp_select_tab", "stable")}
	pageA := []messages.ToolDefinition{definition("cube_state", "cube A")}
	pageB := []messages.ToolDefinition{definition("create_document", "document B")}
	r := startRunning(t, base, pageA)
	r.publisher.MarkSessionReady()
	waitRefresh(t, r.catalog, 1)

	r.catalog.set(pageB)
	for sequence, kind := range []sessionturn.BrowserEventType{sessionturn.BrowserEventSelected, sessionturn.BrowserEventGenerationChanged, sessionturn.BrowserEventCatalogChanged} {
		r.events <- sessionturn.BrowserEvent{Type: kind, BrowserID: browserID, TargetID: "tab-b", Generation: 2, Sequence: uint64(sequence + 1)}
	}
	r.timers.waitArmed(t, burstEvents)
	r.timers.fire(t)
	waitRefresh(t, r.catalog, 2)
	if got := r.sink.next(t); !reflect.DeepEqual(got, Merge(base, pageB)) {
		t.Fatalf("burst tools = %#v", got)
	}
	r.publisher.Stop()
	if calls, extra := r.catalog.callCount(), len(r.sink.published); calls != 2 || extra != 0 {
		t.Fatalf("refresh calls/extra publications = %d/%d, want one burst refresh", calls, extra)
	}
	if state := r.publisher.State(); state.PublicationCount != 1 || state.LastSuccessfulGeneration != 2 || state.LastSuccessfulEventSequence != burstEvents || state.LastSuccessfulTargetID != "tab-b" {
		t.Fatalf("state = %#v", state)
	}
}

func TestEventsBeforeReadyAreReconciledByFirstRefresh(t *testing.T) {
	base := []messages.ToolDefinition{definition(stableTool, "stable")}
	page := []messages.ToolDefinition{definition(pageToolName, "page")}
	r := startRunning(t, base, nil)
	r.catalog.set(page)
	r.events <- sessionturn.BrowserEvent{Type: sessionturn.BrowserEventCatalogChanged, BrowserID: browserID, TargetID: tabID, Generation: 1, Sequence: 1}
	r.events <- sessionturn.BrowserEvent{Type: "invocation_created", Sequence: 2}
	r.publisher.MarkSessionReady()
	waitRefresh(t, r.catalog, 1)
	if got := r.sink.next(t); !reflect.DeepEqual(got, Merge(base, page)) {
		t.Fatalf("first publication = %#v", got)
	}
	r.publisher.Stop()
	if state := r.publisher.State(); state.LastSuccessfulTargetID != tabID || state.LatestEventSequence != 2 || state.Lifecycle != sessionturn.PublicationStopped {
		t.Fatalf("state = %#v", state)
	}
}

func TestWatchLifecycleEdges(t *testing.T) {
	nilWatch := New(sessionturn.PublicationRequest{Watch: func(context.Context) <-chan sessionturn.BrowserEvent { return nil }, Refresh: newCatalog(nil).refresh})
	var parent context.Context
	nilWatch.Start(parent)
	if err := <-nilWatch.Errors(); !errors.Is(err, errNilWatch) || nilWatch.State().Lifecycle != sessionturn.PublicationFailed {
		t.Fatalf("nil watch = %v / %#v", err, nilWatch.State())
	}
	nilWatch.Stop()

	events := make(chan sessionturn.BrowserEvent)
	closed := New(sessionturn.PublicationRequest{Watch: eventWatch(events), Refresh: newCatalog(nil).refresh})
	closed.Start(context.Background())
	close(events)
	<-closed.done
	closed.Stop()
	if closed.State().Lifecycle != sessionturn.PublicationWatchClosed {
		t.Fatalf("closed watch lifecycle = %s", closed.State().Lifecycle)
	}

	unstarted := New(sessionturn.PublicationRequest{})
	unstarted.Stop()
	unstarted.Stop()
	if unstarted.State().Lifecycle != sessionturn.PublicationStopped {
		t.Fatalf("unstarted stop lifecycle = %s", unstarted.State().Lifecycle)
	}
}

func TestWatchCloseDuringReadyAndSettleDrains(t *testing.T) {
	events := make(chan sessionturn.BrowserEvent, eventBuffer)
	closedAtReady := New(sessionturn.PublicationRequest{Watch: eventWatch(events), Refresh: newCatalog(nil).refresh})
	close(events)
	closedAtReady.MarkSessionReady()
	closedAtReady.Start(context.Background())
	<-closedAtReady.done
	closedAtReady.Stop()
	if closedAtReady.State().Lifecycle != sessionturn.PublicationWatchClosed {
		t.Fatalf("ready drain lifecycle = %s", closedAtReady.State().Lifecycle)
	}

	r := startRunning(t, nil, nil)
	r.publisher.MarkSessionReady()
	waitRefresh(t, r.catalog, 1)
	r.events <- sessionturn.BrowserEvent{Type: sessionturn.BrowserEventSelected, Sequence: 1}
	r.timers.waitArmed(t, 1)
	close(r.events)
	// The runner may observe the closed watch before the settle boundary;
	// either order must end in the watch-closed lifecycle.
	r.timers.timers[0].fire()
	<-r.publisher.done
	if lifecycle := r.publisher.State().Lifecycle; lifecycle != sessionturn.PublicationWatchClosed {
		t.Fatalf("settle drain lifecycle = %s", lifecycle)
	}
}

func TestInertPublication(t *testing.T) {
	var inert sessionturn.Publication = Inert{}
	inert.MarkSessionReady()
	inert.Stop()
	if inert.Errors() != nil || !reflect.DeepEqual(inert.State(), sessionturn.PublicationState{}) {
		t.Fatal("inert publication exposed state")
	}
}
