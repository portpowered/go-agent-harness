package targetsession

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	testBrowserPublic = "browser-public"
	testTargetPublic  = "target-public"
	testNavigationURL = "https://www.google.com/"
)

type fakeRawSession struct {
	mu        sync.Mutex
	once      sync.Once
	page      webmcp.PageContext
	tool      webmcp.ToolDescriptor
	events    chan webmcp.BrowserEvent
	done      chan struct{}
	closed    bool
	closeHits int
}

func (s *fakeRawSession) init() {
	s.once.Do(func() {
		s.events = make(chan webmcp.BrowserEvent, 4)
		s.done = make(chan struct{})
	})
}

func (s *fakeRawSession) Context() webmcp.PageContext {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page
}

func (s *fakeRawSession) Ownership() webmcp.TargetOwnership { return webmcp.TargetOwnershipExternal }

func (s *fakeRawSession) EnableWebMCP(ctx context.Context) error {
	s.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.tool.Name == "" {
		return nil
	}
	s.events <- webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: []webmcp.ToolDescriptor{s.tool}, Generation: s.page.Generation}
	return nil
}

func (s *fakeRawSession) Events() <-chan webmcp.BrowserEvent {
	s.init()
	return s.events
}

func (s *fakeRawSession) InvokeWebMCP(context.Context, webmcp.FrameID, string, json.RawMessage) (webmcp.InvocationID, error) {
	return "inv-target-session", nil
}

func (s *fakeRawSession) CancelWebMCP(context.Context, webmcp.InvocationID) error { return nil }

func (s *fakeRawSession) Done() <-chan struct{} {
	s.init()
	return s.done
}

func (s *fakeRawSession) Err() error { return nil }

func (s *fakeRawSession) Close() error {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeHits++
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.events)
	close(s.done)
	return nil
}

type screenshotRawSession struct {
	*fakeRawSession
	screenshot webmcp.PageScreenshot
}

func (s *screenshotRawSession) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if err := ctx.Err(); err != nil {
		return webmcp.PageScreenshot{}, err
	}
	return s.screenshot, nil
}

type navigationRawSession struct {
	*fakeRawSession
	navigatedURL string
}

func (s *navigationRawSession) NavigateTab(ctx context.Context, targetURL string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.navigatedURL = targetURL
	s.init()
	s.mu.Lock()
	previous := s.page.Generation
	s.page.Generation++
	s.page.URL = targetURL
	s.page.Origin = "https://www.google.com"
	current := s.page.Generation
	s.mu.Unlock()
	s.events <- webmcp.BrowserEvent{Type: webmcp.EventPageNavigated, PreviousGeneration: previous, Generation: current, Reason: "navigation"}
	return nil
}

type focusRawSession struct {
	webmcp.TargetSession
	acquired, released int
}

func (s *focusRawSession) AcquirePageFocus(context.Context) (func(context.Context) error, error) {
	s.acquired++
	return func(context.Context) error { s.released++; return nil }, nil
}

func closeSession(t *testing.T, session webmcp.TargetSession) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Errorf("close session: %v", err)
	}
}

func newRawPage(browserID webmcp.BrowserID, targetID webmcp.TargetID) *fakeRawSession {
	return &fakeRawSession{page: webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: browserID, TargetID: targetID},
		Generation: 1,
		Connected:  true,
	}}
}

func TestNewRejectsMissingRawSession(t *testing.T) {
	_, err := New(nil, webmcp.Target{})
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorTargetAttachFailed {
		t.Fatalf("New(nil) error = %v, want target attach failure", err)
	}
}

func TestSessionRebasesRawGenerationForPersistedSelection(t *testing.T) {
	raw := newRawPage("browser-a", "target-a")
	raw.tool = webmcp.ToolDescriptor{Name: "read_state", FrameID: "frame-a", InputSchema: json.RawMessage(`{"type":"object"}`)}
	session, err := New(raw, webmcp.Target{BrowserID: "browser-a", ID: "target-a", Generation: 7})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	t.Cleanup(func() { closeSession(t, session) })

	if err := session.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable production session: %v", err)
	}
	select {
	case event := <-session.Events():
		if event.Type != webmcp.EventToolsAdded || event.Generation != 7 || len(event.Tools) != 1 || event.Tools[0].Generation != 7 {
			t.Fatalf("rebased production event = %+v, want generation seven", event)
		}
		if event.BrowserID != "browser-a" || event.Tools[0].TargetID != "target-a" {
			t.Fatalf("rebased production identity = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rebased production catalog event")
	}
}

func TestSessionMapsRawScreenshotIdentity(t *testing.T) {
	raw := &screenshotRawSession{
		fakeRawSession: newRawPage("browser-raw", "raw-target"),
		screenshot: webmcp.PageScreenshot{
			BrowserID: "browser-raw",
			TargetID:  "raw-target",
			MIMEType:  "image/png",
			Bytes:     []byte{1, 2, 3},
			Width:     320,
			Height:    200,
		},
	}
	session, err := New(raw, webmcp.Target{BrowserID: testBrowserPublic, ID: testTargetPublic, Generation: 4})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	t.Cleanup(func() { closeSession(t, session) })

	capturer, ok := session.(webmcp.PageScreenshotter)
	if !ok {
		t.Fatal("production session does not expose page capture")
	}
	got, err := capturer.CapturePageScreenshot(context.Background())
	if err != nil {
		t.Fatalf("capture production screenshot: %v", err)
	}
	if got.BrowserID != testBrowserPublic || got.TargetID != testTargetPublic {
		t.Fatalf("production screenshot identity = %q/%q, want public selection", got.BrowserID, got.TargetID)
	}
	if string(got.Bytes) != string([]byte{1, 2, 3}) || got.MIMEType != "image/png" || got.Width != 320 || got.Height != 200 {
		t.Fatalf("production screenshot payload = %+v, want raw capture preserved", got)
	}
}

func TestSessionPreservesRawTabNavigation(t *testing.T) {
	raw := &navigationRawSession{fakeRawSession: newRawPage("browser-raw", "target-raw")}
	session, err := New(raw, webmcp.Target{BrowserID: testBrowserPublic, ID: testTargetPublic, Generation: 3})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	t.Cleanup(func() { closeSession(t, session) })

	navigator, ok := session.(webmcp.TargetTabNavigator)
	if !ok {
		t.Fatalf("production session %T does not preserve in-place navigation", session)
	}
	if err := navigator.NavigateTab(context.Background(), testNavigationURL); err != nil {
		t.Fatalf("navigate through production session: %v", err)
	}
	if raw.navigatedURL != testNavigationURL {
		t.Fatalf("raw navigation URL = %q", raw.navigatedURL)
	}
	got := session.Context()
	if got.URL != testNavigationURL || got.Origin != "https://www.google.com" || got.Generation != 4 {
		t.Fatalf("post-navigation context = %+v, want Google at public generation 4", got)
	}
}

func TestSessionForwardsFocusLeaseToExactRawTarget(t *testing.T) {
	raw := &focusRawSession{}
	session := &Session{raw: raw}
	release, err := session.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session.raw = &focusRawSession{} // cleanup must not look up the new target
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw.acquired != 1 || raw.released != 1 {
		t.Fatalf("raw=%+v", raw)
	}
}
