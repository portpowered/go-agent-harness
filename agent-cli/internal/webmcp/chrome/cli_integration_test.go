package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const cliChromeIntegrationEnv = "WEBMCP_CLI_CHROME_INTEGRATION"
const cliSelectDeathIntegrationEnv = "WEBMCP_CLI_SELECT_DEATH_INTEGRATION"

// TestWebMCPDirectCLIWithPinnedChromeCrossProcessCancel exercises the shipped
// CLI binary against the actual pinned Chrome/WebMCP fixture. The invoke and
// cancel commands are separate OS processes and share only persisted
// selection metadata plus the browser-owned invocation state.
func TestWebMCPDirectCLIWithPinnedChromeCrossProcessCancel(t *testing.T) {
	if os.Getenv(cliChromeIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome CLI integration proof", cliChromeIntegrationEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run := launchCLIChromeIntegration(t, ctx, false, "Chrome cleanup")
	binary := buildCLIChromeIntegrationBinary(t, ctx, run.workDir)
	configDir := writeCLIChromeIntegrationConfig(t, run.workDir, run.baseURL)
	browserID, targetID := selectCLIChromeIntegrationTarget(t, binary, configDir, "pinned Chrome")
	toolRefs := listCLIChromeIntegrationToolRefs(t, binary, configDir)

	receipt, cancelOutcome := run.assertControlledCancel(t, ctx, binary, configDir, toolRefs[cancelToolName])
	declarative := run.runDeclarativeCancel(t, ctx, binary, configDir, toolRefs[slowToolName])
	recoveredStatus := assertCLIChromeRecovery(t, binary, configDir, toolRefs[completeToolName])

	t.Logf("WEBMCP_DIRECT_CLI_INTEGRATION_PASS chrome=%s revision=%s browser=%s target=%s controlled_receipt=%s controlled_cancel=%s controlled_oracle=canceled declarative_receipt=%s declarative_outcome=%s declarative_terminal_observed=%t declarative_oracle_pending=%t recovery=%s", lockedChromeVersion, lockedChromeRevision, browserID, targetID, receipt.InvocationID, cancelOutcome, declarative.receiptID, declarative.outcome, declarative.terminalObserved, declarative.oracle.Pending, recoveredStatus)
}

// cliChromeIntegrationRun is the pinned browser and fixture shared by the
// direct CLI live proofs.
type cliChromeIntegrationRun struct {
	workDir    string
	fixture    *fixtureServer
	fixtureURL string
	browser    *runningChrome
	baseURL    string
	killed     bool
}

func launchCLIChromeIntegration(t *testing.T, ctx context.Context, assertHeaders bool, cleanupLabel string) *cliChromeIntegrationRun {
	t.Helper()
	run := &cliChromeIntegrationRun{workDir: t.TempDir()}
	pinned, err := acquirePinnedChrome(ctx, run.workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	run.fixture = newFixtureServer()
	t.Cleanup(run.fixture.Close)
	run.fixtureURL = run.fixture.URL()
	if assertHeaders {
		assertFixtureHeaders(t, ctx, run.fixtureURL)
	}
	run.browser, err = launchPinnedChrome(ctx, pinned, run.fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if run.killed {
			return
		}
		if closeErr := run.browser.Close(); closeErr != nil {
			t.Logf("%s: %v", cleanupLabel, closeErr)
		}
	})

	run.baseURL = browserHTTPURL(run.browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, run.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if version.WebSocketDebuggerURL != run.browser.endpoint() {
		t.Fatalf("DevTools websocket = %q, launch announcement = %q", version.WebSocketDebuggerURL, run.browser.endpoint())
	}
	return run
}

func buildCLIChromeIntegrationBinary(t *testing.T, ctx context.Context, workDir string) string {
	t.Helper()
	binary := filepath.Join(workDir, "agent")
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./agent-cli/cmd/agent")
	build.Dir = root
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build agent CLI: %v\n%s", buildErr, output)
	}
	return binary
}

func writeCLIChromeIntegrationConfig(t *testing.T, workDir, cdpBaseURL string) string {
	t.Helper()
	configDir := filepath.Join(workDir, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("create CLI config directory: %v", err)
	}
	configYAML := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    persist: true
  policy:
    cancel_on_interrupt: always
`, strings.TrimRight(cdpBaseURL, "/")+devToolsVersionPath)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write CLI integration config: %v", err)
	}
	return configDir
}

// selectCLIChromeIntegrationTarget discovers the single browser and eligible
// tab through the CLI and persists that selection.
func selectCLIChromeIntegrationTarget(t *testing.T, binary, configDir, label string) (string, string) {
	t.Helper()
	browsers := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "browsers", "--json")
	browsersEnvelope := requireCLIChromeIntegrationSuccess(t, browsers)
	var browsersData struct {
		Browsers []struct {
			ID string `json:"id"`
		} `json:"browsers"`
	}
	decodeCLIChromeIntegrationData(t, browsersEnvelope.Data, &browsersData)
	if len(browsersData.Browsers) != 1 || browsersData.Browsers[0].ID == "" {
		t.Fatalf("%s browser discovery = %+v", label, browsersData)
	}
	browserID := browsersData.Browsers[0].ID

	tabs := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "tabs", "--browser", browserID, "--eligible", "--json")
	tabsEnvelope := requireCLIChromeIntegrationSuccess(t, tabs)
	var tabsData struct {
		Tabs []struct {
			TargetID string `json:"target_id"`
			Origin   string `json:"origin"`
		} `json:"tabs"`
	}
	decodeCLIChromeIntegrationData(t, tabsEnvelope.Data, &tabsData)
	if len(tabsData.Tabs) != 1 || tabsData.Tabs[0].TargetID == "" || tabsData.Tabs[0].Origin == "" {
		t.Fatalf("%s target discovery = %+v", label, tabsData)
	}
	targetID := tabsData.Tabs[0].TargetID

	selected := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--json")
	requireCLIChromeIntegrationSuccess(t, selected)
	return browserID, targetID
}

func listCLIChromeIntegrationToolRefs(t *testing.T, binary, configDir string) map[string]string {
	t.Helper()
	tools := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "tools", "--json")
	toolsEnvelope := requireCLIChromeIntegrationSuccess(t, tools)
	var toolsData struct {
		Tools []struct {
			Ref  string `json:"ref"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	decodeCLIChromeIntegrationData(t, toolsEnvelope.Data, &toolsData)
	toolRefs := map[string]string{}
	for _, tool := range toolsData.Tools {
		toolRefs[tool.Name] = tool.Ref
	}
	if toolRefs[cancelToolName] == "" || toolRefs[completeToolName] == "" || toolRefs[slowToolName] == "" {
		t.Fatalf("pinned Chrome tools omitted cancellation, recovery, or slow declarative tools: %+v", toolsData)
	}
	return toolRefs
}

// awaitCLIChromeReceipt reads the dispatch receipt an invoke process writes
// as its first stderr line and checks it names the dispatched tool.
func awaitCLIChromeReceipt(t *testing.T, ctx context.Context, process *cliChromeIntegrationProcess, toolRef, subject string, timeout time.Duration) cliChromeIntegrationReceipt {
	t.Helper()
	var receipt cliChromeIntegrationReceipt
	select {
	case line := <-process.stderr.firstLine:
		if err := json.Unmarshal([]byte(line), &receipt); err != nil {
			t.Fatalf("decode %s dispatch receipt: %v; stderr=%q", subject, err, line)
		}
	case <-ctx.Done():
		t.Fatalf("wait for %s dispatch receipt: %v", subject, ctx.Err())
	case <-time.After(timeout):
		t.Fatalf("%s invoke did not emit a dispatch receipt", subject)
	}
	if receipt.InvocationID == "" || receipt.ToolRef != toolRef || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("%s dispatch receipt = %+v", subject, receipt)
	}
	return receipt
}

