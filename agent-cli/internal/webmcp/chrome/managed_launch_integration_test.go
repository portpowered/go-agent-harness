package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

const managedBrowserLaunchIntegrationEnv = "WEBMCP_MANAGED_BROWSER_LAUNCH_INTEGRATION"

// TestManagedBrowserLauncherWithStockChrome is the display-capable direct
// browser proof for story 003. The opt-in check is intentionally first so a
// normal package test never probes the host, starts Chrome, or opens a page.
func TestManagedBrowserLauncherWithStockChrome(t *testing.T) {
	if os.Getenv(managedBrowserLaunchIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the real stock-Chrome managed-launch proof", managedBrowserLaunchIntegrationEnv)
	}

	chromeExecutable, version := findQualifiedStockChromeForIntegration(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/managed-start" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(writer, "<!doctype html><title>Managed WebMCP launch</title><main>managed launch ready</main>")
	}))
	t.Cleanup(fixture.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:  t.TempDir(),
		StartupURL: fixture.URL + "/managed-start",
		Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
			return ChromeExecutable{Path: chromeExecutable, Version: version, Major: MinimumManagedChromeMajor, Source: ExecutableSourceStock}, nil
		}),
		DisplayAvailable: func() bool { return true },
		StartupTimeout:   20 * time.Second,
	})
	browser, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatalf("managed stock Chrome launch: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("managed stock Chrome cleanup: %v", closeErr)
		}
	})

	if browser.Headless() {
		t.Fatal("display-capable managed launch resolved to headless mode")
	}
	if browser.Executable().Major < MinimumManagedChromeMajor {
		t.Fatalf("launched Chrome major = %d, want at least %d", browser.Executable().Major, MinimumManagedChromeMajor)
	}
	if err := waitForManagedLaunchTarget(ctx, browser.Endpoint().CDPURL, fixture.URL+"/managed-start"); err != nil {
		t.Fatalf("wait for managed startup page: %v", err)
	}
	select {
	case <-browser.Done():
		t.Fatal("managed Chrome exited during ordinary detach proof")
	default:
	}
	if err := waitForManagedLaunchTarget(ctx, browser.Endpoint().CDPURL, fixture.URL+"/managed-start"); err != nil {
		t.Fatalf("managed startup page was not reusable after detach: %v", err)
	}
}

func findQualifiedStockChromeForIntegration(t *testing.T) (string, string) {
	t.Helper()
	for _, candidate := range DefaultStockChromePaths(runtime.GOOS, runtime.GOARCH) {
		if err := checkChromeExecutable(candidate); err != nil {
			continue
		}
		version, err := queryChromeVersion(context.Background(), candidate)
		if err != nil {
			continue
		}
		major, err := ParseChromeMajorVersion(version)
		if err == nil && major >= MinimumManagedChromeMajor {
			return candidate, strings.TrimSpace(version)
		}
	}
	t.Skipf("no qualified stock Chrome %d or newer is installed", MinimumManagedChromeMajor)
	return "", ""
}

func waitForManagedLaunchTarget(ctx context.Context, cdpURL, wantURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	lastObservation := "no response"
	for {
		baseURL := strings.TrimSuffix(strings.TrimRight(cdpURL, "/"), "/json/version")
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/list", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var targets []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				}
				decodeErr := json.NewDecoder(response.Body).Decode(&targets)
				_ = response.Body.Close()
				lastObservation = fmt.Sprintf("status=%s targets=%v decode=%v want=%q", response.Status, targets, decodeErr, wantURL)
				pageTargets := 0
				matchingPages := 0
				for _, target := range targets {
					if target.Type != "page" {
						continue
					}
					pageTargets++
					if target.URL == wantURL {
						matchingPages++
					}
				}
				if response.StatusCode == http.StatusOK && decodeErr == nil && pageTargets == 1 && matchingPages == 1 {
					return nil
				}
			} else {
				lastObservation = fmt.Sprintf("request=%v", requestErr)
			}
		} else {
			lastObservation = fmt.Sprintf("request-build=%v", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (%s)", ctx.Err(), lastObservation)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
