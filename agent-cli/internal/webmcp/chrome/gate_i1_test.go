package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("create Gate I1 config directory: %v", err)
	}

	binaryPath := filepath.Join(workDir, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build actual agent binary: %v", err)
	}
	t.Logf("Gate I1 build: (cd agent-cli && go build -o %s ./cmd/agent)", "<temp>/agent")

	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newFixtureServer()
	t.Cleanup(func() { fixture.Close() })
	fixtureURL := fixture.URL() + "?fixture_query=" + gateFixtureQuerySecret + "#" + gateFixtureFragmentSecret
	assertFixtureHeaders(t, ctx, fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("Gate I1 Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, baseURL, fixtureURL)
	if err != nil {
		t.Fatalf("discover exact fixture target: %v", err)
	}

	cdpURL := baseURL + "/json/version?endpoint_query=" + gateEndpointQuerySecret + "#" + gateEndpointFragment
	if err := writeGateConfig(configDir, cdpURL, fixture.server.URL); err != nil {
		t.Fatalf("write Gate I1 browser config: %v", err)
	}

	transcript := make([]string, 0, 12)
	transcript = append(transcript, "WEBMCP_GATE_I1_PASS")
	transcript = append(transcript, fmt.Sprintf("pinned_chrome channel=%s version=%s revision=%s platform=%s", lockedChromeChannel, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform))
	transcript = append(transcript, "fixture headers=Origin-Agent-Cluster:?1 Permissions-Policy:tools=(self) query_fragment_secrets=redacted")

	doctor := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "doctor", "--json")
	// An unselected doctor run still proves the real endpoint and browser
	// identity. The selected doctor run below proves WebMCP/catalog readiness
	// after the exact public target IDs are known.
	doctorReport := requireGateDoctor(t, doctor)
	assertGateDoctorEndpoint(t, doctorReport, version)
	assertGateSafeOutput(t, doctor)
	recordGateTranscript(t, &transcript, doctor)

	browsers := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "browsers", "--json")
	browsersData := requireGateSuccessData[gateBrowsersData](t, browsers)
	if len(browsersData.Browsers) != 1 {
		t.Fatalf("browsers = %+v, want exactly one configured browser", browsersData)
	}
	browserRow := browsersData.Browsers[0]
	if browserRow.ID == "" || browserRow.Product == "" || browserRow.Protocol == "" || browserRow.Scope != "loopback" {
		t.Fatalf("browser row = %+v, want normalized loopback identity", browserRow)
	}
	expectedEndpoint := strings.TrimRight(baseURL, "/") + "/json/version"
	if !strings.Contains(browserRow.Product, lockedChromeVersion) || browserRow.Endpoint != expectedEndpoint {
		t.Fatalf("browser row = %+v, want pinned product and redacted endpoint %q", browserRow, expectedEndpoint)
	}
	assertGateSafeOutput(t, browsers)
	recordGateTranscript(t, &transcript, browsers)

	tabs := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "tabs", "--browser", browserRow.ID, "--eligible", "--json")
	tabsData := requireGateSuccessData[gateTabsData](t, tabs)
	var tabRow *gateTab
	for index := range tabsData.Tabs {
		candidate := tabsData.Tabs[index]
		if candidate.BrowserID == browserRow.ID && candidate.Type == "page" && candidate.Origin == fixture.server.URL && candidate.Eligible {
			if tabRow != nil {
				t.Fatalf("tabs = %+v, want one eligible fixture page", tabsData.Tabs)
			}
			selected := candidate
			tabRow = &selected
		}
	}
	if tabRow == nil || tabRow.TargetID == "" {
		t.Fatalf("tabs = %+v, want eligible fixture page", tabsData.Tabs)
	}
	if tabRow.TargetID == rawTarget.ID {
		t.Fatalf("tabs exposed raw target ID %q; want normalized opaque ID", rawTarget.ID)
	}
	assertGateSafeOutput(t, tabs)
	recordGateTranscript(t, &transcript, tabs)

	selectedDoctor := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "doctor", "--browser-browser", browserRow.ID, "--browser-tab", tabRow.TargetID, "--json")
	selectedDoctorReport := requireGateDoctor(t, selectedDoctor)
	assertGateDoctorEndpoint(t, selectedDoctorReport, version)
	if selectedDoctorReport.Status != "ready" || selectedDoctorReport.WebMCP != "supported" || !selectedDoctorReport.Catalog.Ready || selectedDoctorReport.SelectedPage == nil || selectedDoctorReport.SelectedPage.TargetID != tabRow.TargetID {
		t.Fatalf("selected doctor report = %+v, want ready WebMCP catalog for exact target", selectedDoctorReport)
	}
	assertGateSafeOutput(t, selectedDoctor)
	recordGateTranscript(t, &transcript, selectedDoctor)

	selectResult := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "select", "--browser", browserRow.ID, "--tab", tabRow.TargetID, "--json")
	selectData := requireGateSuccessData[gateContext](t, selectResult)
	if selectData.BrowserID != browserRow.ID || selectData.TargetID != tabRow.TargetID || !selectData.Connected || !selectData.Ready || !selectData.CatalogReady || selectData.ToolCount < 1 {
		t.Fatalf("select = %+v, want connected ready exact selection", selectData)
	}
	assertGateSafeOutput(t, selectResult)
	recordGateTranscript(t, &transcript, selectResult)

	activate := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "activate", "--json")
	activateData := requireGateSuccessData[gateContext](t, activate)
	if activateData.BrowserID != browserRow.ID || activateData.TargetID != tabRow.TargetID {
		t.Fatalf("activate = %+v, want exact persisted selection", activateData)
	}
	assertGateSafeOutput(t, activate)
	recordGateTranscript(t, &transcript, activate)

	contextResult := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "context", "--json")
	contextData := requireGateSuccessData[gateContext](t, contextResult)
	if contextData.BrowserID != browserRow.ID || contextData.TargetID != tabRow.TargetID || !contextData.CatalogReady || contextData.CatalogGeneration == 0 || contextData.ToolCount < 1 {
		t.Fatalf("context = %+v, want rehydrated exact selection and catalog", contextData)
	}
	assertGateSafeOutput(t, contextResult)
	recordGateTranscript(t, &transcript, contextResult)

	toolsResult := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "tools", "--json")
	toolsData := requireGateSuccessData[gateToolsData](t, toolsResult)
	if toolsData.BrowserID != browserRow.ID || toolsData.TargetID != tabRow.TargetID || toolsData.Generation != contextData.CatalogGeneration {
		t.Fatalf("tools identity = %+v, want context browser/target/generation", toolsData)
	}
	var completeTool *gateTool
	for index := range toolsData.Tools {
		tool := toolsData.Tools[index]
		if tool.Name == completeToolName {
			selected := tool
			completeTool = &selected
		}
	}
	if completeTool == nil || completeTool.Ref == "" || !webmcp.IsValidToolRef(webmcp.ToolRef(completeTool.Ref)) || completeTool.Frame.ID == "" || completeTool.InputSchema == nil {
		t.Fatalf("tools = %+v, want valid declarative %s ref/schema", toolsData.Tools, completeToolName)
	}
	if completeTool.Generation != toolsData.Generation || completeTool.Frame.Origin != fixture.server.URL {
		t.Fatalf("complete tool = %+v, want selected generation and fixture origin", completeTool)
	}
	assertGateSafeOutput(t, toolsResult)
	recordGateTranscript(t, &transcript, toolsResult)

	invoke := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "invoke", "--tool-ref", completeTool.Ref, "--input-json", `{"message":"`+gateCompleteMessage+`"}`, "--timeout", "20s", "--json")
	invokeData := requireGateSuccessData[gateInvocation](t, invoke)
	if invokeData.InvocationID == "" || invokeData.ToolRef != completeTool.Ref || invokeData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("invoke = %+v, want completed result for returned ref", invokeData)
	}
	var invokeOutput map[string]any
	if err := json.Unmarshal(invokeData.Output, &invokeOutput); err != nil {
		t.Fatalf("decode invoke output: %v", err)
	}
	if invokeOutput["greeting"] != "hello" || invokeOutput["message"] != gateCompleteMessage {
		t.Fatalf("invoke output = %+v, want fixture greeting/message", invokeOutput)
	}
	assertGateSafeOutput(t, invoke)
	recordGateTranscript(t, &transcript, invoke)

	completedOracleContext, cancelCompletedOracle := context.WithTimeout(ctx, 10*time.Second)
	completedOracle, err := waitForGateFixtureOracle(completedOracleContext, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+gateCompleteMessage && oracle.VisibleText == "completed:"+gateCompleteMessage && !oracle.Pending && hasFixtureInvocation(oracle, completeToolName+":"+gateCompleteMessage)
	})
	cancelCompletedOracle()
	if err != nil {
		t.Fatalf("completed mutation oracle: %v", err)
	}

	// A watch command owns a separate broker and target session. Keep the
	// target present while that child performs selection; the watch transcript
	// below is the authoritative proof that its broker was attached before the
	// second invocation was issued.
	watchContext, cancelWatch := context.WithCancel(ctx)
	watchProcess, err := startGateCommand(watchContext, binaryPath, configDir, "webmcp", "watch", "--timeout", "8s", "--json")
	if err != nil {
		cancelWatch()
		t.Fatalf("start watch child process: %v", err)
	}
	watchTarget, err := waitForFixtureTarget(ctx, baseURL, webmcp.TargetID(rawTarget.ID), fixtureURL, true)
	if err != nil {
		cancelWatch()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("wait for watch target presence: %v", err)
	}
	if watchTarget.ID != rawTarget.ID || watchTarget.URL != fixtureURL {
		cancelWatch()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("watch target = %+v, want exact fixture target", watchTarget)
	}
	// Selection and catalog synchronization happen immediately after attach;
	// the bounded settling window keeps the invocation event paired with the
	// watch broker's catalog-bound reference without making the test depend on
	// a fixed process startup duration alone.
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		cancelWatch()
		_, _ = watchProcess.wait(context.Background())
		t.Fatalf("settle watch process: %v", ctx.Err())
	}

	watchInvoke := runGateCommand(t, ctx, binaryPath, configDir, "webmcp", "invoke", "--tool-ref", completeTool.Ref, "--input-json", `{"message":"`+gateWatchMessage+`"}`, "--timeout", "20s", "--json")
	watchInvokeData := requireGateSuccessData[gateInvocation](t, watchInvoke)
	if watchInvokeData.InvocationID == "" || watchInvokeData.ToolRef != completeTool.Ref || watchInvokeData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("watch invocation = %+v, want completed result for returned ref", watchInvokeData)
	}
	assertGateSafeOutput(t, watchInvoke)
	recordGateTranscript(t, &transcript, watchInvoke)

	watchResult, err := watchProcess.wait(ctx)
	cancelWatch()
	if err != nil {
		t.Fatalf("wait for watch child process: %v", err)
	}
	watchData := requireGateSuccessData[gateWatchData](t, watchResult)
	if watchData.Status != "canceled" {
		t.Fatalf("watch status = %q, want bounded canceled status", watchData.Status)
	}
	assertGateWatchSequence(t, watchData, browserRow.ID, tabRow.TargetID, completeTool.Ref)
	assertGateSafeOutput(t, watchResult)
	recordGateTranscript(t, &transcript, watchResult)

	watchOracle, err := waitForGateFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+gateWatchMessage && oracle.VisibleText == "completed:"+gateWatchMessage && !oracle.Pending && hasFixtureInvocation(oracle, completeToolName+":"+gateWatchMessage)
	})
	if err != nil {
		t.Fatalf("watch mutation oracle: %v", err)
	}
	if pending := watchOracle.Pending; pending {
		t.Fatalf("fixture oracle still has a pending invocation: %+v", watchOracle)
	}

	if _, err := waitForFixtureTarget(ctx, baseURL, webmcp.TargetID(rawTarget.ID), fixtureURL, true); err != nil {
		t.Fatalf("fixture target after CLI detach: %v", err)
	}
	independent, err := inspectExternalTarget(ctx, browser.endpoint(), rawTarget.ID)
	if err != nil {
		t.Fatalf("independent page oracle after CLI detach: %v", err)
	}
	assertPageStateMatchesOracle(t, independent, watchOracle)
	if independent.URL != fixtureURL {
		t.Fatalf("independent page URL = %q, want fixture URL with query/fragment", independent.URL)
	}

	chromePID := 0
	if browser.cmd != nil && browser.cmd.Process != nil {
		chromePID = browser.cmd.Process.Pid
	}
	closeErr := browser.Close()
	closed = true
	if browser.done != nil {
		select {
		case <-browser.done:
		case <-time.After(10 * time.Second):
			t.Fatal("exact harness-owned Chrome process did not terminate within cleanup bound")
		}
	}
	if closeErr != nil {
		t.Logf("Chrome process %d exited after exact-process cleanup: %v", chromePID, closeErr)
	}
	transcript = append(transcript, fmt.Sprintf("mutation_oracle value=%q visible=%q pending=%t invocation=%s", watchOracle.Value, watchOracle.VisibleText, watchOracle.Pending, completeToolName+":"+gateWatchMessage))
	transcript = append(transcript, fmt.Sprintf("cleanup pending_broker_invocations=0 watch_status=%s target_present_after_cli_detach=true target_responsive_after_cli_detach=true chrome_pid=%d profile=%s exact_process_only=true", watchData.Status, chromePID, "<temp>/profile"))

	for _, line := range transcript {
		t.Log(line)
	}
	_ = completedOracle
}