type cliChromeCancelData struct {
	InvocationID string `json:"invocation_id"`
	Status       string `json:"status"`
	Phase        string `json:"phase"`
	Outcome      string `json:"outcome"`
}

func (r *cliChromeIntegrationRun) assertControlledCancel(t *testing.T, ctx context.Context, binary, configDir, toolRef string) (cliChromeIntegrationReceipt, string) {
	t.Helper()
	invoke := startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRef, "--input-json", `{"message":"live-hold"}`, "--timeout", "30s", "--json")
	receipt := awaitCLIChromeReceipt(t, ctx, invoke, toolRef, "pinned Chrome", 15*time.Second)
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Pending && oracle.Value == "pending:live-hold"
	}); err != nil {
		t.Fatalf("wait for pinned Chrome pending page state: %v", err)
	}

	cancelProcess := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "cancel", "--invocation", receipt.InvocationID, "--json")
	if cancelProcess.err != nil {
		t.Fatalf("pinned Chrome cancel process: %v\nstdout=%s\nstderr=%s", cancelProcess.err, cancelProcess.stdout, cancelProcess.stderr)
	}
	var cancelData cliChromeCancelData
	cancelEnvelope := requireCLIChromeIntegrationSuccess(t, cancelProcess)
	decodeCLIChromeIntegrationData(t, cancelEnvelope.Data, &cancelData)
	if cancelData.InvocationID != receipt.InvocationID || cancelData.Status != invocationStatusCanceled || cancelData.Phase != "terminal" || cancelData.Outcome != "confirmed_canceled" {
		t.Fatalf("pinned Chrome cancel result = %+v", cancelData)
	}

	invokeErr := invoke.Wait()
	if invokeErr == nil {
		t.Fatal("pinned Chrome invoke exited successfully after cross-process cancellation")
	}
	invokeEnvelope := decodeCLIChromeIntegrationEnvelope(t, invoke.stdout.String())
	if invokeEnvelope.OK || invokeEnvelope.Error == nil || invokeEnvelope.Error.Code != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("pinned Chrome canceled invoke envelope = %+v", invokeEnvelope)
	}
	if invokeEnvelope.Error.Details["invocation_id"] != receipt.InvocationID {
		t.Fatalf("pinned Chrome canceled invocation ID = %#v, want %q", invokeEnvelope.Error.Details["invocation_id"], receipt.InvocationID)
	}
	if !strings.HasPrefix(invoke.stderr.String(), receiptLineForCLIChrome(receipt)) {
		t.Fatalf("pinned Chrome invoke stderr did not start with receipt: %q", invoke.stderr.String())
	}
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return slices.Contains(oracle.Invocations, "canceled:"+cancelToolName)
	}); err != nil {
		t.Fatalf("wait for pinned Chrome cancellation event: %v", err)
	}
	return receipt, cancelData.Outcome
}

