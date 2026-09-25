package chrome

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
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
		writeFixtureBody(writer, []byte(`[{"id":"target-attach","type":"page","title":"Attach","url":"https://example.test/attach","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/target-attach"}]`))
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

// TestPinnedChromeConnectionSurvivesOpenerContextCancel locks the fix for the
// in-session attach failure observed live on 2026-08-29: the adapter bound the
// chromedp browser connection's lifetime to the ctx of whichever call first
// dialed the endpoint. A session's first bounded tool call therefore tore the
// websocket down when it returned, the sticky disconnected flag poisoned the
// broker's cached handle, and every later select failed browser_disconnected
// at phase=attach while Chrome stayed healthy. The opener's ctx must bound
// only the dial: after Open returns, cancellation of that ctx must not end
// the connection, and both ListTargets and Attach must still succeed.
func TestPinnedChromeConnectionSurvivesOpenerContextCancel(t *testing.T) {
	if os.Getenv(chromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome integration proof", chromeIntegrationEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pinned, err := acquirePinnedChrome(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	fixture := newFixtureServer()
	t.Cleanup(func() { fixture.Close() })
	browser, err := launchPinnedChrome(ctx, pinned, fixture.URL())
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})
	version, err := waitForDevToolsVersion(ctx, browserHTTPURL(browser.endpoint()), lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	fixtureTarget, err := waitForFixturePageTarget(ctx, browserHTTPURL(browser.endpoint()), fixture.URL())
	if err != nil {
		t.Fatalf("discover fixture target: %v", err)
	}

	candidate := webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID("chrome-cft-" + lockedChromeVersion),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      version.Browser,
		Protocol:     version.ProtocolVersion,
		HTTPURL:      browserHTTPURL(browser.endpoint()),
		BrowserWSURL: version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	adapter := NewRuntime(WithEventBuffer(128), WithCommandTimeout(20*time.Second))

	// The session shape: the first tool call's bounded ctx dials the handle
	// and is canceled as soon as that call returns.
	openContext, cancelOpen := context.WithCancel(ctx)
	handleValue, err := adapter.Open(openContext, candidate)
	if err != nil {
		t.Fatalf("open adapter handle: %v", err)
	}
	t.Cleanup(func() { discardSecondaryError(handleValue.Close) })
	cancelOpen()

	// Give a lifetime regression time to surface: the old binding delivered
	// chromedp's LostConnection promptly after cancellation.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if health, ok := handleValue.(webmcp.BrowserHandleHealth); ok && health.Disconnected() {
			t.Fatalf("handle reported disconnected after opener ctx cancel; connection lifetime is still bound to the opener")
		}
		time.Sleep(100 * time.Millisecond)
	}

	targets, err := handleValue.ListTargets(ctx)
	if err != nil {
		t.Fatalf("list targets after opener ctx cancel: %v", err)
	}
	listed := slices.ContainsFunc(targets, func(candidate webmcp.Target) bool {
		return candidate.ID == webmcp.TargetID(fixtureTarget.ID)
	})
	if !listed {
		t.Fatalf("fixture target %q missing after opener ctx cancel: %+v", fixtureTarget.ID, targets)
	}

	session, err := handleValue.Attach(ctx, webmcp.TargetID(fixtureTarget.ID), webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach after opener ctx cancel: %v", err)
	}
	defer discardSecondaryError(session.Close)
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP after opener ctx cancel: %v", err)
	}
}
