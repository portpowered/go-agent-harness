package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	gateFixtureQuerySecret    = "gate-fixture-query-secret"
	gateFixtureFragmentSecret = "gate-fixture-fragment-secret"
	gateEndpointQuerySecret   = "gate-endpoint-query-secret"
	gateEndpointFragment      = "gate-endpoint-fragment"
	gateCompleteMessage       = "gate-complete"
	gateWatchMessage          = "gate-watch"
)

// TestPinnedChromeWebMCPGateI1ThroughActualBinary is the release-facing
// composition proof. It deliberately lives beside the Lane D integration
// harness so it reuses the qualified Chrome lock, flags, local fixture, and
// detach-only browser oracle without duplicating those security boundaries.
func TestPinnedChromeWebMCPGateI1ThroughActualBinary(t *testing.T) {
	// Keep this as the first observable operation. In ordinary CI this test
	// must not read the lock, make network requests, create a server, or start
	// a browser.
	if os.Getenv(chromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the actual-binary Gate I1 proof", chromeIntegrationEnv)
	}

	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	run := launchGateI1(t, ctx)
	doctor := runGateCommand(t, ctx, run.binaryPath, run.configDir, "webmcp", "doctor", "--json")
	// An unselected doctor run still proves the real endpoint and browser
	// identity. The selected doctor run below proves WebMCP/catalog readiness
	// after the exact public target IDs are known.
	doctorReport := requireGateDoctor(t, doctor)
	assertGateDoctorEndpoint(t, doctorReport, run.version)
	run.record(t, doctor)

	browserRow, tabRow := run.discoverBrowserAndTab(t, ctx)
	contextData := run.selectExactTarget(t, ctx, browserRow.ID, tabRow.TargetID)
	completeTool := run.findCompleteTool(t, ctx, browserRow.ID, tabRow.TargetID, contextData.CatalogGeneration)
	run.invokeComplete(t, ctx, completeTool.Ref)
	watchData := run.watchSecondInvocation(t, ctx, browserRow.ID, tabRow.TargetID, completeTool.Ref)
	watchOracle := run.verifyDetachedTarget(t, ctx)
	chromePID := run.closeBrowser(t)
	run.transcript = append(run.transcript, fmt.Sprintf("mutation_oracle value=%q visible=%q pending=%t invocation=%s", watchOracle.Value, watchOracle.VisibleText, watchOracle.Pending, completeToolName+":"+gateWatchMessage))
	run.transcript = append(run.transcript, fmt.Sprintf("cleanup pending_broker_invocations=0 watch_status=%s target_present_after_cli_detach=true target_responsive_after_cli_detach=true chrome_pid=%d profile=%s exact_process_only=true", watchData.Status, chromePID, "<temp>/profile"))

	for _, line := range run.transcript {
		t.Log(line)
	}
}

// gateI1Run is the actual binary, pinned browser, and sanitized transcript
// shared by the Gate I1 phases.
type gateI1Run struct {
	binaryPath string
	configDir  string
	fixture    *fixtureServer
	fixtureURL string
	browser    *runningChrome
	closed     bool
	baseURL    string
	version    devToolsVersion
	rawTarget  devToolsTarget
	transcript []string
}

func launchGateI1(t *testing.T, ctx context.Context) *gateI1Run {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	workDir := t.TempDir()
	run := &gateI1Run{configDir: filepath.Join(workDir, "config"), binaryPath: filepath.Join(workDir, "agent")}
	if err := os.Mkdir(run.configDir, 0o700); err != nil {
		t.Fatalf("create Gate I1 config directory: %v", err)
	}
	if err := buildGateBinary(ctx, root, run.binaryPath); err != nil {
		t.Fatalf("build actual agent binary: %v", err)
	}
	t.Logf("Gate I1 build: (cd agent-cli && go build -o %s ./cmd/agent)", "<temp>/agent")

	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	run.fixture = newFixtureServer()
	t.Cleanup(func() { run.fixture.Close() })
	run.fixtureURL = run.fixture.URL() + "?fixture_query=" + gateFixtureQuerySecret + "#" + gateFixtureFragmentSecret
	assertFixtureHeaders(t, ctx, run.fixtureURL)

	run.browser, err = launchPinnedChrome(ctx, pinned, run.fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if !run.closed {
			if closeErr := run.browser.Close(); closeErr != nil {
				t.Logf("Gate I1 Chrome cleanup: %v", closeErr)
			}
		}
	})

	run.baseURL = browserHTTPURL(run.browser.endpoint())
	run.version, err = waitForDevToolsVersion(ctx, run.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	run.rawTarget, err = waitForFixturePageTarget(ctx, run.baseURL, run.fixtureURL)
	if err != nil {
		t.Fatalf("discover exact fixture target: %v", err)
	}

	cdpURL := run.baseURL + "/json/version?endpoint_query=" + gateEndpointQuerySecret + "#" + gateEndpointFragment
	if err := writeGateConfig(run.configDir, cdpURL, run.fixture.server.URL); err != nil {
		t.Fatalf("write Gate I1 browser config: %v", err)
	}
	run.transcript = make([]string, 0, 12)
	run.transcript = append(run.transcript, "WEBMCP_GATE_I1_PASS")
	run.transcript = append(run.transcript, fmt.Sprintf("pinned_chrome channel=%s version=%s revision=%s platform=%s", lockedChromeChannel, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform))
	run.transcript = append(run.transcript, "fixture headers=Origin-Agent-Cluster:?1 Permissions-Policy:tools=(self) query_fragment_secrets=redacted")
	return run
}