// cliChromeDeclarativeCancel is the classification of a cancel request
// against the slow declarative tool.
type cliChromeDeclarativeCancel struct {
	receiptID        string
	outcome          string
	terminalObserved bool
	oracle           fixtureOracle
	slowInvoke       *cliChromeIntegrationProcess
	finished         bool
}

const (
	cliChromeSlowMessage        = "declarative-hold"
	cliChromeOutcomeUnconfirmed = "cancellation_unconfirmed"
)

// runDeclarativeCancel cancels the slow declarative form. Its timer makes the
// browser's behavior observable: Chrome may cancel it, report completion
// anyway, or fail to provide a correlated terminal event. The test accepts
// each documented classification, but never treats dispatch alone as
// success.
func (r *cliChromeIntegrationRun) runDeclarativeCancel(t *testing.T, ctx context.Context, binary, configDir, toolRef string) *cliChromeDeclarativeCancel {
	t.Helper()
	declarative := &cliChromeDeclarativeCancel{}
	declarative.slowInvoke = startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRef, "--input-json", `{"message":"`+cliChromeSlowMessage+`"}`, "--timeout", "10s", "--json")
	defer func() {
		if !declarative.finished {
			declarative.slowInvoke.stop()
		}
	}()
	slowReceipt := awaitCLIChromeReceipt(t, ctx, declarative.slowInvoke, toolRef, "pinned Chrome slow declarative", 10*time.Second)
	declarative.receiptID = slowReceipt.InvocationID
	if _, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Pending && oracle.Value == "pending:"+cliChromeSlowMessage && hasFixtureInvocation(oracle, slowToolName+":"+cliChromeSlowMessage)
	}); err != nil {
		t.Fatalf("wait for pinned Chrome slow declarative pending page state: %v", err)
	}

	declarativeCancel := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "cancel", "--invocation", slowReceipt.InvocationID, "--timeout", "8s", "--json")
	declarativeEnvelope := decodeCLIChromeIntegrationEnvelope(t, declarativeCancel.stdout)
	switch {
	case declarativeEnvelope.OK:
		r.confirmDeclarativeCancel(t, ctx, declarative, declarativeCancel, declarativeEnvelope)
	case !declarativeEnvelope.OK && declarativeEnvelope.Error != nil:
		r.classifyDeclarativeCancel(t, ctx, declarative, declarativeCancel, declarativeEnvelope)
	default:
		t.Fatalf("pinned Chrome declarative cancel returned malformed envelope: %+v err=%v", declarativeEnvelope, declarativeCancel.err)
	}
	assertCLIChromeDeclarativeCancelOutput(t, declarativeCancel, r.fixtureURL, declarative)
	return declarative
}

