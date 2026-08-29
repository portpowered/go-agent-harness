package chrome

import (
	"context"
	"encoding/json"
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

type recordedCommand struct {
	method    string
	sessionID target.SessionID
	targetID  target.ID
}

type recordingExecutor struct {
	mu          sync.Mutex
	calls       []recordedCommand
	targetInfos []*target.Info
	err         error
}

func (e *recordingExecutor) Execute(ctx context.Context, method string, params, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	call := recordedCommand{method: method}
	switch params := params.(type) {
	case *target.DetachFromTargetParams:
		call.sessionID = params.SessionID
	case *target.CloseTargetParams:
		call.targetID = params.TargetID
	}
	e.mu.Lock()
	e.calls = append(e.calls, call)
	err := e.err
	infos := append([]*target.Info(nil), e.targetInfos...)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if result, ok := result.(*target.GetTargetsReturns); ok {
		result.TargetInfos = infos
	}
	return nil
}

func (e *recordingExecutor) snapshot() []recordedCommand {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recordedCommand(nil), e.calls...)
}

func testHandle(executor *recordingExecutor) *handle {
	return &handle{
		candidate: webmcp.BrowserCandidate{
			ID:           "browser-1",
			HTTPURL:      "http://127.0.0.1:9222",
			BrowserWSURL: "ws://127.0.0.1:9222/devtools/browser/browser-1",
			Loopback:     true,
		},
		browserExecutor: executor,
		httpClient:      http.DefaultClient,
		commandTimeout:  time.Second,
		eventBuffer:     8,
		sessions:        make(map[*targetSession]struct{}),
		done:            make(chan struct{}),
	}
}

func TestExternalSessionCloseDetachesBeforeCancelAndNeverClosesTarget(t *testing.T) {
	executor := &recordingExecutor{}
	handle := testHandle(executor)

	targetContext, rawCancel := chromedp.NewContext(context.Background())
	protocolTarget := &chromedp.Target{SessionID: "session-1", TargetID: "target-1"}
	chromedp.FromContext(targetContext).Target = protocolTarget
	var cancelCount int
	cancelTarget := func() {
		cancelCount++
		rawCancel()
	}
	session := newTargetSession(handle, targetContext, cancelTarget, webmcp.Target{
		BrowserID: handle.candidate.ID,
		ID:        "target-1",
		Type:      "page",
		Title:     "Selected tab",
		URL:       "https://example.test/selected",
	}, webmcp.TargetOwnershipExternal)
	session.setProtocolTarget(protocolTarget)
	handle.sessions[session] = struct{}{}

	if err := session.Close(); err != nil {
		t.Fatalf("close external session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent close external session: %v", err)
	}

	calls := executor.snapshot()
	if len(calls) != 1 {
		t.Fatalf("protocol calls = %+v, want one detach", calls)
	}
	if calls[0].method != target.CommandDetachFromTarget || calls[0].sessionID != "session-1" {
		t.Fatalf("detach call = %+v, want Target.detachFromTarget for session-1", calls[0])
	}
	for _, call := range calls {
		if call.method == target.CommandCloseTarget || call.method == "Browser.close" {
			t.Fatalf("external close issued destructive command: %+v", call)
		}
	}
	if data := chromedp.FromContext(targetContext); data.Target != nil {
		t.Fatal("client target reference was not cleared before cancellation")
	}
	if protocolTarget.SessionID != "" || protocolTarget.TargetID != "" {
		t.Fatalf("protocol target identifiers = %q/%q, want cleared", protocolTarget.SessionID, protocolTarget.TargetID)
	}
	if cancelCount != 1 {
		t.Fatalf("target cancellation count = %d, want 1", cancelCount)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("session Done channel is still open")
	}
}