// record checks a command's output for leaked secrets and appends its
// sanitized form to the Gate I1 transcript.
func (r *gateI1Run) record(t *testing.T, result gateCLIResult) {
	t.Helper()
	assertGateSafeOutput(t, result)
	recordGateTranscript(t, &r.transcript, result)
}

func (r *gateI1Run) discoverBrowserAndTab(t *testing.T, ctx context.Context) (gateBrowser, *gateTab) {
	t.Helper()
	browsers := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "browsers", "--json")
	browsersData := requireGateSuccessData[gateBrowsersData](t, browsers)
	if len(browsersData.Browsers) != 1 {
		t.Fatalf("browsers = %+v, want exactly one configured browser", browsersData)
	}
	browserRow := browsersData.Browsers[0]
	if browserRow.ID == "" || browserRow.Product == "" || browserRow.Protocol == "" || browserRow.Scope != loopbackAddressClass {
		t.Fatalf("browser row = %+v, want normalized loopback identity", browserRow)
	}
	expectedEndpoint := strings.TrimRight(r.baseURL, "/") + devToolsVersionPath
	if !strings.Contains(browserRow.Product, lockedChromeVersion) || browserRow.Endpoint != expectedEndpoint {
		t.Fatalf("browser row = %+v, want pinned product and redacted endpoint %q", browserRow, expectedEndpoint)
	}
	r.record(t, browsers)

	tabs := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "tabs", "--browser", browserRow.ID, "--eligible", "--json")
	tabsData := requireGateSuccessData[gateTabsData](t, tabs)
	var tabRow *gateTab
	for index := range tabsData.Tabs {
		candidate := tabsData.Tabs[index]
		if candidate.BrowserID != browserRow.ID || candidate.Type != pageTargetType || candidate.Origin != r.fixture.server.URL || !candidate.Eligible {
			continue
		}
		if tabRow != nil {
			t.Fatalf("tabs = %+v, want one eligible fixture page", tabsData.Tabs)
		}
		tabRow = &candidate
	}
	if tabRow == nil || tabRow.TargetID == "" {
		t.Fatalf("tabs = %+v, want eligible fixture page", tabsData.Tabs)
	}
	if tabRow.TargetID == r.rawTarget.ID {
		t.Fatalf("tabs exposed raw target ID %q; want normalized opaque ID", r.rawTarget.ID)
	}
	r.record(t, tabs)
	return browserRow, tabRow
}