func (r *cliChromeIntegrationRun) confirmDeclarativeCancel(t *testing.T, ctx context.Context, declarative *cliChromeDeclarativeCancel, declarativeCancel cliChromeIntegrationResult, declarativeEnvelope webmcp.ToolResultEnvelope) {
	t.Helper()
	var data cliChromeCancelData
	decodeCLIChromeIntegrationData(t, declarativeEnvelope.Data, &data)
	if declarativeCancel.err != nil || data.InvocationID != declarative.receiptID || data.Status != invocationStatusCanceled || data.Phase != "terminal" || data.Outcome != "confirmed_canceled" {
		t.Fatalf("pinned Chrome declarative cancel success = %+v err=%v", data, declarativeCancel.err)
	}
	declarative.outcome = data.Outcome
	slowInvoke := declarative.slowInvoke
	if err := slowInvoke.Wait(); err == nil {
		t.Fatalf("slow declarative invoke completed after confirmed cancellation: stdout=%s", slowInvoke.stdout.String())
	}
	declarative.finished = true
	slowEnvelope := decodeCLIChromeIntegrationEnvelope(t, slowInvoke.stdout.String())
	if slowEnvelope.OK || slowEnvelope.Error == nil || slowEnvelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || slowEnvelope.Error.Details["invocation_id"] != declarative.receiptID {
		t.Fatalf("pinned Chrome slow declarative canceled invoke = %+v", slowEnvelope)
	}
	oracle, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return hasFixtureInvocation(oracle, "canceled:"+slowToolName) || hasFixtureInvocation(oracle, "completed:"+slowToolName+":"+cliChromeSlowMessage)
	})
	if err != nil {
		t.Fatalf("wait for pinned Chrome slow declarative terminal oracle: %v", err)
	}
	declarative.oracle = oracle
	declarative.terminalObserved = true
}

func (r *cliChromeIntegrationRun) classifyDeclarativeCancel(t *testing.T, ctx context.Context, declarative *cliChromeDeclarativeCancel, declarativeCancel cliChromeIntegrationResult, declarativeEnvelope webmcp.ToolResultEnvelope) {
	t.Helper()
	details := declarativeEnvelope.Error.Details
	if declarativeCancel.err == nil || declarativeEnvelope.Error.Code != string(webmcp.ErrorInvocationFailed) || declarativeEnvelope.Error.Retryable || details["invocation_id"] != declarative.receiptID || details["cancel_phase"] != "cancel_dispatched" || details["side_effect_unknown"] != true {
		t.Fatalf("pinned Chrome declarative cancel classification = %+v err=%v", declarativeEnvelope.Error, declarativeCancel.err)
	}
	var ok bool
	declarative.outcome, ok = details["outcome"].(string)
	if !ok || (declarative.outcome != "completed_anyway" && declarative.outcome != cliChromeOutcomeUnconfirmed) {
		t.Fatalf("pinned Chrome declarative cancel outcome = %#v", details["outcome"])
	}
	if observed, ok := details["terminal_observed"].(bool); ok {
		declarative.terminalObserved = observed
	}
	var err error
	switch declarative.outcome {
	case "completed_anyway":
		declarative.oracle = r.assertDeclarativeCompletedAnyway(t, ctx, declarative)
	case cliChromeOutcomeUnconfirmed:
		declarative.oracle, err = readFixtureOracle(ctx, r.fixture.StateURL())
		if err != nil {
			t.Fatalf("read cancellation-unconfirmed declarative oracle: %v", err)
		}
		declarative.slowInvoke.stop()
		declarative.finished = true
	}
}

