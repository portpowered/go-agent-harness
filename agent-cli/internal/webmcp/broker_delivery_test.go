package webmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerWatchOverflowReportsBoundedFailure(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-watch", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-watch", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:     runtime,
		Discoverer:  staticDiscoverer{candidate},
		WatchBuffer: 1,
	})
	defer func() { _ = broker.Close() }()

	watch := broker.Watch(context.Background())
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-watch"}); err != nil {
		t.Fatalf("select watched target: %v", err)
	}
	selected := waitForBrokerEvent(t, watch, webmcp.BrokerEventSelected)
	failure := waitForBrokerEvent(t, watch, webmcp.BrokerEventSessionClosed)
	if failure.Reason != webmcp.BrokerWatchBufferFullReason || failure.BrowserID != candidate.ID || failure.TargetID != "tab-watch" || failure.Generation != 1 {
		t.Fatalf("watch overflow event = %#v, want bounded failure for selected target", failure)
	}
	if failure.Sequence <= selected.Sequence {
		t.Fatalf("watch overflow sequence = %d after selected sequence %d, want increasing", failure.Sequence, selected.Sequence)
	}
	select {
	case _, ok := <-watch:
		if ok {
			t.Fatal("watch stream remained open after bounded overflow")
		}
	case <-time.After(time.Second):
		t.Fatal("watch stream did not close after bounded overflow")
	}
}

func TestStatefulBrokerBrowserEventWatchFansOutIndependentCopies(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-browser-events", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-browser-events", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{"type":"object"}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:     runtime,
		Discoverer:  staticDiscoverer{candidate},
		WatchBuffer: 8,
	})
	defer func() { _ = broker.Close() }()

	first := broker.WatchBrowserEvents(context.Background())
	second := broker.WatchBrowserEvents(context.Background())
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-browser-events"}); err != nil {
		t.Fatalf("select browser-event target: %v", err)
	}
	for index, events := range []<-chan webmcp.BrowserEvent{first, second} {
		event := waitForTestkitEvent(t, events)
		if event.Type != webmcp.EventTargetAttached || event.BrowserID != candidate.ID || event.TargetID != "tab-browser-events" {
			t.Fatalf("watcher %d attached event = %#v, want selected target attachment", index, event)
		}
	}
	firstAdded := waitForTestkitEvent(t, first)
	secondAdded := waitForTestkitEvent(t, second)
	if firstAdded.Type != webmcp.EventToolsAdded || secondAdded.Type != webmcp.EventToolsAdded || len(firstAdded.Tools) != 1 || len(secondAdded.Tools) != 1 || len(firstAdded.Tools[0].InputSchema) == 0 || len(secondAdded.Tools[0].InputSchema) == 0 {
		t.Fatalf("tools-added fan-out = %#v / %#v, want one tool on each stream", firstAdded, secondAdded)
	}
	firstAdded.Tools[0].InputSchema[0] = 'x'
	if string(secondAdded.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("semantic watcher payloads alias their tool schema: second=%s", secondAdded.Tools[0].InputSchema)
	}
}
