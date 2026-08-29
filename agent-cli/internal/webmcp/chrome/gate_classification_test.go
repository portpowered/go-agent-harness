package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const classificationIntegrationEnv = "WEBMCP_CLASSIFICATION_INTEGRATION"

// TestPinnedChromeWebMCPClassificationContractTwice is the live companion to
// the hermetic C0 regressions. Each iteration gets fresh browser profiles,
// config state, fixture values, and public browser/target IDs. The test is
// opt-in because it downloads the locked Chrome for Testing artifact and
// starts real browser processes.
func TestPinnedChromeWebMCPClassificationContractTwice(t *testing.T) {
	if os.Getenv(classificationIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the live classification probes", classificationIntegrationEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	workDir := t.TempDir()
	binaryPath := filepath.Join(workDir, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build current agent CLI: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	for iteration := 1; iteration <= 2; iteration++ {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			t.Logf("classification iteration=%d chrome=%s revision=%s platform=%s flags=%s profile=<fresh>", iteration, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform, "WebMCP,WebMCPTesting,DevToolsWebMCPSupport")
			runPinned := pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: t.TempDir()}
			runLiveClassificationProbe04(t, ctx, runPinned, binaryPath, iteration)
			runLiveClassificationProbe08(t, ctx, runPinned, binaryPath, iteration)
			runLiveClassificationProbe09(t, ctx, runPinned, binaryPath, iteration)
			runLiveClassificationProbe10(t, ctx, runPinned, binaryPath, iteration)
		})
	}
}

func runLiveClassificationProbe04(t *testing.T, ctx context.Context, pinned pinnedChrome, binaryPath string, iteration int) {
	t.Helper()
	runDir := filepath.Join(pinned.WorkDir, "probe-04")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("create probe 04 work directory: %v", err)
	}
	browser, err := launchPinnedChrome(ctx, pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: runDir}, "about:blank")
	if err != nil {
		t.Fatalf("probe 04 launch Chrome: %v", err)
	}
	defer func() { _ = browser.Close() }()

	baseURL := browserHTTPURL(browser.endpoint())
	if _, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion); err != nil {
		t.Fatalf("probe 04 wait for Chrome: %v", err)
	}
	configDir := filepath.Join(runDir, "config")
	cdpURL := baseURL + "/json/version?probe04=" + probe03RandomToken(t) + "#redacted"
	writeClassificationConfig(t, configDir, cdpURL, false)
	browserID := liveClassificationBrowserID(t, ctx, binaryPath, configDir, iteration, "04")
	result := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "select", "--browser", browserID, "--json")
	envelope := requireClassificationFailure(t, result, webmcp.ErrorNoEligibleTab)
	if envelope.Error.Details["browser_id"] != browserID {
		t.Fatalf("probe 04 browser ID = %#v, want %q", envelope.Error.Details["browser_id"], browserID)
	}
	if count, ok := envelope.Error.Details["candidate_count"].(float64); !ok || count < 1 {
		t.Fatalf("probe 04 candidate count = %#v, want at least one enumerated ineligible target", envelope.Error.Details["candidate_count"])
	}
	filters, ok := envelope.Error.Details["filters"].(map[string]any)
	if !ok || filters["eligible_only"] != true || filters["include_zero_tool_pages"] != true {
		t.Fatalf("probe 04 filters = %#v, want frozen eligibility filters", envelope.Error.Details["filters"])
	}
	recordClassificationResult(t, result, configDir, "probe-04", fmt.Sprintf(`{"code":%q,"browser_id":%q,"candidate_count":%v,"filters":{"eligible_only":true,"include_zero_tool_pages":true}}`, envelope.Error.Code, browserID, envelope.Error.Details["candidate_count"]))
}