func (r *cliChromeIntegrationRun) assertDeclarativeCompletedAnyway(t *testing.T, ctx context.Context, declarative *cliChromeDeclarativeCancel) fixtureOracle {
	t.Helper()
	slowInvoke := declarative.slowInvoke
	if err := slowInvoke.Wait(); err != nil {
		t.Fatalf("slow declarative invoke after completed-anyway classification: %v; stdout=%s stderr=%s", err, slowInvoke.stdout.String(), slowInvoke.stderr.String())
	}
	declarative.finished = true
	slowEnvelope := decodeCLIChromeIntegrationEnvelope(t, slowInvoke.stdout.String())
	if !slowEnvelope.OK {
		t.Fatalf("slow declarative invoke did not report its observed completion: %+v", slowEnvelope.Error)
	}
	var slowData struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	decodeCLIChromeIntegrationData(t, slowEnvelope.Data, &slowData)
	if slowData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("slow declarative invocation status = %q, want completed", slowData.Status)
	}
	oracle, err := waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Value == "completed:"+cliChromeSlowMessage && !oracle.Pending && hasFixtureInvocation(oracle, "completed:"+slowToolName+":"+cliChromeSlowMessage)
	})
	if err != nil {
		t.Fatalf("wait for completed-anyway declarative oracle: %v", err)
	}
	return oracle
}

func assertCLIChromeDeclarativeCancelOutput(t *testing.T, declarativeCancel cliChromeIntegrationResult, fixtureURL string, declarative *cliChromeDeclarativeCancel) {
	t.Helper()
	if declarativeCancel.err == nil && declarativeCancel.stderr != "" {
		t.Fatalf("pinned Chrome declarative cancel wrote unexpected stderr = %q", declarativeCancel.stderr)
	}
	for _, forbidden := range []string{fixtureURL, cliChromeSlowMessage, "endpoint", "credential", "secret"} {
		if strings.Contains(declarativeCancel.stderr, forbidden) {
			t.Fatalf("pinned Chrome declarative cancel stderr leaked %q: %q", forbidden, declarativeCancel.stderr)
		}
	}
	if declarative.outcome == "" {
		t.Fatal("pinned Chrome declarative cancellation produced no outcome")
	}
	if !declarative.terminalObserved && declarative.outcome != cliChromeOutcomeUnconfirmed {
		t.Fatalf("pinned Chrome declarative outcome was not correlated: outcome=%q terminal_observed=%t", declarative.outcome, declarative.terminalObserved)
	}
}

func assertCLIChromeRecovery(t *testing.T, binary, configDir, toolRef string) string {
	t.Helper()
	recovered := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRef, "--input-json", `{"message":"recovered"}`, "--timeout", "30s", "--json")
	recoveredEnvelope := requireCLIChromeIntegrationSuccess(t, recovered)
	var recoveredData struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	decodeCLIChromeIntegrationData(t, recoveredEnvelope.Data, &recoveredData)
	if recoveredData.Status != string(webmcp.InvocationCompleted) {
		t.Fatalf("pinned Chrome recovery status = %q", recoveredData.Status)
	}
	var recoveredOutput map[string]any
	if err := json.Unmarshal(recoveredData.Output, &recoveredOutput); err != nil {
		t.Fatalf("decode pinned Chrome recovery output: %v", err)
	}
	if recoveredOutput["message"] != "recovered" || recoveredOutput["greeting"] != fixtureGreeting {
		t.Fatalf("pinned Chrome recovery output = %+v", recoveredOutput)
	}
	return recoveredData.Status
}

