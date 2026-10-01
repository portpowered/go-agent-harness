//go:build e2e

package chrome

import (
	"context"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

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
	observation := time.NewTimer(3 * time.Second)
	defer observation.Stop()
	healthCheck := time.NewTicker(100 * time.Millisecond)
	defer healthCheck.Stop()
	for observing := true; observing; {
		if health, ok := handleValue.(webmcp.BrowserHandleHealth); ok && health.Disconnected() {
			t.Fatalf("handle reported disconnected after opener ctx cancel; connection lifetime is still bound to the opener")
		}
		select {
		case <-observation.C:
			observing = false
		case <-healthCheck.C:
		}
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