func (r *gateI1Run) selectExactTarget(t *testing.T, ctx context.Context, browserID, targetID string) gateContext {
	t.Helper()
	selectedDoctor := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "doctor", "--browser-browser", browserID, "--browser-tab", targetID, "--json")
	selectedDoctorReport := requireGateDoctor(t, selectedDoctor)
	assertGateDoctorEndpoint(t, selectedDoctorReport, r.version)
	if selectedDoctorReport.Status != gateStatusReady || selectedDoctorReport.WebMCP != "supported" || !selectedDoctorReport.Catalog.Ready || selectedDoctorReport.SelectedPage == nil || selectedDoctorReport.SelectedPage.TargetID != targetID {
		t.Fatalf("selected doctor report = %+v, want ready WebMCP catalog for exact target", selectedDoctorReport)
	}
	r.record(t, selectedDoctor)

	selectResult := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--json")
	selectData := requireGateSuccessData[gateContext](t, selectResult)
	if selectData.BrowserID != browserID || selectData.TargetID != targetID || !selectData.Connected || !selectData.Ready || !selectData.CatalogReady || selectData.ToolCount < 1 {
		t.Fatalf("select = %+v, want connected ready exact selection", selectData)
	}
	r.record(t, selectResult)

	activate := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "activate", "--json")
	activateData := requireGateSuccessData[gateContext](t, activate)
	if activateData.BrowserID != browserID || activateData.TargetID != targetID {
		t.Fatalf("activate = %+v, want exact persisted selection", activateData)
	}
	r.record(t, activate)

	contextResult := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "context", "--json")
	contextData := requireGateSuccessData[gateContext](t, contextResult)
	if contextData.BrowserID != browserID || contextData.TargetID != targetID || !contextData.CatalogReady || contextData.CatalogGeneration == 0 || contextData.ToolCount < 1 {
		t.Fatalf("context = %+v, want rehydrated exact selection and catalog", contextData)
	}
	r.record(t, contextResult)
	return contextData
}

func (r *gateI1Run) findCompleteTool(t *testing.T, ctx context.Context, browserID, targetID string, generation uint64) *gateTool {
	t.Helper()
	toolsResult := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "tools", "--json")
	toolsData := requireGateSuccessData[gateToolsData](t, toolsResult)
	if toolsData.BrowserID != browserID || toolsData.TargetID != targetID || toolsData.Generation != generation {
		t.Fatalf("tools identity = %+v, want context browser/target/generation", toolsData)
	}
	var completeTool *gateTool
	for index := range toolsData.Tools {
		tool := toolsData.Tools[index]
		if tool.Name == completeToolName {
			completeTool = &tool
		}
	}
	if completeTool == nil || completeTool.Ref == "" || !webmcp.IsValidToolRef(webmcp.ToolRef(completeTool.Ref)) || completeTool.Frame.ID == "" || completeTool.InputSchema == nil {
		t.Fatalf("tools = %+v, want valid declarative %s ref/schema", toolsData.Tools, completeToolName)
	}
	if completeTool.Generation != toolsData.Generation || completeTool.Frame.Origin != r.fixture.server.URL {
		t.Fatalf("complete tool = %+v, want selected generation and fixture origin", completeTool)
	}
	r.record(t, toolsResult)
	return completeTool
}

func (r *gateI1Run) invokeComplete(t *testing.T, ctx context.Context, toolRef string) {
	t.Helper()
	invoke := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "invoke", "--tool-ref", toolRef, "--input-json", `{"message":"`+gateCompleteMessage+`"}`, "--timeout", "20s", "--json")
	invokeData := requireGateSuccessData[gateInvocation](t, invoke)
	if invokeData.InvocationID == "" || invokeData.ToolRef != toolRef || invokeData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("invoke = %+v, want completed result for returned ref", invokeData)
	}
	var invokeOutput map[string]any
	if err := json.Unmarshal(invokeData.Output, &invokeOutput); err != nil {
		t.Fatalf("decode invoke output: %v", err)
	}
	if invokeOutput["greeting"] != fixtureGreeting || invokeOutput["message"] != gateCompleteMessage {
		t.Fatalf("invoke output = %+v, want fixture greeting/message", invokeOutput)
	}
	r.record(t, invoke)

	completedOracleContext, cancelCompletedOracle := context.WithTimeout(ctx, 10*time.Second)
	_, err := waitForGateFixtureOracle(completedOracleContext, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+gateCompleteMessage && oracle.VisibleText == "completed:"+gateCompleteMessage && !oracle.Pending && hasFixtureInvocation(oracle, completeToolName+":"+gateCompleteMessage)
	})
	cancelCompletedOracle()
	if err != nil {
		t.Fatalf("completed mutation oracle: %v", err)
	}
}

