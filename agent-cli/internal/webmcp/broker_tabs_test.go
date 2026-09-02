package webmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerOpenTabCreatesSelectsAndActivatesTarget(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-open-tab", Product: "fixture", Loopback: true}
	opened := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-opened",
		Type:      "page",
		URL:       "https://notes.example.test/",
		Origin:    "https://notes.example.test",
		Eligible:  true,
	}
	base := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets: []testkit.TargetConfig{testkit.NewTargetConfig(opened,
			testkit.WithInitialCatalog(webmcp.ToolDescriptor{
				Name: "read_notes", FrameID: "frame-notes", InputSchema: []byte(`{"type":"object"}`),
			}),
		),
		},
	})
	var openedURL string
	runtime := openTabRuntime{BrowserRuntime: base, opened: opened, last: &openedURL}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
	defer func() { _ = broker.Close() }()

	page, err := broker.OpenTab(context.Background(), webmcp.OpenTabRequest{URL: opened.URL, Activate: true})
	if err != nil {
		t.Fatalf("broker open tab: %v", err)
	}
	if page.Key.BrowserID != candidate.ID || page.Key.TargetID != opened.ID || page.URL != opened.URL || !page.Connected || !page.Ready {
		t.Fatalf("opened page = %+v", page)
	}
	if runtime.lastRequest() != opened.URL {
		t.Fatalf("runtime open-tab URL = %q, want %q", runtime.lastRequest(), opened.URL)
	}
}

func TestStatefulBrokerOpenTabRejectsUnsafeURLBeforeBrowserMutation(t *testing.T) {
	broker := webmcp.NewBroker(webmcp.BrokerOptions{})
	defer func() { _ = broker.Close() }()
	_, err := broker.OpenTab(context.Background(), webmcp.OpenTabRequest{URL: "file:///private/notes"})
	classified, ok := err.(*webmcp.ClassifiedError)
	if !ok || classified.Code != webmcp.ErrorInvalidToolInput {
		t.Fatalf("unsafe URL error = %T %v, want invalid_tool_input", err, err)
	}
}

func TestStatefulBrokerOpenTabReportsSelectedWhileCatalogIsLate(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-late-open", Product: "fixture", Loopback: true}
	opened := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-late-open",
		Type:      "page",
		URL:       "https://slow.example.test/",
		Origin:    "https://slow.example.test",
		Eligible:  true,
	}
	base := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(opened)},
	})
	runtime := openTabRuntime{BrowserRuntime: base, opened: opened}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:     runtime,
		Discoverer:  staticDiscoverer{candidate},
		CatalogWait: 10 * time.Millisecond,
	})
	defer func() { _ = broker.Close() }()

	page, err := broker.OpenTab(context.Background(), webmcp.OpenTabRequest{URL: opened.URL, Activate: true})
	if err != nil {
		t.Fatalf("open tab with late catalog: %v", err)
	}
	if page.Key.TargetID != opened.ID || !page.Connected || page.Ready {
		t.Fatalf("late-catalog opened page = %+v, want connected selected page pending readiness", page)
	}
}

type openTabRuntime struct {
	webmcp.BrowserRuntime
	opened webmcp.Target
	last   *string
}

func (r openTabRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	handle, err := r.BrowserRuntime.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return &openTabHandle{BrowserHandle: handle, opened: r.opened, last: r.last}, nil
}

func (r openTabRuntime) lastRequest() string {
	if r.last == nil {
		return ""
	}
	return *r.last
}

type openTabHandle struct {
	webmcp.BrowserHandle
	opened webmcp.Target
	last   *string
}

func (h *openTabHandle) OpenTab(_ context.Context, rawURL string) (webmcp.Target, error) {
	if h.last != nil {
		*h.last = rawURL
	}
	return h.opened, nil
}
