package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const managedBrowserReattachIntegrationEnv = "WEBMCP_MANAGED_BROWSER_REATTACH_INTEGRATION"

// TestManagedBrowserManagerWithPinnedChromeReattachesAcrossSessions is the
// credit-free real-browser proof for story 006. It deliberately uses the
// manager and the production Chrome adapter rather than a fake process, while
// keeping the provider out of the test entirely. The opt-in check must remain
// first so ordinary package tests do not locate the lock, download Chrome, or
// start a browser.
func TestManagedBrowserManagerWithPinnedChromeReattachesAcrossSessions(t *testing.T) {
	if os.Getenv(managedBrowserReattachIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome managed reattach proof", managedBrowserReattachIntegrationEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("the repository-pinned artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	workDir := t.TempDir()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	lockPath := filepath.Join(root, "scripts", "webmcp-o0", "chrome-for-testing.json")
	cacheDir := filepath.Join(workDir, "chrome-cache")

	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, ctx, fixtureURL)

	// Force this proof through the same stock-first selector and verified CFT
	// fallback used by production, but disable stock candidates so the result
	// is deterministic even on a workstation with a newer system Chrome.
	var acquisitionCalls atomic.Int32
	selector := NewManagedChromeAcquirer(ManagedChromeAcquisitionOptions{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		RequiredMajor: MinimumManagedChromeMajor,
		StockPaths:    []string{},
		LockPath:      lockPath,
		CacheDir:      cacheDir,
	})
	acquirer := ManagedChromeExecutableAcquirerFunc(func(acquireContext context.Context) (ChromeExecutable, error) {
		acquisitionCalls.Add(1)
		return selector.Acquire(acquireContext)
	})

	var launchCalls atomic.Int32
	starter := ManagedBrowserProcessStarter(func(executable string, args []string) (ManagedBrowserProcess, error) {
		launchCalls.Add(1)
		return startManagedBrowserProcess(executable, args)
	})

	newManager := func(configDir string, headless bool) *ManagedBrowserManager {
		return NewManagedBrowserManager(ManagedBrowserManagerOptions{
			ConfigDir: configDir,
			LaunchOptions: ManagedBrowserLaunchOptions{
				ConfigDir:        configDir,
				StartupURL:       fixtureURL,
				Headless:         headless,
				DisplayAvailable: func() bool { return true },
				Acquirer:         acquirer,
				ProcessStarter:   starter,
				StartupTimeout:   45 * time.Second,
				PollInterval:     100 * time.Millisecond,
				ShutdownTimeout:  10 * time.Second,
			},
			LockTimeout: 10 * time.Second,
			LockPoll:    25 * time.Millisecond,
		})
	}

	configDir := filepath.Join(workDir, "config")
	first := newManager(configDir, false).Acquire
	firstBrowser, err := first(ctx, ManagedBrowserLaunchOptions{})
	if err != nil {
		var fallbackErr *ChromeForTestingError
		if errors.As(err, &fallbackErr) {
			t.Fatalf("first managed CFT session launch: %v (fallback category=%s cause=%v)", err, fallbackErr.Category, fallbackErr.Cause)
		}
		t.Fatalf("first managed CFT session launch: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := firstBrowser.Close(); closeErr != nil {
			t.Logf("first managed browser cleanup: %v", closeErr)
		}
	})
	if firstBrowser.Headless() {
		t.Fatal("display-capable first managed session resolved to headless mode")
	}
	if firstBrowser.Executable().Source != ExecutableSourceChromeForTesting || firstBrowser.Executable().Major < MinimumManagedChromeMajor {
		t.Fatalf("first managed executable = %+v, want verified pinned Chrome %d+", firstBrowser.Executable(), MinimumManagedChromeMajor)
	}

	statePath := ManagedBrowserStatePath(configDir)
	state, present, err := readManagedBrowserState(statePath)
	if err != nil || !present {
		t.Fatalf("first managed state = %#v present=%t err=%v", state, present, err)
	}
	profileInfo, err := os.Stat(firstBrowser.ProfileDir())
	if err != nil {
		t.Fatalf("stat first managed profile: %v", err)
	}
	if !profileInfo.IsDir() || profileInfo.Mode().Perm() != 0o700 {
		t.Fatalf("first managed profile mode/type = %s/%t, want private directory", profileInfo.Mode().Perm(), profileInfo.IsDir())
	}
	stateInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat first managed state: %v", err)
	}
	if stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("managed state mode = %s, want 0600", stateInfo.Mode().Perm())
	}
	if state.PID != firstBrowser.PID() || state.ProfileDir != firstBrowser.ProfileDir() || state.ExecutablePath != firstBrowser.Executable().Path {
		t.Fatalf("managed state identity = %+v, browser pid/profile/executable = %d/%q/%q", state, firstBrowser.PID(), firstBrowser.ProfileDir(), firstBrowser.Executable().Path)
	}
	if strings.Contains(state.BrowserWSEndpoint, "?") || strings.Contains(state.BrowserWSEndpoint, "#") {
		t.Fatalf("managed state websocket retained query or fragment: %q", state.BrowserWSEndpoint)
	}

	firstTarget, firstOracle, err := runManagedPinnedFixtureSession(ctx, firstBrowser, fixture, "first")
	if err != nil {
		t.Fatalf("first managed WebMCP session: %v", err)
	}
	if firstTarget.ID == "" || !firstOracle.Ready || firstOracle.Value != "completed:first" {
		t.Fatalf("first managed session target/oracle = %+v/%+v", firstTarget, firstOracle)
	}
	if managedBrowserDone(firstBrowser.Done()) {
		t.Fatal("managed browser exited during ordinary session detach")
	}
	if _, err := waitForFixtureTarget(ctx, browserHTTPURL(firstBrowser.Endpoint().BrowserWSEndpoint), firstTarget.ID, fixtureURL, true); err != nil {
		t.Fatalf("first fixture target after ordinary detach: %v", err)
	}
	firstState, firstPresent, err := readManagedBrowserState(statePath)
	if err != nil || !firstPresent || firstState.PID != firstBrowser.PID() {
		t.Fatalf("state after ordinary detach = %#v present=%t err=%v", firstState, firstPresent, err)
	}

	// A fresh manager is the independent second session boundary. Reuse must
	// validate the real process, profile, loopback endpoint, and DevTools
	// response without calling either the executable acquirer or process starter.
	secondManager := newManager(configDir, false)
	secondBrowser, err := secondManager.Acquire(ctx, ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("second managed CFT session reattach: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := secondBrowser.Close(); closeErr != nil {
			t.Logf("second managed browser cleanup: %v", closeErr)
		}
	})
	if secondBrowser.PID() != firstBrowser.PID() || secondBrowser.ProfileDir() != firstBrowser.ProfileDir() || secondBrowser.Endpoint().CDPURL != firstBrowser.Endpoint().CDPURL {
		t.Fatalf("reattached browser identity = pid:%d profile:%q cdp:%q, first = pid:%d profile:%q cdp:%q", secondBrowser.PID(), secondBrowser.ProfileDir(), secondBrowser.Endpoint().CDPURL, firstBrowser.PID(), firstBrowser.ProfileDir(), firstBrowser.Endpoint().CDPURL)
	}
	if launchCalls.Load() != 1 || acquisitionCalls.Load() != 1 {
		t.Fatalf("second session started/acquired again = %d/%d, want one initial launch and acquisition", launchCalls.Load(), acquisitionCalls.Load())
	}
	secondTarget, secondOracle, err := runManagedPinnedFixtureSession(ctx, secondBrowser, fixture, "second")
	if err != nil {
		t.Fatalf("second managed WebMCP session: %v", err)
	}
	if secondTarget.ID != firstTarget.ID || !secondOracle.Ready || secondOracle.Value != "completed:second" {
		t.Fatalf("second managed session target/oracle = %+v/%+v, first target=%+v", secondTarget, secondOracle, firstTarget)
	}

	if err := secondBrowser.Close(); err != nil {
		t.Fatalf("close reattached managed browser: %v", err)
	}
	if !managedBrowserDone(secondBrowser.Done()) {
		t.Fatal("explicit managed close returned before the exact browser exited")
	}
	waitForManagedStateAbsent(t, statePath)
	if managedProcessAlive(secondBrowser.PID()) {
		t.Fatalf("managed process %d remains alive after exact close", secondBrowser.PID())
	}

	// The separate closure case uses explicit headless mode so the same proof
	// covers display-independent automation and verifies the close-on-exit
	// ownership operation against a second real Chrome process.
	closeConfigDir := filepath.Join(workDir, "close-config")
	closeManager := newManager(closeConfigDir, true)
	closeBrowser, err := closeManager.Acquire(ctx, ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("headless managed close-on-exit launch: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := closeBrowser.Close(); closeErr != nil {
			t.Logf("headless close-on-exit cleanup: %v", closeErr)
		}
	})
	if !closeBrowser.Headless() {
		t.Fatal("explicit headless managed close-on-exit session resolved to headful mode")
	}
	closeStatePath := ManagedBrowserStatePath(closeConfigDir)
	if _, _, err := readManagedBrowserState(closeStatePath); err != nil {
		t.Fatalf("headless close-on-exit state before close: %v", err)
	}
	closePID := closeBrowser.PID()
	if err := closeBrowser.Close(); err != nil {
		t.Fatalf("headless close-on-exit exact close: %v", err)
	}
	if !managedBrowserDone(closeBrowser.Done()) {
		t.Fatal("headless close-on-exit returned before Chrome exited")
	}
	waitForManagedStateAbsent(t, closeStatePath)
	if managedProcessAlive(closePID) {
		t.Fatalf("headless close-on-exit process %d remains alive", closePID)
	}
	if launchCalls.Load() != 2 || acquisitionCalls.Load() != 2 {
		t.Fatalf("launch/acquisition totals = %d/%d, want two real browser processes", launchCalls.Load(), acquisitionCalls.Load())
	}

	t.Logf("WEBMCP_MANAGED_BROWSER_REATTACH_PASS chrome=%s revision=%s first_mode=headful second_mode=headful close_mode=headless first_target=%s second_target=%s launch_count=%d acquisition_count=%d pid_reused=true profile_reused=true default_detach=alive exact_close=closed state_cleanup=complete", lockedChromeVersion, lockedChromeRevision, firstTarget.ID, secondTarget.ID, launchCalls.Load(), acquisitionCalls.Load())
}

