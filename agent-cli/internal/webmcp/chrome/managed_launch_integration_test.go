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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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
		if request.URL.Path != "/managed-start" && request.URL.Path != "/opened-by-agent" && request.URL.Path != "/webmcp-tool" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		if request.URL.Path == "/webmcp-tool" {
			_, _ = fmt.Fprint(writer, managedLaunchWebMCPFixture)
			return
		}
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
	t.Cleanup(func() { _ = handle.Close() })
	opener, ok := handle.(webmcp.BrowserTabOpener)
	if !ok {
		t.Fatalf("managed browser handle %T does not expose tab creation", handle)
	}
	openedURL := fixture.URL + "/opened-by-agent"
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
	found := false
	for _, candidate := range targets {
		if candidate.ID == opened.ID && candidate.URL == openedURL {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("opened target %q not found in %+v", opened.ID, targets)
	}

	secondURL := fixture.URL + "/webmcp-tool"
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
	targets, err = handle.ListTargets(ctx)
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

	session, err := handle.Attach(ctx, second.ID, webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach second managed target: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP on opened target: %v", err)
	}
	castController, ok := session.(webmcp.TargetCastController)
	if !ok {
		t.Fatalf("managed target session %T does not expose Cast controls", session)
	}
	castDevices, err := castController.ListCastDevices(ctx)
	if err != nil {
		t.Fatalf("enable the real Chrome Cast domain: %v", err)
	}
	t.Logf("real Chrome Cast domain enabled; discovered_devices=%d", len(castDevices))
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
	if completed.Status != "Completed" || !strings.Contains(string(completed.Output), "actual-browser") {
		t.Fatalf("managed opened-tab WebMCP response = %+v", completed)
	}
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