func runLiveClassificationProbe08(t *testing.T, ctx context.Context, pinned pinnedChrome, binaryPath string, iteration int) {
	t.Helper()
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL() + "?probe08=" + probe03RandomToken(t)
	runDir := filepath.Join(pinned.WorkDir, "probe-08")
	initialDir := filepath.Join(runDir, "initial")
	if err := os.MkdirAll(initialDir, 0o700); err != nil {
		t.Fatalf("create probe 08 initial directory: %v", err)
	}
	initialPinned := pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: initialDir}
	initial, err := launchPinnedChrome(ctx, initialPinned, fixtureURL)
	if err != nil {
		t.Fatalf("probe 08 initial Chrome: %v", err)
	}
	baseURL := browserHTTPURL(initial.endpoint())
	if _, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion); err != nil {
		_ = initial.Close()
		t.Fatalf("probe 08 wait for initial Chrome: %v", err)
	}
	port, err := recoveryEndpointPort(initial.endpoint())
	if err != nil {
		_ = initial.Close()
		t.Fatalf("probe 08 read initial port: %v", err)
	}
	configDir := filepath.Join(runDir, "config")
	cdpURL := baseURL + "/json/version?probe08=" + probe03RandomToken(t) + "#redacted"
	writeClassificationConfig(t, configDir, cdpURL, true)
	browserID := liveClassificationBrowserID(t, ctx, binaryPath, configDir, iteration, "08-initial")
	tabs := liveClassificationTabs(t, ctx, binaryPath, configDir, browserID, iteration, "08-initial")
	if len(tabs) != 1 {
		_ = initial.Close()
		t.Fatalf("probe 08 initial eligible tabs = %+v, want one", tabs)
	}
	selected := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "select", "--browser", browserID, "--tab", tabs[0].TargetID, "--json")
	if selected.Err != nil || selected.ExitCode != 0 {
		_ = initial.Close()
		t.Fatalf("probe 08 initial selection failed: exit=%d err=%v stdout=%q", selected.ExitCode, selected.Err, selected.Stdout)
	}
	recordClassificationResult(t, selected, configDir, "probe-08-initial-selection", fmt.Sprintf(`{"code":"success","browser_id":%q,"target_id":%q}`, browserID, tabs[0].TargetID))

	if err := initial.Close(); err != nil {
		t.Fatalf("probe 08 stop initial Chrome: %v", err)
	}
	unreachable := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "context", "--json")
	unreachableEnvelope := requireClassificationFailure(t, unreachable, webmcp.ErrorBrowserDisconnected)
	if unreachableEnvelope.Error.Details["browser_id"] != browserID || unreachableEnvelope.Error.Details["target_id"] != tabs[0].TargetID || unreachableEnvelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("probe 08 disconnected details = %#v", unreachableEnvelope.Error.Details)
	}
	recordClassificationResult(t, unreachable, configDir, "probe-08-disconnected", fmt.Sprintf(`{"code":%q,"browser_id":%q,"target_id":%q,"phase":%q,"reconnect_required":true}`, unreachableEnvelope.Error.Code, browserID, tabs[0].TargetID, unreachableEnvelope.Error.Details["phase"]))

	replacementDir := filepath.Join(runDir, "replacement")
	if err := os.MkdirAll(replacementDir, 0o700); err != nil {
		t.Fatalf("create probe 08 replacement directory: %v", err)
	}
	replacement, err := launchPinnedChromeAtPort(ctx, pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: replacementDir}, fixtureURL, port)
	if err != nil {
		t.Fatalf("probe 08 replacement Chrome: %v", err)
	}
	defer func() { _ = replacement.Close() }()
	if _, err := waitForDevToolsVersion(ctx, browserHTTPURL(replacement.endpoint()), lockedChromeVersion); err != nil {
		t.Fatalf("probe 08 wait for replacement Chrome: %v", err)
	}
	stale := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "context", "--json")
	staleEnvelope := requireClassificationFailure(t, stale, webmcp.ErrorStaleSelection)
	if staleEnvelope.Error.Details["browser_id"] != browserID || staleEnvelope.Error.Details["target_id"] != tabs[0].TargetID || staleEnvelope.Error.Details["selected_generation"] == nil {
		t.Fatalf("probe 08 stale details = %#v", staleEnvelope.Error.Details)
	}
	if !strings.Contains(strings.ToLower(staleEnvelope.Error.Message), "rediscover") || !strings.Contains(strings.ToLower(staleEnvelope.Error.Message), "select") {
		t.Fatalf("probe 08 stale guidance = %q, want rediscover and explicit select", staleEnvelope.Error.Message)
	}
	recordClassificationResult(t, stale, configDir, "probe-08-fresh-identity", fmt.Sprintf(`{"code":%q,"old_browser_id":%q,"old_target_id":%q,"selected_generation":%v,"reason":%q,"replacement_work":"not_attached"}`, staleEnvelope.Error.Code, browserID, tabs[0].TargetID, staleEnvelope.Error.Details["selected_generation"], staleEnvelope.Error.Details["reason"]))
}

