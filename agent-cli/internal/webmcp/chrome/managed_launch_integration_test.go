//go:build e2e

package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// TestManagedBrowserLauncherWithStockChrome is the display-capable direct
// browser proof for story 003. The opt-in check is intentionally first so a
// normal package test never probes the host, starts Chrome, or opens a page.
func TestManagedBrowserLauncherWithStockChrome(t *testing.T) {
	chromeExecutable, version := findQualifiedStockChromeForIntegration(t)
	fixture := newManagedLaunchFixture(t)
	t.Cleanup(fixture.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser := launchManagedStockChrome(t, ctx, chromeExecutable, version, fixture.URL)

	runtimeAdapter := NewRuntime()
	handle, err := runtimeAdapter.Open(ctx, webmcp.BrowserCandidate{
		ID:           "managed-integration-browser",
		HTTPURL:      browser.Endpoint().CDPURL,
		BrowserWSURL: browser.Endpoint().BrowserWSEndpoint,
		Loopback:     true,
	})
	if err != nil {
		t.Fatalf("attach managed browser runtime: %v", err)
	}
	t.Cleanup(func() { discardSecondaryError(handle.Close) })
	second := openManagedLaunchTabs(t, ctx, handle, fixture.URL)

	session, err := handle.Attach(ctx, second.ID, webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach second managed target: %v", err)
	}
	t.Cleanup(func() { discardSecondaryError(session.Close) })
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP on opened target: %v", err)
	}
	exerciseManagedLaunchCastDiscovery(t, ctx, session)
	invokeManagedLaunchProbe(t, ctx, session)
}

func newManagedLaunchFixture(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/managed-start" && request.URL.Path != "/opened-by-agent" && request.URL.Path != "/webmcp-tool" && request.URL.Path != "/cast-navigation" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		page := "<!doctype html><title>Managed WebMCP launch</title><main>managed launch ready</main>"
		if request.URL.Path == "/webmcp-tool" {
			page = managedLaunchWebMCPFixture
		}
		if _, err := fmt.Fprint(writer, page); err != nil {
			t.Errorf("write managed launch fixture: %v", err)
		}
	}))
}

func launchManagedStockChrome(t *testing.T, ctx context.Context, chromeExecutable, version, fixtureURL string) *ManagedBrowser {
	t.Helper()
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:  t.TempDir(),
		StartupURL: fixtureURL + "/managed-start",
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
	if err := waitForManagedLaunchTarget(ctx, browser.Endpoint().CDPURL, fixtureURL+"/managed-start"); err != nil {
		t.Fatalf("wait for managed startup page: %v", err)
	}
	select {
	case <-browser.Done():
		t.Fatal("managed Chrome exited during ordinary detach proof")
	default:
	}
	if err := waitForManagedLaunchTarget(ctx, browser.Endpoint().CDPURL, fixtureURL+"/managed-start"); err != nil {
		t.Fatalf("managed startup page was not reusable after detach: %v", err)
	}
	return browser
}

// openManagedLaunchTabs opens and activates two agent tabs and returns the
// second, which serves the WebMCP fixture.
func openManagedLaunchTabs(t *testing.T, ctx context.Context, handle webmcp.BrowserHandle, fixtureURL string) webmcp.Target {
	t.Helper()
	opener, ok := handle.(webmcp.BrowserTabOpener)
	if !ok {
		t.Fatalf("managed browser handle %T does not expose tab creation", handle)
	}
	openedURL := fixtureURL + "/opened-by-agent"
	opened, err := opener.OpenTab(ctx, openedURL)
	if err != nil {
		t.Fatalf("open managed browser tab: %v", err)
	}
	if opened.ID == "" || opened.URL != openedURL {
		t.Fatalf("opened managed target = %+v", opened)
	}
	if err := handle.Activate(ctx, opened.ID); err != nil {
		t.Fatalf("activate managed browser tab: %v", err)
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		t.Fatalf("list managed targets after open: %v", err)
	}
	if !slices.ContainsFunc(targets, func(candidate webmcp.Target) bool { return candidate.ID == opened.ID && candidate.URL == openedURL }) {
		t.Fatalf("opened target %q not found in %+v", opened.ID, targets)
	}

	return openSecondManagedLaunchTab(t, ctx, handle, opener, opened, fixtureURL)
}

func openSecondManagedLaunchTab(t *testing.T, ctx context.Context, handle webmcp.BrowserHandle, opener webmcp.BrowserTabOpener, opened webmcp.Target, fixtureURL string) webmcp.Target {
	t.Helper()
	openedURL := opened.URL
	secondURL := fixtureURL + "/webmcp-tool"
	second, err := opener.OpenTab(ctx, secondURL)
	if err != nil {
		t.Fatalf("open second managed browser tab: %v", err)
	}
	if second.ID == "" || second.ID == opened.ID || second.URL != secondURL {
		t.Fatalf("second managed target = %+v, first = %+v", second, opened)
	}
	if err := handle.Activate(ctx, second.ID); err != nil {
		t.Fatalf("activate second managed browser tab: %v", err)
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		t.Fatalf("list managed targets after second open: %v", err)
	}
	foundFirst, foundSecond := false, false
	for _, candidate := range targets {
		foundFirst = foundFirst || candidate.ID == opened.ID && candidate.URL == openedURL
		foundSecond = foundSecond || candidate.ID == second.ID && candidate.URL == secondURL
	}
	if !foundFirst || !foundSecond {
		t.Fatalf("managed targets after repeated open = %+v, want both %q and %q", targets, opened.ID, second.ID)
	}
	return second
}

