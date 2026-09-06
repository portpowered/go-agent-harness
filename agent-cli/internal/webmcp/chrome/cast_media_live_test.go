package chrome

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const castMediaLiveIntegrationEnv = "WEBMCP_CAST_MEDIA_LIVE_INTEGRATION"
const castMediaLiveURLEnv = "WEBMCP_CAST_MEDIA_URL"

// TestCastMediaWithStockChromeAndPhysicalReceiver is an opt-in hardware proof
// that a real page can initiate native media playback on a real Cast sink.
func TestCastMediaWithStockChromeAndPhysicalReceiver(t *testing.T) {
	if os.Getenv(castMediaLiveIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the physical native-media Cast proof", castMediaLiveIntegrationEnv)
	}
	deviceName := strings.TrimSpace(os.Getenv(managedBrowserCastDeviceEnv))
	if deviceName == "" {
		t.Fatalf("set %s to the exact receiver name", managedBrowserCastDeviceEnv)
	}
	mediaURL := strings.TrimSpace(os.Getenv(castMediaLiveURLEnv))
	if mediaURL == "" {
		t.Fatalf("set %s to an absolute page URL containing castable media", castMediaLiveURLEnv)
	}

	chromeExecutable, version := findQualifiedStockChromeForIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:  t.TempDir(),
		StartupURL: mediaURL,
		Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
			return ChromeExecutable{Path: chromeExecutable, Version: version, Major: MinimumManagedChromeMajor, Source: ExecutableSourceStock}, nil
		}),
		DisplayAvailable: func() bool { return true },
		StartupTimeout:   20 * time.Second,
	})
	browser, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatalf("launch stock Chrome: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	if err := waitForManagedLaunchTarget(ctx, browser.Endpoint().CDPURL, mediaURL); err != nil {
		t.Fatalf("wait for media page: %v", err)
	}

	runtimeAdapter := NewRuntime()
	handle, err := runtimeAdapter.Open(ctx, webmcp.BrowserCandidate{
		ID:           "cast-media-live-browser",
		HTTPURL:      browser.Endpoint().CDPURL,
		BrowserWSURL: browser.Endpoint().BrowserWSEndpoint,
		Loopback:     true,
	})
	if err != nil {
		t.Fatalf("attach browser runtime: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		t.Fatalf("list browser targets: %v", err)
	}
	var target webmcp.Target
	for _, candidate := range targets {
		if candidate.URL == mediaURL {
			target = candidate
			break
		}
	}
	if target.ID == "" {
		t.Fatalf("media target %q was not found in %+v", mediaURL, targets)
	}
	session, err := handle.Attach(ctx, target.ID, webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach media target: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	chromeSession, ok := session.(*targetSession)
	if !ok {
		t.Fatalf("media target session = %T, want *targetSession", session)
	}
	if err := waitForLiveMediaElement(ctx, chromeSession); err != nil {
		t.Fatalf("wait for active page media: %v", err)
	}
	devices, err := chromeSession.ListCastDevices(ctx)
	if err != nil {
		t.Fatalf("list Cast devices: %v", err)
	}
	assertExpectedCastDevices(t, devices, deviceName)
	if err := chromeSession.CastMedia(ctx, deviceName); err != nil {
		t.Fatalf("cast page media to %q: %v", deviceName, err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = chromeSession.StopCasting(stopCtx, deviceName)
	})
	device, err := waitForActiveCastSession(ctx, chromeSession, deviceName)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native media Cast established: device=%q session=%q url=%q", device.Name, device.Session, mediaURL)
}

func waitForLiveMediaElement(ctx context.Context, session *targetSession) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		var ready bool
		lastErr = session.run(ctx, chromedp.Evaluate(`document.querySelector("video, audio") !== null`, &ready))
		if lastErr == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
