package chrome

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type blockingRoundTripper struct {
	started chan struct{}
	once    sync.Once
}

func (t *blockingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestHandleTransportLossUnblocksTargetListing(t *testing.T) {
	transport := &blockingRoundTripper{started: make(chan struct{})}
	executor := &recordingExecutor{}
	handle := testHandle(executor)
	handle.httpClient = &http.Client{Transport: transport}
	handle.candidate.HTTPURL = "http://browser.invalid:9222"
	handle.disconnectDone = make(chan struct{})

	listDone := make(chan struct {
		targets []webmcp.Target
		err     error
	}, 1)
	go func() {
		targets, err := handle.ListTargets(context.Background())
		listDone <- struct {
			targets []webmcp.Target
			err     error
		}{targets: targets, err: err}
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("target listing did not reach the blocking transport")
	}

	handle.markTransportLost()
	select {
	case result := <-listDone:
		if result.targets != nil {
			t.Fatalf("targets after transport loss = %#v, want nil", result.targets)
		}
		assertChromeBrowserDisconnected(t, result.err, "list_targets", handle.candidate.ID, "")
	case <-time.After(time.Second):
		t.Fatal("target listing remained blocked after transport loss")
	}
	if !handle.isDisconnected() {
		t.Fatal("handle did not retain disconnected state")
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close disconnected handle: %v", err)
	}
}

func TestHandleTransportLossUnblocksTargetAttach(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"target-attach","type":"page","title":"Attach","url":"https://example.test/attach","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/target-attach"}]`))
	}))
	defer server.Close()

	executor := &recordingExecutor{}
	handle := testHandle(executor)
	handle.candidate.HTTPURL = server.URL
	runStarted := make(chan struct{})
	handle.targetOps = targetContextOps{
		newContext: func(context.Context, target.ID) (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		listen: func(context.Context, func(any)) {},
		run: func(ctx context.Context, _ ...chromedp.Action) error {
			close(runStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		target: func(context.Context) *chromedp.Target {
			return &chromedp.Target{SessionID: "session-attach", TargetID: "target-attach"}
		},
	}

	attachDone := make(chan error, 1)
	go func() {
		_, err := handle.Attach(context.Background(), "target-attach", webmcp.TargetOwnershipExternal)
		attachDone <- err
	}()
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("target attach did not reach the persistent run")
	}

	handle.markTransportLost()
	select {
	case err := <-attachDone:
		assertChromeBrowserDisconnected(t, err, "attach", handle.candidate.ID, "target-attach")
	case <-time.After(time.Second):
		t.Fatal("target attach remained blocked after transport loss")
	}
	if calls := executor.snapshot(); len(calls) != 0 {
		// The attach uses a custom run seam and must not issue a destructive
		// target close while the browser transport is already gone.
		t.Fatalf("unexpected executor state = %#v", calls)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close disconnected handle: %v", err)
	}
}

func assertChromeBrowserDisconnected(t *testing.T, err error, phase string, browserID webmcp.BrowserID, targetID webmcp.TargetID) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded, want browser_disconnected")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
		t.Fatalf("error = %v (%T), want browser_disconnected", err, err)
	}
	if classified.Details["browser_id"] != string(browserID) || classified.Details["target_id"] != string(targetID) || classified.Details["phase"] != phase || classified.Details["reconnect_required"] != true {
		t.Fatalf("browser loss details = %#v, want browser=%q target=%q phase=%q", classified.Details, browserID, targetID, phase)
	}
}
