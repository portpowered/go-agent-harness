package sessionbroker

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestLiveBrowserEventsProjectsUntilSourceCloses(t *testing.T) {
	at := time.Unix(10, 0)
	source := make(chan webmcp.BrowserEvent, 1)
	source <- webmcp.BrowserEvent{Type: "catalog", Sequence: 7, At: at, BrowserID: "b", TargetID: "t", Generation: 2, PreviousGeneration: 1, ToolName: "search", CatalogReady: true, ToolCount: 3, ToolCountKnown: true}
	close(source)
	watch := LiveBrowserEvents(func(context.Context) <-chan webmcp.BrowserEvent { return source })
	events := watch(context.Background())
	event, ok := <-events
	if !ok {
		t.Fatal("projected stream closed before the event")
	}
	if event.Type != "catalog" || event.Sequence != 7 || !event.Timestamp.Equal(at) || event.BrowserID != "b" || event.TargetID != "t" ||
		event.Generation != 2 || event.PreviousGeneration != 1 || event.ToolName != "search" || !event.CatalogReady || event.ToolCount != 3 || !event.ToolCountKnown {
		t.Fatalf("projected browser event = %+v", event)
	}
	if _, ok := <-events; ok {
		t.Fatal("projected stream must close with its source")
	}
}

func TestLiveBrokerEventsKeepsLifecycleStateAndStopsWithContext(t *testing.T) {
	source := make(chan webmcp.BrokerEvent)
	ctx, cancel := context.WithCancel(context.Background())
	events := LiveBrokerEvents(func(context.Context) <-chan webmcp.BrokerEvent { return source })(ctx)
	source <- webmcp.BrokerEvent{Type: "invocation", State: "running", InvocationID: "i", Reason: "r"}
	event := <-events
	if event.State != "running" || event.InvocationID != "i" || event.Reason != "r" || event.Type != "invocation" {
		t.Fatalf("projected broker event = %+v", event)
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("unexpected event after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("projection did not stop with its context")
	}
}

func TestLiveCapabilityEventsWithoutSourceIsNil(t *testing.T) {
	if LiveBrowserEvents(nil)(context.Background()) != nil {
		t.Fatal("nil source must yield a nil stream")
	}
	empty := LiveBrokerEvents(func(context.Context) <-chan webmcp.BrokerEvent { return nil })
	if empty(context.Background()) != nil {
		t.Fatal("nil source stream must yield a nil stream")
	}
}