// watchSecondInvocation runs a watch command, which owns a separate broker and
// target session, while a second invocation is issued. The watch transcript is
// the authoritative proof that its broker was attached before that invocation.
func (r *gateI1Run) watchSecondInvocation(t *testing.T, ctx context.Context, browserID, targetID, toolRef string) gateWatchData {
	t.Helper()
	watchContext, cancelWatch := context.WithCancel(ctx)
	watchProcess, err := startGateCommand(watchContext, r.binaryPath, r.configDir, "webmcp", "watch", "--timeout", "8s", "--json")
	if err != nil {
		cancelWatch()
		t.Fatalf("start watch child process: %v", err)
	}
	r.awaitWatchAttachment(t, ctx, watchProcess, cancelWatch)

	watchInvoke := runGateCommand(t, ctx, r.binaryPath, r.configDir, "webmcp", "invoke", "--tool-ref", toolRef, "--input-json", `{"message":"`+gateWatchMessage+`"}`, "--timeout", "20s", "--json")
	watchInvokeData := requireGateSuccessData[gateInvocation](t, watchInvoke)
	if watchInvokeData.InvocationID == "" || watchInvokeData.ToolRef != toolRef || watchInvokeData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("watch invocation = %+v, want completed result for returned ref", watchInvokeData)
	}
	r.record(t, watchInvoke)

	watchResult, err := watchProcess.wait(ctx)
	cancelWatch()
	if err != nil {
		t.Fatalf("wait for watch child process: %v", err)
	}
	watchData := requireGateSuccessData[gateWatchData](t, watchResult)
	if watchData.Status != invocationStatusCanceled {
		t.Fatalf("watch status = %q, want bounded canceled status", watchData.Status)
	}
	assertGateWatchSequence(t, watchData, browserID, targetID, toolRef)
	r.record(t, watchResult)
	return watchData
}

// awaitWatchAttachment keeps the target present while the watch child
// performs selection. Selection and catalog synchronization happen immediately
// after attach; the bounded settling window keeps the invocation event paired
// with the watch broker's catalog-bound reference without making the test
// depend on a fixed process startup duration alone.
func (r *gateI1Run) awaitWatchAttachment(t *testing.T, ctx context.Context, watchProcess *gateCLIProcess, cancelWatch context.CancelFunc) {
	t.Helper()
	abandon := func(format string, args ...any) {
		cancelWatch()
		watchProcess.abandon(ctx)
		t.Fatalf(format, args...)
	}
	watchTarget, err := waitForFixtureTarget(ctx, r.baseURL, webmcp.TargetID(r.rawTarget.ID), r.fixtureURL, true)
	if err != nil {
		abandon("wait for watch target presence: %v", err)
	}
	if watchTarget.ID != r.rawTarget.ID || watchTarget.URL != r.fixtureURL {
		abandon("watch target = %+v, want exact fixture target", watchTarget)
	}
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		abandon("settle watch process: %v", ctx.Err())
	}
}

func (r *gateI1Run) verifyDetachedTarget(t *testing.T, ctx context.Context) fixtureOracle {
	t.Helper()
	watchOracle, err := waitForGateFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+gateWatchMessage && oracle.VisibleText == "completed:"+gateWatchMessage && !oracle.Pending && hasFixtureInvocation(oracle, completeToolName+":"+gateWatchMessage)
	})
	if err != nil {
		t.Fatalf("watch mutation oracle: %v", err)
	}
	if pending := watchOracle.Pending; pending {
		t.Fatalf("fixture oracle still has a pending invocation: %+v", watchOracle)
	}

	if _, err := waitForFixtureTarget(ctx, r.baseURL, webmcp.TargetID(r.rawTarget.ID), r.fixtureURL, true); err != nil {
		t.Fatalf("fixture target after CLI detach: %v", err)
	}
	independent, err := inspectExternalTarget(ctx, r.browser.endpoint(), r.rawTarget.ID)
	if err != nil {
		t.Fatalf("independent page oracle after CLI detach: %v", err)
	}
	assertPageStateMatchesOracle(t, independent, watchOracle)
	if independent.URL != r.fixtureURL {
		t.Fatalf("independent page URL = %q, want fixture URL with query/fragment", independent.URL)
	}
	return watchOracle
}

// closeBrowser closes exactly the harness-owned Chrome process and returns
// its PID for the transcript.
func (r *gateI1Run) closeBrowser(t *testing.T) int {
	t.Helper()
	chromePID := 0
	if r.browser.cmd != nil && r.browser.cmd.Process != nil {
		chromePID = r.browser.cmd.Process.Pid
	}
	closeErr := r.browser.Close()
	r.closed = true
	if r.browser.done != nil {
		select {
		case <-r.browser.done:
		case <-time.After(10 * time.Second):
			t.Fatal("exact harness-owned Chrome process did not terminate within cleanup bound")
		}
	}
	if closeErr != nil {
		t.Logf("Chrome process %d exited after exact-process cleanup: %v", chromePID, closeErr)
	}
	return chromePID
}

func startGateCommand(parent context.Context, binaryPath, configDir string, args ...string) (*gateCLIProcess, error) {
	return startGateCommandWithEnvironment(parent, binaryPath, configDir, nil, args...)
}

