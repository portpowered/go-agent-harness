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
