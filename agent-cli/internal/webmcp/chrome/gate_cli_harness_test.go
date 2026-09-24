package chrome

// Actual-binary CLI harness for the Gate I1 family of live proofs: child
// process lifecycle, sanitized environment, and typed JSON envelopes.

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
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

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

// gateStatusReady is the doctor status for a fully ready browser surface.
const gateStatusReady = "ready"

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
	expectedAddress := strings.TrimRight(browserHTTPURL(version.WebSocketDebuggerURL), "/") + devToolsVersionPath
	if report.Endpoint.Scope != loopbackAddressClass || report.Endpoint.Address != expectedAddress {
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

func runProbe03Command(t *testing.T, parent context.Context, binaryPath, configDir, homeDir string, args ...string) gateCLIResult {
	t.Helper()
	commandContext, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	process, err := startProbe03Command(commandContext, binaryPath, configDir, homeDir, args...)
	if err != nil {
		return gateCLIResult{Args: append([]string(nil), args...), ExitCode: -1, Err: err}
	}
	result, waitErr := process.wait(commandContext)
	if waitErr != nil {
		result.Args = append([]string(nil), process.args...)
		result.ExitCode = -1
		result.Err = waitErr
	}
	return result
}

func probe03ChildEnvironment(homeDir string) []string {
	base := gateChildEnvironment()
	if homeDir == "" {
		return base
	}
	environment := make([]string, 0, len(base)+2)
	for _, value := range base {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || key == "USERPROFILE" || key == "XDG_CONFIG_HOME" || strings.HasPrefix(key, "AGENT_") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "HOME="+homeDir, "USERPROFILE="+homeDir)
	return environment
}

func recordProbe03Command(transcript *[]string, result gateCLIResult, cdpURL, fixtureToken, profileDir, configDir, homeDir string) {
	if transcript == nil {
		return
	}
	args := strings.Join(result.Args, " ")
	args = strings.ReplaceAll(args, cdpURL, "<cdp-url?query-redacted>")
	args = strings.ReplaceAll(args, fixtureToken, "<fixture-token>")
	args = strings.ReplaceAll(args, profileDir, "<temporary-profile>")
	if configDir != "" {
		args = strings.ReplaceAll(args, configDir, "<fresh-config-dir>")
	}
	if homeDir != "" {
		args = strings.ReplaceAll(args, homeDir, "<temporary-home>")
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = "<empty>"
	}
	output = strings.ReplaceAll(output, cdpURL, "<cdp-url?query-redacted>")
	output = strings.ReplaceAll(output, fixtureToken, "<fixture-token>")
	output = strings.ReplaceAll(output, profileDir, "<temporary-profile>")
	*transcript = append(*transcript, fmt.Sprintf("$ agent %s exit=%d\n%s", args, result.ExitCode, output))
}

func assertProbe03SafeOutput(t *testing.T, result gateCLIResult, cdpURL, token string) {
	t.Helper()
	output := result.Stdout + "\n" + result.Stderr
	for _, secret := range []string{cdpURL, "probe03-fragment-" + token} {
		if strings.Contains(output, secret) {
			t.Fatalf("Probe 03 command %q exposed %q: stdout=%q stderr=%q", result.Args, secret, result.Stdout, result.Stderr)
		}
	}
	assertGateSafeOutput(t, result)
}

func requireProbe03Failure(t *testing.T, result gateCLIResult, wantCode webmcp.ErrorCode) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("Probe 03 failure command unexpectedly succeeded: args=%q exit=%d err=%v stdout=%q stderr=%q", result.Args, result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := decodeOneJSON([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode Probe 03 failure envelope: %v; output=%q", err, result.Stdout)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(wantCode) {
		t.Fatalf("Probe 03 failure envelope = %+v, want code %q", envelope, wantCode)
	}
	return envelope
}