func startGateCommandWithEnvironment(parent context.Context, binaryPath, configDir string, extraEnvironment []string, args ...string) (*gateCLIProcess, error) {
	if parent == nil {
		parent = context.Background()
	}
	commandContext, cancel := context.WithCancel(parent)
	fullArgs := append([]string{"--config-dir", configDir}, args...)
	command := exec.CommandContext(commandContext, binaryPath, fullArgs...)
	command.Dir = mustRepositoryRoot()
	command.Env = gateChildEnvironment()
	for _, extra := range extraEnvironment {
		key, _, ok := strings.Cut(extra, "=")
		if !ok || key == "" {
			continue
		}
		filtered := command.Env[:0]
		for _, value := range command.Env {
			if strings.HasPrefix(value, key+"=") {
				continue
			}
			filtered = append(filtered, value)
		}
		command.Env = append(filtered, extra)
	}
	process := &gateCLIProcess{args: fullArgs, cmd: command, done: make(chan gateCLIResult, 1), cancel: cancel}
	command.Stdout = &process.stdout
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		err := command.Wait()
		exitCode := 0
		if command.ProcessState != nil {
			exitCode = command.ProcessState.ExitCode()
		}
		process.done <- gateCLIResult{Args: append([]string(nil), process.args...), Stdout: process.stdout.String(), Stderr: process.stderr.String(), ExitCode: exitCode, Err: err}
	}()
	return process, nil
}

func (p *gateCLIProcess) wait(ctx context.Context) (gateCLIResult, error) {
	if p == nil {
		return gateCLIResult{}, errors.New("nil Gate I1 child process")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-p.done:
		p.cancel()
		return result, nil
	case <-ctx.Done():
		p.cancel()
		return gateCLIResult{}, ctx.Err()
	}
}

// abandon reaps a child process on a failure path; its exit status cannot
// change the failure already being reported.
func (p *gateCLIProcess) abandon(ctx context.Context) {
	if _, err := p.wait(context.WithoutCancel(ctx)); err != nil {
		return
	}
}

func assertGateWatchSequence(t *testing.T, data gateWatchData, browserID, targetID, toolRef string) {
	t.Helper()
	if len(data.Events) < 4 {
		t.Fatalf("watch events = %+v, want selected/catalog/invocation-created/invocation-terminal", data.Events)
	}
	lastSequence := uint64(0)
	selectedIndex, catalogIndex, createdIndex, terminalIndex := -1, -1, -1, -1
	observedInvocationID := ""
	for index, event := range data.Events {
		if event.Sequence <= lastSequence || event.BrowserID != browserID || event.TargetID != targetID {
			t.Fatalf("watch event ordering/identity = %+v, previous sequence=%d", data.Events, lastSequence)
		}
		lastSequence = event.Sequence
		switch event.Type {
		case "selected":
			if selectedIndex < 0 {
				selectedIndex = index
			}
		case "catalog_changed":
			if catalogIndex < 0 {
				catalogIndex = index
			}
		case "invocation_created":
			if event.InvocationID != "" && event.ToolRef == toolRef && observedInvocationID == "" {
				observedInvocationID = event.InvocationID
				createdIndex = index
			}
		case "invocation_terminal":
			if event.InvocationID == observedInvocationID && event.ToolRef == toolRef {
				terminalIndex = index
			}
		}
	}
	if selectedIndex < 0 || catalogIndex < 0 || createdIndex < 0 || terminalIndex < 0 || selectedIndex >= catalogIndex || catalogIndex >= createdIndex || createdIndex >= terminalIndex {
		t.Fatalf("watch semantic sequence = %+v, want selected < catalog_changed < invocation_created < invocation_terminal", data.Events)
	}
}

func hasFixtureInvocation(oracle fixtureOracle, want string) bool {
	for _, invocation := range oracle.Invocations {
		if invocation == want {
			return true
		}
	}
	return false
}

func waitForGateFixtureOracle(ctx context.Context, endpoint string, match func(fixtureOracle) bool) (fixtureOracle, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last fixtureOracle
	var lastErr error
	for {
		requestContext, cancel := context.WithTimeout(ctx, time.Second)
		oracle, err := readFixtureOracle(requestContext, endpoint)
		cancel()
		if err == nil {
			last = oracle
			if match(oracle) {
				return oracle, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("wait for Gate I1 fixture oracle: %w (last=%+v err=%v)", ctx.Err(), last, lastErr)
		case <-ticker.C:
		}
	}
}