// TestWebMCPDirectCLISelectBrowserDeathWithPinnedChrome is the live replay of
// acceptance-gate probe 07. It holds the target-resolution HTTP request to
// prove select is in flight before killing the externally connected browser.
// The test never gives the CLI a browser-owned launch path; its only browser
// cleanup is the explicit kill below.
func TestWebMCPDirectCLISelectBrowserDeathWithPinnedChrome(t *testing.T) {
	if os.Getenv(cliSelectDeathIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the live kill-during-select proof", cliSelectDeathIntegrationEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run := launchCLIChromeIntegration(t, ctx, true, "live select Chrome cleanup")
	proxy := newLiveCDPProxy(run.baseURL)
	t.Cleanup(proxy.Close)
	binary := buildCLIChromeIntegrationBinary(t, ctx, run.workDir)
	configDir := writeCLIChromeIntegrationConfig(t, run.workDir, proxy.URL())
	browserID, targetID := selectCLIChromeIntegrationTarget(t, binary, configDir, "live select")
	selectionPath := filepath.Join(configDir, "webmcp-selection.json")
	priorBytes, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("read prior persisted selection: %v", err)
	}
	proxy.DelayNextList()

	started := time.Now()
	held := startCLIChromeHeldSelect(t, binary, configDir, browserID, targetID, proxy)
	run.killDuringHeldSelect(t, ctx, proxy)
	held.awaitExit(t)
	elapsed := time.Since(started)
	envelope, phase := held.assertBrowserDisconnected(t, browserID)

	afterBytes, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("read selection after failed select: %v", err)
	}
	if string(afterBytes) != string(priorBytes) {
		t.Fatalf("failed select changed persisted selection: before=%q after=%q", string(priorBytes), string(afterBytes))
	}
	followUp, followUpEnvelope := assertCLIChromeFollowUpDisconnected(t, binary, configDir, browserID, targetID)

	t.Logf("WEBMCP_DIRECT_CLI_SELECT_DEATH_PASS chrome=%s revision=%s browser=%s target=%s synchronization=select_target_resolution_list_admitted kill=isolated_chrome_process command_bound=5s elapsed=%s exit_status=nonzero error_code=%s phase=%s reconnect_required=true externally_owned=true relaunch=false selection_preserved=true follow_up_code=%s output=%s stderr=%q follow_up_output=%s follow_up_stderr=%q", lockedChromeVersion, lockedChromeRevision, browserID, targetID, elapsed, envelope.Error.Code, phase, followUpEnvelope.Error.Code, held.process.stdout.String(), held.process.stderr.String(), followUp.stdout, followUp.stderr)
}

// cliChromeHeldSelect is a select process whose target-resolution request is
// held by the live CDP proxy.
type cliChromeHeldSelect struct {
	process *cliChromeIntegrationProcess
	done    chan struct{}
	err     error
}

func startCLIChromeHeldSelect(t *testing.T, binary, configDir, browserID, targetID string, proxy *liveCDPProxy) *cliChromeHeldSelect {
	t.Helper()
	held := &cliChromeHeldSelect{done: make(chan struct{})}
	held.process = startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--command-timeout", "5s", "--json")
	go func() {
		held.err = held.process.Wait()
		close(held.done)
	}()
	t.Cleanup(func() {
		select {
		case <-held.done:
		default:
			held.kill()
		}
	})
	select {
	case <-held.done:
		t.Fatalf("select exited before target-resolution hold was observed: err=%v stdout=%q stderr=%q", held.err, held.process.stdout.String(), held.process.stderr.String())
	case <-proxy.ListAdmitted():
		t.Log("synchronization=select_target_resolution_list_admitted")
	case <-time.After(10 * time.Second):
		t.Fatalf("select did not reach the observable target-resolution hold")
	}
	return held
}

// kill stops the held select process and waits for its reaper goroutine.
func (h *cliChromeHeldSelect) kill() {
	if h.process.command.Process != nil {
		discardSecondaryError(h.process.command.Process.Kill)
	}
	<-h.done
}

func (r *cliChromeIntegrationRun) killDuringHeldSelect(t *testing.T, ctx context.Context, proxy *liveCDPProxy) {
	t.Helper()
	select {
	case <-r.browser.done:
		t.Fatalf("Chrome exited before the explicit Chrome kill")
	default:
	}
	if _, err := readDevToolsTargets(ctx, r.baseURL); err != nil {
		t.Fatalf("observe live browser before explicit kill: %v", err)
	}

	if err := r.browser.Kill(); err != nil {
		t.Fatalf("kill only the live Chrome process: %v", err)
	}
	r.killed = true
	proxy.MarkBrowserDead()
	proxy.ReleaseList()
}

func (h *cliChromeHeldSelect) awaitExit(t *testing.T) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(12 * time.Second):
		h.kill()
		t.Fatalf("select did not exit within command bound plus cleanup allowance: err=%v stdout=%q stderr=%q", h.err, h.process.stdout.String(), h.process.stderr.String())
	}
}

// assertBrowserDisconnected checks the failed select envelope and returns it
// with the classified failure phase.
func (h *cliChromeHeldSelect) assertBrowserDisconnected(t *testing.T, browserID string) (webmcp.ToolResultEnvelope, string) {
	t.Helper()
	if h.err == nil {
		t.Fatalf("select unexpectedly succeeded after Chrome death: stdout=%q", h.process.stdout.String())
	}
	envelope := decodeCLIChromeIntegrationEnvelope(t, h.process.stdout.String())
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("live kill-during-select envelope = %+v; stderr=%q", envelope, h.process.stderr.String())
	}
	if envelope.Error.Details["browser_id"] != browserID || envelope.Error.Details["target_id"] != "" || envelope.Error.Details["phase"] != "list_targets" || envelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("live kill-during-select details = %#v", envelope.Error.Details)
	}
	// The details check above pinned phase to the string "list_targets".
	phase, ok := envelope.Error.Details["phase"].(string)
	if !ok {
		t.Fatalf("live kill-during-select phase = %#v, want string", envelope.Error.Details["phase"])
	}
	return envelope, phase
}