func runLiveClassificationProbe09(t *testing.T, ctx context.Context, pinned pinnedChrome, binaryPath string, iteration int) {
	t.Helper()
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	token := probe03RandomToken(t)
	firstURL := fixture.URL() + "?probe09=" + token + "-a"
	secondURL := fixture.URL() + "?probe09=" + token + "-b"
	runDir := filepath.Join(pinned.WorkDir, "probe-09")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("create probe 09 directory: %v", err)
	}
	browser, err := launchPinnedChrome(ctx, pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: runDir}, firstURL)
	if err != nil {
		t.Fatalf("probe 09 Chrome: %v", err)
	}
	defer func() { _ = browser.Close() }()
	baseURL := browserHTTPURL(browser.endpoint())
	if _, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion); err != nil {
		t.Fatalf("probe 09 wait for Chrome: %v", err)
	}
	if _, err := openClassificationTarget(ctx, baseURL, secondURL); err != nil {
		t.Fatalf("probe 09 open second page: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, firstURL); err != nil {
		t.Fatalf("probe 09 wait for first page: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, secondURL); err != nil {
		t.Fatalf("probe 09 wait for second page: %v", err)
	}
	configDir := filepath.Join(runDir, "config")
	cdpURL := baseURL + "/json/version?probe09=" + probe03RandomToken(t) + "#redacted"
	writeClassificationConfig(t, configDir, cdpURL, false)
	browserID := liveClassificationBrowserID(t, ctx, binaryPath, configDir, iteration, "09")
	tabs := liveClassificationTabs(t, ctx, binaryPath, configDir, browserID, iteration, "09")
	if len(tabs) != 2 {
		t.Fatalf("probe 09 eligible tabs = %+v, want two", tabs)
	}
	ids := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		ids = append(ids, tab.TargetID)
	}
	sort.Strings(ids)
	result := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "select", "--browser", browserID, "--json")
	envelope := requireClassificationFailure(t, result, webmcp.ErrorAmbiguousTab)
	candidateIDs := classificationStringList(t, envelope.Error.Details["candidate_target_ids"])
	if fmt.Sprint(candidateIDs) != fmt.Sprint(ids) {
		t.Fatalf("probe 09 candidate IDs = %v, want sorted %v", candidateIDs, ids)
	}
	if envelope.Error.Details["browser_id"] != browserID {
		t.Fatalf("probe 09 browser ID = %#v, want %q", envelope.Error.Details["browser_id"], browserID)
	}
	recordClassificationResult(t, result, configDir, "probe-09", fmt.Sprintf(`{"code":%q,"browser_id":%q,"candidate_target_ids":%s,"selection":"none"}`, envelope.Error.Code, browserID, mustJSON(t, candidateIDs)))
}