type gateCLIResult struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

type gateCLIProcess struct {
	args   []string
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   chan gateCLIResult
	cancel context.CancelFunc
}

type gateDoctorReport struct {
	Status       string             `json:"status"`
	Endpoint     gateDoctorEndpoint `json:"endpoint"`
	Browsers     []gateBrowser      `json:"browsers"`
	WebMCP       string             `json:"webmcp"`
	Catalog      gateDoctorCatalog  `json:"catalog"`
	SelectedPage *gateDoctorTarget  `json:"selected_page"`
}

type gateDoctorEndpoint struct {
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

type gateDoctorCatalog struct {
	Ready     bool `json:"ready"`
	ToolCount int  `json:"tool_count"`
}

type gateDoctorTarget struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
}

type gateBrowser struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Product      string `json:"product"`
	Protocol     string `json:"protocol"`
	Scope        string `json:"scope"`
	Endpoint     string `json:"endpoint"`
	HarnessOwned bool   `json:"harness_owned"`
}

type gateBrowsersData struct {
	Browsers []gateBrowser `json:"browsers"`
}

type gateTab struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Eligible  bool   `json:"eligible"`
}

type gateTabsData struct {
	Tabs []gateTab `json:"tabs"`
}

type gateContext struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Origin            string `json:"origin"`
	Generation        uint64 `json:"generation"`
	Connected         bool   `json:"connected"`
	Ready             bool   `json:"ready"`
	CatalogReady      bool   `json:"catalog_ready"`
	CatalogGeneration uint64 `json:"catalog_generation"`
	ToolCount         int    `json:"tool_count"`
}