// exerciseManagedLaunchCastDiscovery proves the managed browser enables the
// real Chrome Cast domain on the opened target; with no receiver on the
// network the device list is empty, not an error.
func exerciseManagedLaunchCastDiscovery(t *testing.T, ctx context.Context, session webmcp.TargetSession) {
	t.Helper()
	castController, ok := session.(webmcp.TargetCastController)
	if !ok {
		t.Fatalf("managed target session %T does not expose Cast controls", session)
	}
	castDevices, err := castController.ListCastDevices(ctx)
	if err != nil {
		t.Fatalf("enable the real Chrome Cast domain: %v", err)
	}
	t.Logf("real Chrome Cast domain enabled; discovered_devices=%d names=%v", len(castDevices), castDeviceNames(castDevices))
}

func invokeManagedLaunchProbe(t *testing.T, ctx context.Context, session webmcp.TargetSession) {
	t.Helper()
	added, err := waitForIntegrationEvent(ctx, session.Events(), "managed opened-tab tools", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolsAdded && hasTool(event.Tools, "managed_open_tab_probe")
	})
	if err != nil {
		t.Fatal(err)
	}
	var probe webmcp.ToolDescriptor
	for _, candidate := range added.Tools {
		if candidate.Name == "managed_open_tab_probe" {
			probe = candidate
			break
		}
	}
	invocationID, err := session.InvokeWebMCP(ctx, probe.FrameID, probe.Name, json.RawMessage(`{"value":"actual-browser"}`))
	if err != nil {
		t.Fatalf("invoke WebMCP tool on opened target: %v", err)
	}
	completed, err := waitForIntegrationEvent(ctx, session.Events(), "managed opened-tab invocation", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.InvocationID == invocationID
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != toolStatusCompleted || !strings.Contains(string(completed.Output), "actual-browser") {
		t.Fatalf("managed opened-tab WebMCP response = %+v", completed)
	}
}

func castDeviceNames(devices []webmcp.CastDevice) []string {
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, device.Name)
	}
	return names
}

const managedLaunchWebMCPFixture = `<!doctype html>
<html><head><meta charset="utf-8"><title>Managed WebMCP tool</title></head>
<body><main id="result">ready</main><script>
(async () => {
  const context = document.modelContext || navigator.modelContext;
  if (!context || typeof context.registerTool !== "function") {
    document.querySelector("#result").textContent = "WebMCP unavailable";
    return;
  }
  await context.registerTool({
    name: "managed_open_tab_probe",
    description: "Return the supplied probe value.",
    inputSchema: {
      type: "object",
      properties: { value: { type: "string" } },
      required: ["value"],
      additionalProperties: false
    },
    execute: async (input) => {
      document.querySelector("#result").textContent = String(input.value);
      return { value: String(input.value), invoked: true };
    }
  });
})();
</script></body></html>`

func findQualifiedStockChromeForIntegration(t *testing.T) (string, string) {
	t.Helper()
	for _, candidate := range DefaultStockChromePaths(runtime.GOOS, runtime.GOARCH) {
		// Windows does not expose POSIX execute bits, and chrome.exe --version
		// opens the browser instead of writing stdout. Read its version resource
		// for opt-in stock-browser proofs without touching the user's profile.
		if runtime.GOOS == goosWindows {
			info, err := os.Stat(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-Item -LiteralPath '"+strings.ReplaceAll(candidate, "'", "''")+"').VersionInfo.ProductVersion").Output()
			cancel()
			version := strings.TrimSpace(string(output))
			major, parseErr := ParseChromeMajorVersion(version)
			if err == nil && parseErr == nil && major >= MinimumManagedChromeMajor {
				return candidate, version
			}
			continue
		}
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
	t.Fatalf("no qualified stock Chrome %d or newer is installed", MinimumManagedChromeMajor)
	return "", ""
}

func waitForManagedLaunchTarget(ctx context.Context, cdpURL, wantURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	var lastObservation string
	for {
		baseURL := strings.TrimSuffix(strings.TrimRight(cdpURL, "/"), "/json/version")
		var ready bool
		ready, lastObservation = observeManagedLaunchTarget(ctx, client, baseURL, wantURL)
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (%s)", ctx.Err(), lastObservation)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// observeManagedLaunchTarget reports whether the browser lists exactly one page
// target, at wantURL, with a description of what it observed.
func observeManagedLaunchTarget(ctx context.Context, client *http.Client, baseURL, wantURL string) (bool, string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+jsonListPath, nil)
	if err != nil {
		return false, fmt.Sprintf("request-build=%v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return false, fmt.Sprintf("request=%v", err)
	}
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&targets)
	decodeErr = errors.Join(decodeErr, response.Body.Close())
	observation := fmt.Sprintf("status=%s targets=%v decode=%v want=%q", response.Status, targets, decodeErr, wantURL)
	pageTargets, matchingPages := 0, 0
	for _, target := range targets {
		if target.Type != pageTargetType {
			continue
		}
		pageTargets++
		if target.URL == wantURL {
			matchingPages++
		}
	}
	return response.StatusCode == http.StatusOK && decodeErr == nil && pageTargets == 1 && matchingPages == 1, observation
}
