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
	browser, err := launchPinnedChrome(ctx, pinned, fixture.URL())
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
	if toolRefs[cancelToolName] == "" || toolRefs[completeToolName] == "" {
		t.Fatalf("pinned Chrome tools omitted cancellation/recovery tools: %+v", toolsData)
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
	}
	cancelEnvelope := requireCLIChromeIntegrationSuccess(t, cancelProcess)
	decodeCLIChromeIntegrationData(t, cancelEnvelope.Data, &cancelData)
	if cancelData.InvocationID != receipt.InvocationID || cancelData.Status != "cancel_requested" {
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

	t.Logf("WEBMCP_DIRECT_CLI_INTEGRATION_PASS chrome=%s revision=%s browser=%s target=%s receipt=%s cancel=%s recovery=%s", lockedChromeVersion, lockedChromeRevision, browserID, targetID, receipt.InvocationID, cancelData.Status, recoveredData.Status)
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