type gateFrame struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

type gateTool struct {
	Ref         string          `json:"ref"`
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"input_schema"`
	Frame       gateFrame       `json:"frame"`
	Generation  uint64          `json:"generation"`
}

type gateToolsData struct {
	BrowserID  string     `json:"browser_id"`
	TargetID   string     `json:"target_id"`
	Generation uint64     `json:"generation"`
	Tools      []gateTool `json:"tools"`
}

type gateInvocation struct {
	InvocationID string          `json:"invocation_id"`
	ToolRef      string          `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

type gateWatchEvent struct {
	Type         string `json:"type"`
	Sequence     uint64 `json:"sequence"`
	BrowserID    string `json:"browser_id"`
	TargetID     string `json:"target_id"`
	Generation   uint64 `json:"generation"`
	InvocationID string `json:"invocation_id"`
	ToolRef      string `json:"tool_ref"`
	State        string `json:"state"`
}

type gateWatchData struct {
	Status string           `json:"status"`
	Events []gateWatchEvent `json:"events"`
}

func buildGateBinary(ctx context.Context, root, destination string) error {
	command := exec.CommandContext(ctx, "go", "build", "-o", destination, "./cmd/agent")
	command.Dir = filepath.Join(root, "agent-cli")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build output: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if info, err := os.Stat(destination); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("built agent binary is unavailable: %s", destination)
	}
	return nil
}