func runManagedPinnedFixtureSession(ctx context.Context, browser *ManagedBrowser, fixture *fixtureServer, message string) (webmcp.Target, fixtureOracle, error) {
	if browser == nil || fixture == nil {
		return webmcp.Target{}, fixtureOracle{}, errors.New("managed fixture session inputs are unavailable")
	}
	baseURL := browserHTTPURL(browser.Endpoint().BrowserWSEndpoint)
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		return webmcp.Target{}, fixtureOracle{}, fmt.Errorf("read managed DevTools version: %w", err)
	}
	if version.WebSocketDebuggerURL != browser.Endpoint().BrowserWSEndpoint {
		return webmcp.Target{}, fixtureOracle{}, errors.New("managed DevTools websocket changed during session")
	}
	candidate := webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID("managed-cft-session"),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      version.Browser,
		Protocol:     version.ProtocolVersion,
		HTTPURL:      baseURL,
		BrowserWSURL: version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	adapter := NewRuntime(WithCommandTimeout(20 * time.Second))
	handle, err := adapter.Open(ctx, candidate)
	if err != nil {
		return webmcp.Target{}, fixtureOracle{}, fmt.Errorf("open managed WebMCP session: %w", err)
	}
	defer func() { _ = handle.Close() }()

	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return webmcp.Target{}, fixtureOracle{}, fmt.Errorf("list managed WebMCP targets: %w", err)
	}
	target, err := findFixtureTarget(targets, fixture.URL())
	if err != nil {
		return webmcp.Target{}, fixtureOracle{}, err
	}
	oracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(value fixtureOracle) bool { return value.Ready })
	if err != nil {
		return target, fixtureOracle{}, err
	}
	session, err := handle.Attach(ctx, target.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		return target, oracle, fmt.Errorf("attach managed WebMCP target: %w", err)
	}
	defer func() { _ = session.Close() }()
	if err := session.EnableWebMCP(ctx); err != nil {
		return target, oracle, fmt.Errorf("enable managed WebMCP target: %w", err)
	}
	if _, err := waitForIntegrationEvent(ctx, session.Events(), "managed target attached", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventTargetAttached
	}); err != nil {
		return target, oracle, err
	}
	added, err := waitForIntegrationEvent(ctx, session.Events(), "managed declarative tools", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolsAdded && hasTool(event.Tools, completeToolName)
	})
	if err != nil {
		return target, oracle, err
	}
	completeTool, _, _, err := findIntegrationTools(added.Tools)
	if err != nil {
		return target, oracle, err
	}
	invocationID, err := session.InvokeWebMCP(ctx, completeTool.FrameID, completeTool.Name, json.RawMessage(`{"message":"`+message+`"}`))
	if err != nil {
		return target, oracle, fmt.Errorf("invoke managed first-class page tool: %w", err)
	}
	if _, err := waitForIntegrationEvent(ctx, session.Events(), "managed tool invoked", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == invocationID
	}); err != nil {
		return target, oracle, err
	}
	responded, err := waitForIntegrationEvent(ctx, session.Events(), "managed tool responded", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.InvocationID == invocationID
	})
	if err != nil {
		return target, oracle, err
	}
	if responded.Status != "Completed" || responded.ErrorCode != "" {
		return target, oracle, fmt.Errorf("managed page tool response = %+v", responded)
	}
	var output map[string]any
	if err := json.Unmarshal(responded.Output, &output); err != nil {
		return target, oracle, fmt.Errorf("decode managed page tool output: %w", err)
	}
	if output["greeting"] != "hello" || output["message"] != message {
		return target, oracle, fmt.Errorf("managed page tool output = %+v", output)
	}
	oracle, err = waitForFixtureOracle(ctx, fixture.StateURL(), func(value fixtureOracle) bool {
		return value.Ready && value.Value == "completed:"+message && value.VisibleText == "completed:"+message && !value.Pending && hasFixtureInvocation(value, completeToolName+":"+message)
	})
	if err != nil {
		return target, oracle, err
	}
	if err := session.Close(); err != nil {
		return target, oracle, fmt.Errorf("detach managed WebMCP target: %w", err)
	}
	if err := handle.Close(); err != nil {
		return target, oracle, fmt.Errorf("close managed WebMCP handle: %w", err)
	}
	if _, err := waitForFixtureTarget(ctx, baseURL, target.ID, fixture.URL(), true); err != nil {
		return target, oracle, fmt.Errorf("verify managed target after detach: %w", err)
	}
	state, err := inspectExternalTarget(ctx, browser.Endpoint().BrowserWSEndpoint, string(target.ID))
	if err != nil {
		return target, oracle, fmt.Errorf("independent managed page oracle: %w", err)
	}
	if !state.Ready || state.Value != oracle.Value || state.VisibleText != oracle.VisibleText || state.Pending != oracle.Pending {
		return target, oracle, fmt.Errorf("managed page state = %+v, oracle = %+v", state, oracle)
	}
	return target, oracle, nil
}

func waitForManagedStateAbsent(t *testing.T, statePath string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.Stat(statePath)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed state remains after exact close: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