func TestHandleCloseIsIdempotentAndPreservesExternalTarget(t *testing.T) {
	executor := &recordingExecutor{}
	handle := testHandle(executor)
	targetContext, rawCancel := chromedp.NewContext(context.Background())
	protocolTarget := &chromedp.Target{SessionID: "session-2", TargetID: "target-2"}
	chromedp.FromContext(targetContext).Target = protocolTarget
	var browserCancelCount, allocatorCancelCount int
	handle.cancelBrowser = func() { browserCancelCount++ }
	handle.cancelAllocator = func() { allocatorCancelCount++ }
	session := newTargetSession(handle, targetContext, rawCancel, webmcp.Target{
		BrowserID: handle.candidate.ID,
		ID:        "target-2",
		Type:      "page",
		URL:       "https://example.test/selected",
	}, webmcp.TargetOwnershipExternal)
	session.setProtocolTarget(protocolTarget)
	handle.sessions[session] = struct{}{}

	if err := handle.Close(); err != nil {
		t.Fatalf("first handle close: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second handle close: %v", err)
	}
	if calls := executor.snapshot(); len(calls) != 1 || calls[0].method != target.CommandDetachFromTarget {
		t.Fatalf("protocol calls after repeated handle close = %+v, want one detach", calls)
	}
	if browserCancelCount != 1 || allocatorCancelCount != 1 {
		t.Fatalf("browser/allocator cancellation counts = %d/%d, want 1/1", browserCancelCount, allocatorCancelCount)
	}
	if protocolTarget.TargetID != "" || chromedp.FromContext(targetContext).Target != nil {
		t.Fatal("external target was not fully detached from client state")
	}
}

func TestAttachUsesCallerTargetIDAndNormalizedTargetMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/list" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[
			{"id":"other-target","type":"page","title":"Other","url":"https://example.test/other","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/other-target","attached":false},
			{"id":"wanted-target","type":"page","title":"Wanted","url":"https://example.test/wanted","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/wanted-target","attached":true}
		]`))
	}))
	defer server.Close()

	executor := &recordingExecutor{}
	handle := testHandle(executor)
	handle.candidate.HTTPURL = server.URL
	handle.candidate.BrowserWSURL = "ws://127.0.0.1:9222/devtools/browser/browser-1"
	var selectedID target.ID
	protocolTarget := &chromedp.Target{SessionID: "session-wanted", TargetID: "wanted-target"}
	handle.targetOps = targetContextOps{
		newContext: func(_ context.Context, id target.ID) (context.Context, context.CancelFunc) {
			selectedID = id
			return context.WithCancel(context.Background())
		},
		listen: func(context.Context, func(any)) {},
		run:    func(context.Context, ...chromedp.Action) error { return nil },
		target: func(context.Context) *chromedp.Target { return protocolTarget },
	}

	neutralSession, err := handle.Attach(context.Background(), "wanted-target", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach wanted target: %v", err)
	}
	if selectedID != "wanted-target" {
		t.Fatalf("attach selected target ID = %q, want wanted-target", selectedID)
	}
	page := neutralSession.Context()
	if page.Key.TargetID != "wanted-target" || page.Title != "Wanted" || page.URL != "https://example.test/wanted" {
		t.Fatalf("attached page context = %+v, want exact target metadata", page)
	}
	if err := neutralSession.Close(); err != nil {
		t.Fatalf("close attached target: %v", err)
	}
	if calls := executor.snapshot(); len(calls) != 1 || calls[0].sessionID != "session-wanted" {
		t.Fatalf("detach calls = %+v, want session-wanted", calls)
	}
}

func TestAttachKeepsTargetReaderAliveUntilTransportLoss(t *testing.T) {
	handle := testHandle(&recordingExecutor{})
	readerContext := make(chan context.Context, 1)
	protocolTarget := &chromedp.Target{SessionID: "session-reader", TargetID: "target-reader"}
	handle.targetOps = targetContextOps{
		newContext: func(context.Context, target.ID) (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		listen: func(context.Context, func(any)) {},
		run: func(ctx context.Context, _ ...chromedp.Action) error {
			readerContext <- ctx
			return nil
		},
		target: func(context.Context) *chromedp.Target { return protocolTarget },
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/list" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"target-reader","type":"page","title":"Reader","url":"https://example.test/reader","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/target-reader"}]`))
	}))
	defer server.Close()
	handle.candidate.HTTPURL = server.URL

	session, err := handle.Attach(context.Background(), "target-reader", webmcp.TargetOwnershipExternal)
	if err != nil {
		t.Fatalf("attach target reader: %v", err)
	}
	select {
	case ctx := <-readerContext:
		select {
		case <-ctx.Done():
			t.Fatal("target reader context was canceled when Attach returned")
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("target reader context was not captured")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close target after reader assertion: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close handle after reader assertion: %v", err)
	}
}

