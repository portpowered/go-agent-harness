package targetsession

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestSessionRejectsUnsupportedRawCapabilities(t *testing.T) {
	raw := newRawPage("browser-raw", "target-raw")
	value, err := New(raw, webmcp.Target{BrowserID: testBrowserPublic, ID: testTargetPublic})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	session, ok := value.(*Session)
	if !ok {
		t.Fatalf("New() returned %T", value)
	}
	t.Cleanup(func() { closeSession(t, session) })
	ctx := context.Background()
	_, listErr := session.ListCastDevices(ctx)
	_, focusErr := session.AcquirePageFocus(ctx)
	_, captureErr := session.CapturePageScreenshot(ctx)
	checks := map[string]error{
		"list_cast_devices": listErr,
		"cast_tab":          session.CastTab(ctx, "tv"),
		"cast_media":        session.CastMedia(ctx, "tv"),
		"stop_casting":      session.StopCasting(ctx, "tv"),
		"navigate_tab":      session.NavigateTab(ctx, testNavigationURL),
		"focus":             focusErr,
	}
	for name, got := range checks {
		var classified *webmcp.ClassifiedError
		if !errors.As(got, &classified) || classified.Code != webmcp.ErrorBrowserProtocol {
			t.Fatalf("%s error = %v, want browser protocol classification", name, got)
		}
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(captureErr, &classified) || classified.Code != webmcp.ErrorUnsupportedWebMCP {
		t.Fatalf("screenshot error = %v, want unsupported WebMCP", captureErr)
	}
}

func TestNilSessionCapabilitiesReportClosed(t *testing.T) {
	var session *Session
	ctx := context.Background()
	_, listErr := session.ListCastDevices(ctx)
	_, focusErr := session.AcquirePageFocus(ctx)
	_, captureErr := session.CapturePageScreenshot(ctx)
	for name, got := range map[string]error{
		"list":     listErr,
		"focus":    focusErr,
		"capture":  captureErr,
		"cast":     session.CastTab(ctx, "tv"),
		"media":    session.CastMedia(ctx, "tv"),
		"stop":     session.StopCasting(ctx, "tv"),
		"navigate": session.NavigateTab(ctx, testNavigationURL),
	} {
		if !errors.Is(got, webmcp.ErrClosed) {
			t.Fatalf("%s error = %v, want ErrClosed", name, got)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatalf("nil Close() = %v", err)
	}
	if err := session.flushEvents(ctx); err != nil {
		t.Fatalf("nil flush = %v", err)
	}
}

func TestSessionAppliesAttachedMetadataAndForwardsCalls(t *testing.T) {
	raw := newRawPage("browser-raw", "target-raw")
	value, err := New(raw, webmcp.Target{
		BrowserID:            testBrowserPublic,
		ID:                   testTargetPublic,
		Title:                "Attached",
		URL:                  "https://attached.test/page",
		Origin:               "https://attached.test",
		DocumentReadyState:   "complete",
		DocumentLoading:      false,
		DocumentLoadingKnown: true,
	})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	page := value.Context()
	if page.Key.BrowserID != testBrowserPublic || page.Title != "Attached" || page.Origin != "https://attached.test" || !page.DocumentLoadingKnown || page.DocumentReadyState != "complete" {
		t.Fatalf("attached context = %+v", page)
	}
	if value.Ownership() != webmcp.TargetOwnershipExternal || value.Err() != nil {
		t.Fatalf("forwarded ownership/err = %v/%v", value.Ownership(), value.Err())
	}
	if id, invokeErr := value.InvokeWebMCP(context.Background(), "frame", "tool", nil); invokeErr != nil || id != "inv-target-session" {
		t.Fatalf("invoke = %q/%v", id, invokeErr)
	}
	if cancelErr := value.CancelWebMCP(context.Background(), "inv"); cancelErr != nil {
		t.Fatalf("cancel = %v", cancelErr)
	}
	if closeErr := value.Close(); closeErr != nil {
		t.Fatalf("close = %v", closeErr)
	}
	if closeErr := value.Close(); closeErr != nil || raw.closeHits != 1 {
		t.Fatalf("second close = %v hits=%d", closeErr, raw.closeHits)
	}
	select {
	case <-value.Done():
	default:
		t.Fatal("session Done() did not close")
	}
}

func TestPublicGenerationSaturatesAndPreservesOlderRawGenerations(t *testing.T) {
	session := &Session{rawGeneration: 5, publicGeneration: ^uint64(0) - 1}
	if got := session.publicGenerationForRawGeneration(3); got != 3 {
		t.Fatalf("older raw generation = %d, want unchanged", got)
	}
	if got := session.publicGenerationForRawGeneration(9); got != ^uint64(0) {
		t.Fatalf("overflowing generation = %d, want saturation", got)
	}
	if got := session.publicGenerationForRawGeneration(0); got != 0 {
		t.Fatalf("zero generation = %d, want zero", got)
	}
}
