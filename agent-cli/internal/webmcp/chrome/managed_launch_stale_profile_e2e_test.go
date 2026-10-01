//go:build e2e && !windows

package chrome

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestManagedBrowserManagerRecoversLiveStaleProfileOwner reproduces the
// production failure where Chrome remains alive with the agent-owned profile
// after managed-browser.json disappears. The second acquisition must prove
// ownership from Chrome's singleton metadata, stop only that exact process,
// launch a replacement, and publish a working DevTools endpoint.
func TestManagedBrowserManagerRecoversLiveStaleProfileOwner(t *testing.T) {
	chromeExecutable, version := findQualifiedStockChromeForIntegration(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeFixtureBody(writer, []byte("<!doctype html><title>Managed recovery</title><main>managed recovery ready</main>"))
	}))
	t.Cleanup(fixture.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	configDir := t.TempDir()
	options := ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: ManagedBrowserLaunchOptions{
			ConfigDir:  configDir,
			StartupURL: fixture.URL + "/stale-profile",
			Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
				return ChromeExecutable{Path: chromeExecutable, Version: version, Major: MinimumManagedChromeMajor, Source: ExecutableSourceStock}, nil
			}),
			DisplayAvailable: func() bool { return true },
			StartupTimeout:   20 * time.Second,
			ShutdownTimeout:  5 * time.Second,
		},
	}

	first, err := NewManagedBrowserManager(options).Acquire(ctx, ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("launch first managed stock Chrome: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := first.Close(); closeErr != nil {
			t.Logf("first managed Chrome cleanup: %v", closeErr)
		}
	})
	if err := waitForManagedLaunchTarget(ctx, first.Endpoint().CDPURL, fixture.URL+"/stale-profile"); err != nil {
		t.Fatalf("first managed Chrome endpoint: %v", err)
	}

	statePath := ManagedBrowserStatePath(configDir)
	firstState, present, err := readManagedBrowserState(statePath)
	if err != nil || !present || firstState.PID != first.PID() {
		t.Fatalf("first managed state = %+v present=%t err=%v", firstState, present, err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("simulate missing managed state: %v", err)
	}
	select {
	case <-first.Done():
		t.Fatal("first Chrome exited before stale-profile recovery")
	default:
	}

	second, err := NewManagedBrowserManager(options).Acquire(ctx, ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("recover live stale-profile owner: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := second.Close(); closeErr != nil {
			t.Logf("replacement managed Chrome cleanup: %v", closeErr)
		}
	})
	if second.PID() <= 0 || second.PID() == first.PID() {
		t.Fatalf("replacement PID = %d, want a live process distinct from stale PID %d", second.PID(), first.PID())
	}
	select {
	case <-first.Done():
	case <-ctx.Done():
		t.Fatalf("stale managed Chrome was not terminated: %v", ctx.Err())
	}
	if err := waitForManagedLaunchTarget(ctx, second.Endpoint().CDPURL, fixture.URL+"/stale-profile"); err != nil {
		t.Fatalf("replacement managed Chrome endpoint: %v", err)
	}
	secondState, present, err := readManagedBrowserState(statePath)
	if err != nil || !present || secondState.PID != second.PID() || secondState.CDPURL != second.Endpoint().CDPURL {
		t.Fatalf("replacement managed state = %+v present=%t err=%v", secondState, present, err)
	}
	t.Logf("WEBMCP_MANAGED_STALE_RECOVERY_PASS stale_pid=%d replacement_pid=%d loopback=true", first.PID(), second.PID())
}