func runLiveClassificationProbe10(t *testing.T, ctx context.Context, pinned pinnedChrome, binaryPath string, iteration int) {
	t.Helper()
	readyFixture := newFixtureServer()
	t.Cleanup(readyFixture.Close)
	noTools := newClassificationNoToolsServer()
	t.Cleanup(noTools.Close)
	token := probe03RandomToken(t)
	readyURL := readyFixture.URL() + "?probe10-ready=" + token
	noToolsURL := noTools.URL + "/?probe10-unverified=" + token
	runDir := filepath.Join(pinned.WorkDir, "probe-10")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("create probe 10 directory: %v", err)
	}
	browser, err := launchPinnedChrome(ctx, pinnedChrome{Lock: pinned.Lock, Executable: pinned.Executable, WorkDir: runDir}, readyURL)
	if err != nil {
		t.Fatalf("probe 10 Chrome: %v", err)
	}
	defer func() { _ = browser.Close() }()
	baseURL := browserHTTPURL(browser.endpoint())
	if _, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion); err != nil {
		t.Fatalf("probe 10 wait for Chrome: %v", err)
	}
	if _, err := openClassificationTarget(ctx, baseURL, noToolsURL); err != nil {
		t.Fatalf("probe 10 open unverified page: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, readyURL); err != nil {
		t.Fatalf("probe 10 wait for ready page: %v", err)
	}
	if _, err := waitForFixturePageTarget(ctx, baseURL, noToolsURL); err != nil {
		t.Fatalf("probe 10 wait for unverified page: %v", err)
	}
	configDir := filepath.Join(runDir, "config")
	cdpURL := baseURL + "/json/version?probe10=" + probe03RandomToken(t) + "#redacted"
	writeClassificationConfig(t, configDir, cdpURL, false)
	browserID := liveClassificationBrowserID(t, ctx, binaryPath, configDir, iteration, "10")
	tabs := liveClassificationTabs(t, ctx, binaryPath, configDir, browserID, iteration, "10")
	readyTab, unverifiedTab := classificationTabsByOrigin(t, tabs, readyFixture.server.URL, noTools.URL)

	unselected := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "doctor", "--browser-browser", browserID, "--json")
	unselectedReport := requireClassificationDoctor(t, unselected, true)
	if unselectedReport.Status != "not_ready" || unselectedReport.PageTools != "not_checked" || unselectedReport.Catalog.Ready || unselectedReport.Catalog.Evidence != "not_checked" {
		t.Fatalf("probe 10 unselected report = %+v, want not_ready/not_checked/false", unselectedReport)
	}
	recordClassificationResult(t, unselected, configDir, "probe-10-unselected", fmt.Sprintf(`{"status":%q,"page_tools":%q,"catalog_ready":false,"catalog_evidence":%q,"selection":"none"}`, unselectedReport.Status, unselectedReport.PageTools, unselectedReport.Catalog.Evidence))

	unverified := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "doctor", "--browser-browser", browserID, "--browser-tab", unverifiedTab.TargetID, "--json")
	unverifiedReport := requireClassificationDoctor(t, unverified, false)
	if unverifiedReport.Status != "not_ready" || unverifiedReport.PageTools != "unverified" || unverifiedReport.Catalog.Ready {
		t.Fatalf("probe 10 exact unverified report = %+v, want not_ready/unverified/false", unverifiedReport)
	}
	recordClassificationResult(t, unverified, configDir, "probe-10-exact-unverified", fmt.Sprintf(`{"status":%q,"page_tools":%q,"catalog_ready":false,"target_id":%q,"error_code":%q}`, unverifiedReport.Status, unverifiedReport.PageTools, unverifiedTab.TargetID, classificationDoctorErrorCode(unverifiedReport)))

	ready := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "doctor", "--browser-browser", browserID, "--browser-tab", readyTab.TargetID, "--json")
	readyReport := requireClassificationDoctor(t, ready, true)
	if readyReport.Status != "ready" || readyReport.PageTools != "ready" || !readyReport.Catalog.Ready {
		t.Fatalf("probe 10 exact ready report = %+v, want ready/ready/true", readyReport)
	}
	recordClassificationResult(t, ready, configDir, "probe-10-exact-ready", fmt.Sprintf(`{"status":%q,"page_tools":%q,"catalog_ready":true,"target_id":%q}`, readyReport.Status, readyReport.PageTools, readyTab.TargetID))
}

func liveClassificationBrowserID(t *testing.T, ctx context.Context, binaryPath, configDir string, iteration int, probe string) string {
	t.Helper()
	result := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "browsers", "--json")
	data := requireGateSuccessData[gateBrowsersData](t, result)
	if len(data.Browsers) != 1 || data.Browsers[0].ID == "" {
		t.Fatalf("probe %s browsers = %+v, want one public browser ID", probe, data)
	}
	if !strings.Contains(data.Browsers[0].Product, lockedChromeVersion) {
		t.Fatalf("probe %s browser product = %q, want locked version", probe, data.Browsers[0].Product)
	}
	recordClassificationResult(t, result, configDir, "probe-"+probe+"-browsers", fmt.Sprintf(`{"browser_id":%q,"product":%q,"scope":%q}`, data.Browsers[0].ID, data.Browsers[0].Product, data.Browsers[0].Scope))
	return data.Browsers[0].ID
}

func liveClassificationTabs(t *testing.T, ctx context.Context, binaryPath, configDir, browserID string, iteration int, probe string) []gateTab {
	t.Helper()
	result := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "tabs", "--browser", browserID, "--eligible", "--json")
	data := requireGateSuccessData[gateTabsData](t, result)
	eligible := make([]gateTab, 0, len(data.Tabs))
	for _, tab := range data.Tabs {
		if tab.BrowserID == browserID && tab.Type == "page" && tab.Eligible {
			eligible = append(eligible, tab)
		}
	}
	if len(eligible) == 0 {
		t.Fatalf("probe %s tabs = %+v, want at least one eligible page", probe, data)
	}
	recordClassificationResult(t, result, configDir, "probe-"+probe+"-tabs", fmt.Sprintf(`{"eligible_target_ids":%s}`, mustJSON(t, classificationTabIDs(eligible))))
	return eligible
}

