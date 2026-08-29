package webmcp_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// redialProbeHandle wraps a healthy scripted handle with a controllable
// webmcp.BrowserHandleHealth answer, modeling the production chrome handle
// whose transport loss flag is sticky while the endpoint itself is healthy.
type redialProbeHandle struct {
	webmcp.BrowserHandle
	lost   *atomic.Bool
	closed atomic.Bool
}

func (h *redialProbeHandle) Disconnected() bool { return h.lost.Load() }

func (h *redialProbeHandle) Close() error {
	h.closed.Store(true)
	return h.BrowserHandle.Close()
}

// redialProbeRuntime hands out the health-probed wrapper for the first open
// and plain scripted handles afterwards, counting every dial.
type redialProbeRuntime struct {
	inner     *testkit.ScriptedBrowserRuntime
	firstLost *atomic.Bool

	mu    sync.Mutex
	opens int
	first *redialProbeHandle
}

func (r *redialProbeRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	handle, err := r.inner.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opens++
	if r.opens == 1 {
		r.first = &redialProbeHandle{BrowserHandle: handle, lost: r.firstLost}
		return r.first, nil
	}
	return handle, nil
}

func (r *redialProbeRuntime) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

// TestStatefulBrokerRedialsDisconnectedCachedHandle locks the fix for the
// in-session attach failure observed live on 2026-08-29: the broker cached a
// browser handle whose transport had been lost (the connection was tied to the
// first caller's context) and kept reusing it, so every later selection failed
// browser_disconnected at attach while the endpoint stayed healthy. A cached
// handle that reports Disconnected must be discarded and the endpoint dialed
// again.
func TestStatefulBrokerRedialsDisconnectedCachedHandle(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-redial", Product: "fixture", Loopback: true}
	scripted := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
			webmcp.Target{BrowserID: candidate.ID, ID: "tab-redial", Type: "page", Title: "Redial", URL: "https://fixture.test/"},
			testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{"type":"object","properties":{},"additionalProperties":false}`)),
		)},
	})
	defer func() { _ = scripted.Close() }()

	firstLost := &atomic.Bool{}
	runtime := &redialProbeRuntime{inner: scripted, firstLost: firstLost}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() { _ = broker.Close() }()

	// The session shape: a first read-only call opens and caches the handle.
	if _, err := broker.ListTargets(context.Background(), webmcp.BrowserSelector{BrowserID: candidate.ID}); err != nil {
		t.Fatalf("initial list targets: %v", err)
	}
	if got := runtime.openCount(); got != 1 {
		t.Fatalf("open count after list = %d, want 1", got)
	}

	// The cached handle's transport is lost while the endpoint stays healthy.
	firstLost.Store(true)

	page, err := broker.Select(context.Background(), webmcp.TargetSelector{
		BrowserID: candidate.ID,
		TargetID:  "tab-redial",
	})
	if err != nil {
		t.Fatalf("select after cached-handle loss: %v", err)
	}
	if page.Key.TargetID != "tab-redial" || !page.Connected {
		t.Fatalf("selection after re-dial = %+v, want connected tab-redial", page)
	}
	if got := runtime.openCount(); got != 2 {
		t.Fatalf("open count after select = %d, want a fresh dial for the lost handle", got)
	}
	if runtime.first == nil || !runtime.first.closed.Load() {
		t.Fatalf("lost cached handle was not closed before re-dial")
	}
}