func assertCLIChromeFollowUpDisconnected(t *testing.T, binary, configDir, browserID, targetID string) (cliChromeIntegrationResult, webmcp.ToolResultEnvelope) {
	t.Helper()
	followUp := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "context", "--command-timeout", "5s", "--json")
	if followUp.err == nil {
		t.Fatalf("follow-up context unexpectedly succeeded after Chrome death: stdout=%q", followUp.stdout)
	}
	followUpEnvelope := decodeCLIChromeIntegrationEnvelope(t, followUp.stdout)
	if followUpEnvelope.OK || followUpEnvelope.Error == nil || followUpEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("follow-up context envelope code=%v details=%#v; stderr=%q stdout=%q", followUpEnvelope.Error.Code, followUpEnvelope.Error.Details, followUp.stderr, followUp.stdout)
	}
	if followUpEnvelope.Error.Details["browser_id"] != browserID || followUpEnvelope.Error.Details["target_id"] != targetID || followUpEnvelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("follow-up context details = %#v", followUpEnvelope.Error.Details)
	}
	return followUp, followUpEnvelope
}

type cliChromeIntegrationReceipt struct {
	Version      string `json:"version"`
	InvocationID string `json:"invocation_id"`
	ToolRef      string `json:"tool_ref"`
	State        string `json:"state"`
}

type cliChromeIntegrationProcess struct {
	command *exec.Cmd
	stdout  *cliChromeIntegrationBuffer
	stderr  *cliChromeIntegrationStderr
}

type cliChromeIntegrationResult struct {
	stdout string
	stderr string
	err    error
}

func startCLIChromeIntegrationProcess(t *testing.T, binary, configDir string, args ...string) *cliChromeIntegrationProcess {
	t.Helper()
	commandArgs := append([]string{"--config-dir", configDir}, args...)
	command := exec.Command(binary, commandArgs...)
	stdout := &cliChromeIntegrationBuffer{}
	stderr := newCLIChromeIntegrationStderr()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start CLI process %v: %v", args, err)
	}
	return &cliChromeIntegrationProcess{command: command, stdout: stdout, stderr: stderr}
}

func runCLIChromeIntegrationCommand(t *testing.T, binary, configDir string, args ...string) cliChromeIntegrationResult {
	t.Helper()
	process := startCLIChromeIntegrationProcess(t, binary, configDir, args...)
	err := process.Wait()
	return cliChromeIntegrationResult{stdout: process.stdout.String(), stderr: process.stderr.String(), err: err}
}

func (p *cliChromeIntegrationProcess) Wait() error {
	return p.command.Wait()
}

func (p *cliChromeIntegrationProcess) stop() {
	if p == nil || p.command == nil {
		return
	}
	if p.command.Process != nil {
		discardSecondaryError(p.command.Process.Kill)
	}
	discardSecondaryError(p.Wait)
}

func receiptLineForCLIChrome(receipt cliChromeIntegrationReceipt) string {
	encoded := mustFixtureJSON(receipt)
	return string(append(encoded, '\n'))
}

func decodeCLIChromeIntegrationEnvelope(t *testing.T, output string) webmcp.ToolResultEnvelope {
	t.Helper()
	envelope, err := webmcp.UnmarshalToolResult([]byte(output))
	if err != nil {
		t.Fatalf("decode CLI integration envelope: %v; output=%q", err, output)
	}
	return envelope
}

func requireCLIChromeIntegrationSuccess(t *testing.T, result cliChromeIntegrationResult) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.err != nil {
		t.Fatalf("CLI integration command: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	envelope := decodeCLIChromeIntegrationEnvelope(t, result.stdout)
	if !envelope.OK {
		t.Fatalf("CLI integration command failed: %+v", envelope.Error)
	}
	return envelope
}

func decodeCLIChromeIntegrationData(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode CLI integration data: %v; data=%s", err, raw)
	}
}