func writeGateConfig(configDir, cdpURL, origin string) error {
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
    persist: true
  policy:
    allowed_origins:
      - %q
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 30s
`, cdpURL, origin)
	return os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600)
}

func startGateCommand(parent context.Context, binaryPath, configDir string, args ...string) (*gateCLIProcess, error) {
	if parent == nil {
		parent = context.Background()
	}
	commandContext, cancel := context.WithCancel(parent)
	fullArgs := append([]string{"--config-dir", configDir}, args...)
	command := exec.CommandContext(commandContext, binaryPath, fullArgs...)
	command.Dir, _ = repositoryRoot()
	command.Env = gateChildEnvironment()
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

func runGateCommand(t *testing.T, parent context.Context, binaryPath, configDir string, args ...string) gateCLIResult {
	t.Helper()
	t.Logf("Gate I1 starting child: %s", strings.Join(args, " "))
	commandContext, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	process, err := startGateCommand(commandContext, binaryPath, configDir, args...)
	if err != nil {
		t.Logf("Gate I1 child failed to start: %v", err)
		return gateCLIResult{Args: append([]string(nil), args...), ExitCode: -1, Err: err}
	}
	result, waitErr := process.wait(commandContext)
	if waitErr != nil {
		result.Args = append([]string(nil), args...)
		result.ExitCode = -1
		result.Err = waitErr
	}
	t.Logf("Gate I1 child finished: %s exit=%d err=%v", strings.Join(args, " "), result.ExitCode, result.Err)
	return result
}

func gateChildEnvironment() []string {
	const (
		noProxyKey      = "NO_PROXY="
		lowerNoProxyKey = "no_proxy="
	)
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, noProxyKey) || strings.HasPrefix(value, lowerNoProxyKey) {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
}

func requireGateDoctor(t *testing.T, result gateCLIResult) gateDoctorReport {
	t.Helper()
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("Gate I1 doctor failed: exit=%d err=%v stdout=%q stderr=%q", result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var report gateDoctorReport
	if err := decodeOneJSON([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("decode Gate I1 doctor: %v; output=%q", err, result.Stdout)
	}
	return report
}

func assertGateDoctorEndpoint(t *testing.T, report gateDoctorReport, version devToolsVersion) {
	t.Helper()
	expectedAddress := strings.TrimRight(browserHTTPURL(version.WebSocketDebuggerURL), "/") + "/json/version"
	if report.Endpoint.Scope != "loopback" || report.Endpoint.Address != expectedAddress {
		t.Fatalf("doctor endpoint = %+v, want loopback redacted address", report.Endpoint)
	}
	if len(report.Browsers) != 1 || !strings.Contains(report.Browsers[0].Product, lockedChromeVersion) || report.Browsers[0].Protocol == "" {
		t.Fatalf("doctor browsers = %+v, want pinned browser/protocol", report.Browsers)
	}
}

func requireGateSuccessData[T any](t *testing.T, result gateCLIResult) T {
	t.Helper()
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("Gate I1 command failed: args=%q exit=%d err=%v stdout=%q stderr=%q", result.Args, result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := decodeOneJSON([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode Gate I1 result for %q: %v; output=%q", result.Args, err, result.Stdout)
	}
	if !envelope.OK {
		t.Fatalf("Gate I1 command returned failure for %q: %+v", result.Args, envelope.Error)
	}
	var data T
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode Gate I1 data for %q: %v; data=%s", result.Args, err, envelope.Data)
	}
	return data
}

func decodeOneJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(data)))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func assertGateSafeOutput(t *testing.T, result gateCLIResult) {
	t.Helper()
	output := result.Stdout + "\n" + result.Stderr
	for _, secret := range []string{gateFixtureQuerySecret, gateFixtureFragmentSecret, gateEndpointQuerySecret, gateEndpointFragment} {
		if strings.Contains(output, secret) {
			t.Fatalf("Gate I1 command %q exposed secret %q: stdout=%q stderr=%q", result.Args, secret, result.Stdout, result.Stderr)
		}
	}
	if strings.Contains(output, "ws://") || strings.Contains(output, "wss://") {
		t.Fatalf("Gate I1 command %q exposed a raw websocket endpoint: stdout=%q stderr=%q", result.Args, result.Stdout, result.Stderr)
	}
}

func recordGateTranscript(t *testing.T, transcript *[]string, result gateCLIResult) {
	t.Helper()
	if transcript == nil {
		return
	}
	command := strings.Join(result.Args, " ")
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = "<empty>"
	}
	*transcript = append(*transcript, fmt.Sprintf("$ agent %s\n%s", command, output))
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
