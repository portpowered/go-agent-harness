package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	workDir := t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if version.WebSocketDebuggerURL != browser.endpoint() {
		t.Fatalf("DevTools websocket = %q, launch announcement = %q", version.WebSocketDebuggerURL, browser.endpoint())
	}
	binary := filepath.Join(workDir, "agent")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./agent-cli/cmd/agent")
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	build.Dir = root
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build agent CLI: %v\n%s", buildErr, output)
	}

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
`, strings.TrimRight(baseURL, "/")+"/json/version")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write CLI integration config: %v", err)
	}

	browsers := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "browsers", "--json")
	browsersEnvelope := requireCLIChromeIntegrationSuccess(t, browsers)
	var browsersData struct {
		Browsers []struct {
			ID string `json:"id"`
		} `json:"browsers"`
	}
	decodeCLIChromeIntegrationData(t, browsersEnvelope.Data, &browsersData)
	if len(browsersData.Browsers) != 1 || browsersData.Browsers[0].ID == "" {
		t.Fatalf("pinned Chrome browser discovery = %+v", browsersData)
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
		t.Fatalf("pinned Chrome target discovery = %+v", tabsData)
	}
	targetID := tabsData.Tabs[0].TargetID

	selected := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--json")
	_ = requireCLIChromeIntegrationSuccess(t, selected)

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

	invoke := startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRefs[cancelToolName], "--input-json", `{"message":"live-hold"}`, "--timeout", "30s", "--json")
	var receipt cliChromeIntegrationReceipt
	select {
	case line := <-invoke.stderr.firstLine:
		if err := json.Unmarshal([]byte(line), &receipt); err != nil {
			t.Fatalf("decode pinned Chrome dispatch receipt: %v; stderr=%q", err, line)
		}
	case <-ctx.Done():
		t.Fatalf("wait for pinned Chrome dispatch receipt: %v", ctx.Err())
	case <-time.After(15 * time.Second):
		t.Fatal("pinned Chrome invoke did not emit a dispatch receipt")
	}
	if receipt.InvocationID == "" || receipt.ToolRef != toolRefs[cancelToolName] || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("pinned Chrome dispatch receipt = %+v", receipt)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Pending && oracle.Value == "pending:live-hold"
	}); err != nil {
		t.Fatalf("wait for pinned Chrome pending page state: %v", err)
	}

	cancelProcess := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "cancel", "--invocation", receipt.InvocationID, "--json")
	if cancelProcess.err != nil {
		t.Fatalf("pinned Chrome cancel process: %v\nstdout=%s\nstderr=%s", cancelProcess.err, cancelProcess.stdout, cancelProcess.stderr)
	}
	var cancelData struct {
		InvocationID string `json:"invocation_id"`
		Status       string `json:"status"`
		Phase        string `json:"phase"`
		Outcome      string `json:"outcome"`
	}
	cancelEnvelope := requireCLIChromeIntegrationSuccess(t, cancelProcess)
	decodeCLIChromeIntegrationData(t, cancelEnvelope.Data, &cancelData)
	if cancelData.InvocationID != receipt.InvocationID || cancelData.Status != "canceled" || cancelData.Phase != "terminal" || cancelData.Outcome != "confirmed_canceled" {
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
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		for _, invocation := range oracle.Invocations {
			if invocation == "canceled:"+cancelToolName {
				return true
			}
		}
		return false
	}); err != nil {
		t.Fatalf("wait for pinned Chrome cancellation event: %v", err)
	}

	// The slow form is declarative and has toolautosubmit. Its timer makes the
	// browser's behavior observable: Chrome may cancel it, report completion
	// anyway, or fail to provide a correlated terminal event. The test accepts
	// each documented classification, but never treats dispatch alone as
	// success.
	const slowMessage = "declarative-hold"
	slowInvoke := startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRefs[slowToolName], "--input-json", `{"message":"`+slowMessage+`"}`, "--timeout", "10s", "--json")
	slowInvokeFinished := false
	defer func() {
		if !slowInvokeFinished {
			slowInvoke.stop()
		}
	}()
	var slowReceipt cliChromeIntegrationReceipt
	select {
	case line := <-slowInvoke.stderr.firstLine:
		if err := json.Unmarshal([]byte(line), &slowReceipt); err != nil {
			t.Fatalf("decode pinned Chrome slow declarative dispatch receipt: %v; stderr=%q", err, line)
		}
	case <-ctx.Done():
		t.Fatalf("wait for pinned Chrome slow declarative dispatch receipt: %v", ctx.Err())
	case <-time.After(10 * time.Second):
		t.Fatal("pinned Chrome slow declarative invoke did not emit a dispatch receipt")
	}
	if slowReceipt.InvocationID == "" || slowReceipt.ToolRef != toolRefs[slowToolName] || slowReceipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("pinned Chrome slow declarative dispatch receipt = %+v", slowReceipt)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Pending && oracle.Value == "pending:"+slowMessage && hasFixtureInvocation(oracle, slowToolName+":"+slowMessage)
	}); err != nil {
		t.Fatalf("wait for pinned Chrome slow declarative pending page state: %v", err)
	}

	declarativeCancel := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "cancel", "--invocation", slowReceipt.InvocationID, "--timeout", "8s", "--json")
	declarativeEnvelope := decodeCLIChromeIntegrationEnvelope(t, declarativeCancel.stdout)
	declarativeOutcome := ""
	declarativeTerminalObserved := false
	var declarativeOracle fixtureOracle
	switch {
	case declarativeEnvelope.OK:
		var data struct {
			InvocationID string `json:"invocation_id"`
			Status       string `json:"status"`
			Phase        string `json:"phase"`
			Outcome      string `json:"outcome"`
		}
		decodeCLIChromeIntegrationData(t, declarativeEnvelope.Data, &data)
		if declarativeCancel.err != nil || data.InvocationID != slowReceipt.InvocationID || data.Status != "canceled" || data.Phase != "terminal" || data.Outcome != "confirmed_canceled" {
			t.Fatalf("pinned Chrome declarative cancel success = %+v err=%v", data, declarativeCancel.err)
		}
		declarativeOutcome = data.Outcome
		if err := slowInvoke.Wait(); err == nil {
			t.Fatalf("slow declarative invoke completed after confirmed cancellation: stdout=%s", slowInvoke.stdout.String())
		}
		slowInvokeFinished = true
		slowEnvelope := decodeCLIChromeIntegrationEnvelope(t, slowInvoke.stdout.String())
		if slowEnvelope.OK || slowEnvelope.Error == nil || slowEnvelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || slowEnvelope.Error.Details["invocation_id"] != slowReceipt.InvocationID {
			t.Fatalf("pinned Chrome slow declarative canceled invoke = %+v", slowEnvelope)
		}
		declarativeOracle, err = waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
			return hasFixtureInvocation(oracle, "canceled:"+slowToolName) || hasFixtureInvocation(oracle, "completed:"+slowToolName+":"+slowMessage)
		})
		if err != nil {
			t.Fatalf("wait for pinned Chrome slow declarative terminal oracle: %v", err)
		}
		declarativeTerminalObserved = true
	case !declarativeEnvelope.OK && declarativeEnvelope.Error != nil:
		if declarativeCancel.err == nil || declarativeEnvelope.Error.Code != string(webmcp.ErrorInvocationFailed) || declarativeEnvelope.Error.Retryable || declarativeEnvelope.Error.Details["invocation_id"] != slowReceipt.InvocationID || declarativeEnvelope.Error.Details["cancel_phase"] != "cancel_dispatched" || declarativeEnvelope.Error.Details["side_effect_unknown"] != true {
			t.Fatalf("pinned Chrome declarative cancel classification = %+v err=%v", declarativeEnvelope.Error, declarativeCancel.err)
		}
		var ok bool
		declarativeOutcome, ok = declarativeEnvelope.Error.Details["outcome"].(string)
		if !ok || (declarativeOutcome != "completed_anyway" && declarativeOutcome != "cancellation_unconfirmed") {
			t.Fatalf("pinned Chrome declarative cancel outcome = %#v", declarativeEnvelope.Error.Details["outcome"])
		}
		if observed, ok := declarativeEnvelope.Error.Details["terminal_observed"].(bool); ok {
			declarativeTerminalObserved = observed
		}
		switch declarativeOutcome {
		case "completed_anyway":
			if err := slowInvoke.Wait(); err != nil {
				t.Fatalf("slow declarative invoke after completed-anyway classification: %v; stdout=%s stderr=%s", err, slowInvoke.stdout.String(), slowInvoke.stderr.String())
			}
			slowInvokeFinished = true
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
			declarativeOracle, err = waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
				return oracle.Value == "completed:"+slowMessage && !oracle.Pending && hasFixtureInvocation(oracle, "completed:"+slowToolName+":"+slowMessage)
			})
			if err != nil {
				t.Fatalf("wait for completed-anyway declarative oracle: %v", err)
			}
		case "cancellation_unconfirmed":
			declarativeOracle, err = readFixtureOracle(ctx, fixture.StateURL())
			if err != nil {
				t.Fatalf("read cancellation-unconfirmed declarative oracle: %v", err)
			}
			slowInvoke.stop()
			slowInvokeFinished = true
		}
	default:
		t.Fatalf("pinned Chrome declarative cancel returned malformed envelope: %+v err=%v", declarativeEnvelope, declarativeCancel.err)
	}
	if declarativeCancel.err == nil && declarativeCancel.stderr != "" {
		t.Fatalf("pinned Chrome declarative cancel wrote unexpected stderr = %q", declarativeCancel.stderr)
	}
	for _, forbidden := range []string{fixtureURL, slowMessage, "endpoint", "credential", "secret"} {
		if strings.Contains(declarativeCancel.stderr, forbidden) {
			t.Fatalf("pinned Chrome declarative cancel stderr leaked %q: %q", forbidden, declarativeCancel.stderr)
		}
	}
	if declarativeOutcome == "" {
		t.Fatal("pinned Chrome declarative cancellation produced no outcome")
	}
	if !declarativeTerminalObserved && declarativeOutcome != "cancellation_unconfirmed" {
		t.Fatalf("pinned Chrome declarative outcome was not correlated: outcome=%q terminal_observed=%t", declarativeOutcome, declarativeTerminalObserved)
	}

	recovered := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "invoke", "--tool-ref", toolRefs[completeToolName], "--input-json", `{"message":"recovered"}`, "--timeout", "30s", "--json")
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
	if recoveredOutput["message"] != "recovered" || recoveredOutput["greeting"] != "hello" {
		t.Fatalf("pinned Chrome recovery output = %+v", recoveredOutput)
	}

	t.Logf("WEBMCP_DIRECT_CLI_INTEGRATION_PASS chrome=%s revision=%s browser=%s target=%s controlled_receipt=%s controlled_cancel=%s controlled_oracle=canceled declarative_receipt=%s declarative_outcome=%s declarative_terminal_observed=%t declarative_oracle_pending=%t recovery=%s", lockedChromeVersion, lockedChromeRevision, browserID, targetID, receipt.InvocationID, cancelData.Outcome, slowReceipt.InvocationID, declarativeOutcome, declarativeTerminalObserved, declarativeOracle.Pending, recoveredData.Status)
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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	workDir := t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, ctx, fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	browserKilled := false
	t.Cleanup(func() {
		if !browserKilled {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("live select Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if version.WebSocketDebuggerURL != browser.endpoint() {
		t.Fatalf("DevTools websocket = %q, launch announcement = %q", version.WebSocketDebuggerURL, browser.endpoint())
	}
	proxy := newLiveCDPProxy(baseURL)
	t.Cleanup(proxy.Close)

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
`, strings.TrimRight(proxy.URL(), "/")+"/json/version")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write CLI integration config: %v", err)
	}

	browsers := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "browsers", "--json")
	browsersEnvelope := requireCLIChromeIntegrationSuccess(t, browsers)
	var browsersData struct {
		Browsers []struct {
			ID string `json:"id"`
		} `json:"browsers"`
	}
	decodeCLIChromeIntegrationData(t, browsersEnvelope.Data, &browsersData)
	if len(browsersData.Browsers) != 1 || browsersData.Browsers[0].ID == "" {
		t.Fatalf("live select browser discovery = %+v", browsersData)
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
		t.Fatalf("live select target discovery = %+v", tabsData)
	}
	targetID := tabsData.Tabs[0].TargetID

	selected := runCLIChromeIntegrationCommand(t, binary, configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--json")
	_ = requireCLIChromeIntegrationSuccess(t, selected)
	selectionPath := filepath.Join(configDir, "webmcp-selection.json")
	priorBytes, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("read prior persisted selection: %v", err)
	}
	proxy.DelayNextList()

	started := time.Now()
	selectProcess := startCLIChromeIntegrationProcess(t, binary, configDir, "webmcp", "select", "--browser", browserID, "--tab", targetID, "--command-timeout", "5s", "--json")
	selectDone := make(chan struct{})
	var selectErr error
	go func() {
		selectErr = selectProcess.Wait()
		close(selectDone)
	}()
	t.Cleanup(func() {
		select {
		case <-selectDone:
		default:
			if selectProcess.command.Process != nil {
				_ = selectProcess.command.Process.Kill()
			}
			<-selectDone
		}
	})
	select {
	case <-selectDone:
		t.Fatalf("select exited before target-resolution hold was observed: err=%v stdout=%q stderr=%q", selectErr, selectProcess.stdout.String(), selectProcess.stderr.String())
	case <-proxy.ListAdmitted():
		t.Log("synchronization=select_target_resolution_list_admitted")
	case <-time.After(10 * time.Second):
		t.Fatalf("select did not reach the observable target-resolution hold")
	}
	select {
	case <-browser.done:
		t.Fatalf("Chrome exited before the explicit Chrome kill")
	default:
	}
	if _, err := readDevToolsTargets(ctx, baseURL); err != nil {
		t.Fatalf("observe live browser before explicit kill: %v", err)
	}

	if err := browser.Kill(); err != nil {
		t.Fatalf("kill only the live Chrome process: %v", err)
	}
	browserKilled = true
	proxy.MarkBrowserDead()
	proxy.ReleaseList()

	select {
	case <-selectDone:
	case <-time.After(12 * time.Second):
		if selectProcess.command.Process != nil {
			_ = selectProcess.command.Process.Kill()
		}
		<-selectDone
		t.Fatalf("select did not exit within command bound plus cleanup allowance: err=%v stdout=%q stderr=%q", selectErr, selectProcess.stdout.String(), selectProcess.stderr.String())
	}
	elapsed := time.Since(started)
	if selectErr == nil {
		t.Fatalf("select unexpectedly succeeded after Chrome death: stdout=%q", selectProcess.stdout.String())
	}
	envelope := decodeCLIChromeIntegrationEnvelope(t, selectProcess.stdout.String())
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("live kill-during-select envelope = %+v; stderr=%q", envelope, selectProcess.stderr.String())
	}
	if envelope.Error.Details["browser_id"] != browserID || envelope.Error.Details["target_id"] != "" || envelope.Error.Details["phase"] != "list_targets" || envelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("live kill-during-select details = %#v", envelope.Error.Details)
	}
	phase, _ := envelope.Error.Details["phase"].(string)

	afterBytes, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatalf("read selection after failed select: %v", err)
	}
	if string(afterBytes) != string(priorBytes) {
		t.Fatalf("failed select changed persisted selection: before=%q after=%q", string(priorBytes), string(afterBytes))
	}

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

	t.Logf("WEBMCP_DIRECT_CLI_SELECT_DEATH_PASS chrome=%s revision=%s browser=%s target=%s synchronization=select_target_resolution_list_admitted kill=isolated_chrome_process command_bound=5s elapsed=%s exit_status=nonzero error_code=%s phase=%s reconnect_required=true externally_owned=true relaunch=false selection_preserved=true follow_up_code=%s output=%s stderr=%q follow_up_output=%s follow_up_stderr=%q", lockedChromeVersion, lockedChromeRevision, browserID, targetID, elapsed, envelope.Error.Code, phase, followUpEnvelope.Error.Code, selectProcess.stdout.String(), selectProcess.stderr.String(), followUp.stdout, followUp.stderr)
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
		_ = p.command.Process.Kill()
	}
	_ = p.Wait()
}

type cliChromeIntegrationBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *cliChromeIntegrationBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}

func (b *cliChromeIntegrationBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type cliChromeIntegrationStderr struct {
	cliChromeIntegrationBuffer
	firstLine chan string
	notified  bool
}

func newCLIChromeIntegrationStderr() *cliChromeIntegrationStderr {
	return &cliChromeIntegrationStderr{firstLine: make(chan string, 1)}
}

func (b *cliChromeIntegrationStderr) Write(value []byte) (int, error) {
	b.mu.Lock()
	written, err := b.data.Write(value)
	if !b.notified {
		if newline := bytes.IndexByte(b.data.Bytes(), '\n'); newline >= 0 {
			b.notified = true
			line := append([]byte(nil), b.data.Bytes()[:newline+1]...)
			b.mu.Unlock()
			b.firstLine <- string(line)
			return written, err
		}
	}
	b.mu.Unlock()
	return written, err
}

func receiptLineForCLIChrome(receipt cliChromeIntegrationReceipt) string {
	encoded, _ := json.Marshal(receipt)
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