func writeClassificationConfig(t *testing.T, configDir, cdpURL string, persist bool) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create classification config directory: %v", err)
	}
	contents := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
    allow_remote_cdp: false
  selection:
    auto_select: off
    activate_tab: false
    persist: %t
  policy:
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 30s
`, cdpURL, persist)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write classification config: %v", err)
	}
}

func openClassificationTarget(ctx context.Context, baseURL, targetURL string) (devToolsTarget, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(baseURL, "/")+"/json/new?"+targetURL, nil)
	if err != nil {
		return devToolsTarget{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return devToolsTarget{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return devToolsTarget{}, fmt.Errorf("Chrome /json/new status: %s", response.Status)
	}
	var target devToolsTarget
	if err := json.NewDecoder(response.Body).Decode(&target); err != nil {
		return devToolsTarget{}, err
	}
	if target.ID == "" {
		return devToolsTarget{}, fmt.Errorf("Chrome /json/new returned an empty target ID")
	}
	return target, nil
}

func newClassificationNoToolsServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" || request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		_, _ = writer.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>unverified WebMCP probe page</title></head><body><p>no page tools</p></body></html>`))
	}))
}

type classificationDoctorCatalog struct {
	Ready    bool   `json:"ready"`
	Evidence string `json:"evidence"`
}

type classificationDoctorError struct {
	Code string `json:"code"`
}

type classificationDoctorReport struct {
	Status    string                      `json:"status"`
	PageTools string                      `json:"page_tools"`
	Catalog   classificationDoctorCatalog `json:"catalog"`
	Error     *classificationDoctorError  `json:"error"`
}

func requireClassificationDoctor(t *testing.T, result gateCLIResult, wantSuccess bool) classificationDoctorReport {
	t.Helper()
	if wantSuccess && (result.Err != nil || result.ExitCode != 0) {
		t.Fatalf("doctor unexpectedly failed: exit=%d err=%v stdout=%q stderr=%q", result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	if !wantSuccess && (result.Err == nil || result.ExitCode == 0) {
		t.Fatalf("doctor unexpectedly succeeded: exit=%d err=%v stdout=%q stderr=%q", result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var report classificationDoctorReport
	if err := decodeOneJSON([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("decode doctor report: %v; output=%q", err, result.Stdout)
	}
	return report
}

func classificationDoctorErrorCode(report classificationDoctorReport) string {
	if report.Error == nil {
		return "none"
	}
	return report.Error.Code
}

func requireClassificationFailure(t *testing.T, result gateCLIResult, wantCode webmcp.ErrorCode) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("classification command unexpectedly succeeded: args=%q exit=%d err=%v stdout=%q stderr=%q", result.Args, result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := decodeOneJSON([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode classification failure: %v; output=%q", err, result.Stdout)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(wantCode) {
		t.Fatalf("classification failure = %+v, want %q", envelope, wantCode)
	}
	return envelope
}

func classificationStringList(t *testing.T, value any) []string {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("classification candidate IDs = %#v, want JSON array", value)
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok || text == "" {
			t.Fatalf("classification candidate ID = %#v, want non-empty string", item)
		}
		result = append(result, text)
	}
	return result
}

func classificationTabIDs(tabs []gateTab) []string {
	ids := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		ids = append(ids, tab.TargetID)
	}
	sort.Strings(ids)
	return ids
}

func classificationTabsByOrigin(t *testing.T, tabs []gateTab, readyOrigin, noToolsOrigin string) (gateTab, gateTab) {
	t.Helper()
	var ready, noTools *gateTab
	for index := range tabs {
		tab := tabs[index]
		switch tab.Origin {
		case readyOrigin:
			ready = &tab
		case noToolsOrigin:
			noTools = &tab
		}
	}
	if ready == nil || noTools == nil {
		t.Fatalf("probe 10 tabs = %+v, want origins %q and %q", tabs, readyOrigin, noToolsOrigin)
	}
	return *ready, *noTools
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode classification transcript: %v", err)
	}
	return string(encoded)
}

func recordClassificationResult(t *testing.T, result gateCLIResult, configDir, label, summary string) {
	t.Helper()
	command := strings.Join(result.Args, " ")
	command = strings.ReplaceAll(command, configDir, "<fresh-config-dir>")
	if strings.Contains(command, "ws://") || strings.Contains(command, "wss://") {
		t.Fatalf("classification command exposed websocket transport: %q", command)
	}
	if strings.Contains(result.Stdout+result.Stderr, "ws://") || strings.Contains(result.Stdout+result.Stderr, "wss://") {
		t.Fatalf("classification output exposed websocket transport: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
	t.Logf("$ agent %s exit=%d %s", command, result.ExitCode, label+"="+summary)
}