func TestAttachFailureCleansUpPartialExternalAttachmentWithoutCloseTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"wanted-target","type":"page","title":"Wanted","url":"https://example.test/wanted","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/wanted-target"}]`))
	}))
	defer server.Close()

	executor := &recordingExecutor{}
	handle := testHandle(executor)
	handle.candidate.HTTPURL = server.URL
	protocolTarget := &chromedp.Target{SessionID: "partial-session", TargetID: "wanted-target"}
	attachErr := errors.New("initial target setup failed")
	var cancelCount int
	handle.targetOps = targetContextOps{
		newContext: func(_ context.Context, _ target.ID) (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
		listen: func(context.Context, func(any)) {},
		run:    func(context.Context, ...chromedp.Action) error { return attachErr },
		target: func(context.Context) *chromedp.Target { return protocolTarget },
	}
	// Replace the context constructor with one that makes cancellation
	// observable while retaining the same controlled attachment behavior.
	var targetCancel context.CancelFunc
	handle.targetOps.newContext = func(_ context.Context, _ target.ID) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		targetCancel = func() {
			cancelCount++
			cancel()
		}
		return ctx, targetCancel
	}

	_, err := handle.Attach(context.Background(), "wanted-target", webmcp.TargetOwnershipExternal)
	if err == nil {
		t.Fatal("partial attach unexpectedly succeeded")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorTargetAttachFailed {
		t.Fatalf("partial attach error = %v, want target_attach_failed", err)
	}
	if !errors.Is(err, attachErr) {
		t.Fatalf("partial attach error does not retain safe cause: %v", err)
	}
	calls := executor.snapshot()
	if len(calls) != 1 || calls[0].method != target.CommandDetachFromTarget || calls[0].sessionID != "partial-session" {
		t.Fatalf("partial cleanup calls = %+v, want one typed detach", calls)
	}
	if cancelCount != 1 || targetCancel == nil {
		t.Fatalf("partial attachment cancellation count = %d, want 1", cancelCount)
	}
	for _, call := range calls {
		if call.method == target.CommandCloseTarget || call.method == "Browser.close" {
			t.Fatalf("partial external attach issued destructive command: %+v", call)
		}
	}
}

func TestAttachMissingExactTargetDoesNotInvokeAttacher(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode([]targetInfo{{
			ID: "present-target", Type: "page", Title: "Present", URL: "https://example.test/present",
			WSURL: "ws://127.0.0.1/devtools/page/present-target",
		}})
	}))
	defer server.Close()

	handle := testHandle(&recordingExecutor{})
	handle.candidate.HTTPURL = server.URL
	var called bool
	handle.targetOps.newContext = func(context.Context, target.ID) (context.Context, context.CancelFunc) {
		called = true
		return context.WithCancel(context.Background())
	}
	_, err := handle.Attach(context.Background(), "missing-target", webmcp.TargetOwnershipExternal)
	if err == nil {
		t.Fatal("missing target unexpectedly attached")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorTargetAttachFailed {
		t.Fatalf("missing target error = %v, want target_attach_failed", err)
	}
	if called {
		t.Fatal("attacher was called after exact target lookup failed")
	}
}
