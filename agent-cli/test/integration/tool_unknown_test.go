package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
)

// s2s-v4e-tool-unknown proves the unregistered-tool refusal through the public
// agent-cli command entrypoint, run in-process on a virtual clock (clitest). The fixtures are replay-only: no provider credentials
// or network connection is needed by any command under test.
const (
	unknownToolFixture     = "s2s-v4e-tool-unknown.capture.json"
	unknownToolScenario    = "s2s-v4e/s2s-v4e-tool-unknown.scenario.json"
	registeredToolFixture  = "s2s-v4e-tool-registered.capture.json"
	registeredToolScenario = "s2s-v4e/s2s-v4e-tool-registered.scenario.json"
	unknownToolName        = "s2s_v4e_unregistered_tool"
	registeredToolName     = "read_file"
)

// unknownToolRefusalPayload is the exact typed refusal payload that the v4e
// scenarios demand in observable output: a stable machine-readable refusal
// classification carrying the requested tool name. Both the positive and the
// negative-control scenario declare this identical expectation.
const unknownToolRefusalPayload = `{"type":"refusal","classification":"unknown_tool","tool_name":"s2s_v4e_unregistered_tool"}`

const toolUnknownRunDeadline = 60 * time.Second

type agentProcessResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type toolRefusalObservation struct {
	Type           string `json:"type"`
	Classification string `json:"classification"`
	ToolName       string `json:"tool_name"`
}

func agentCLIRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve agent-cli root: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..")
}

// diagnosticDeadline bounds an in-process CLI command. These commands do no
// build or exec, so the deadline only fires on a real hang; when it does, the
// goroutine dump is logged so the stuck wait is visible in CI output.
func diagnosticDeadline(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	dumped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(dumped)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			stack := make([]byte, 1<<20)
			t.Logf("in-process command exceeded %s; goroutines:\n%s", timeout, stack[:runtime.Stack(stack, true)])
		}
	})
	return ctx, func() {
		cancel()
		if !stop() {
			<-dumped
		}
	}
}

func buildAgentBinary(t *testing.T) string {
	t.Helper()
	if agentBinaryPath == "" {
		t.Fatal("package TestMain did not build the agent CLI")
	}
	return agentBinaryPath
}

// runAgentCLI runs one invocation of the production-composed CLI in the
// calling test's bubble.
func runAgentCLI(t *testing.T, args ...string) agentProcessResult {
	t.Helper()
	// Replay is in-memory. Invalid proxy endpoints make an accidental HTTP(S)
	// attempt fail immediately instead of allowing a test to reach the network.
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(name, "http://127.0.0.1:1")
	}
	run := clitest.Run(t, clitest.Invocation{Args: args, Timeout: toolUnknownRunDeadline})
	return agentProcessResult{ExitCode: run.ExitCode, Stdout: run.Stdout, Stderr: run.Stderr}
}

func locateUnknownToolFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(agentCLIRoot(t), "test", "integration", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unknown-tool fixture %q not found: %v", name, err)
	}
	return path
}

type probeExpectationOutcome struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Passed   bool   `json:"passed"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Error    string `json:"error,omitempty"`
}

type probeScenarioResult struct {
	Name           string                    `json:"name"`
	Pass           bool                      `json:"pass"`
	Expectations   []probeExpectationOutcome `json:"expectations"`
	Frames         int                       `json:"frames"`
	TerminalReason string                    `json:"terminal_reason,omitempty"`
	Error          string                    `json:"error,omitempty"`
}

func parseProbeResult(t *testing.T, stdout string) probeScenarioResult {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("probe stdout should contain one scenario result, got %d lines:\n%s", len(lines), stdout)
	}
	var result probeScenarioResult
	if err := json.Unmarshal([]byte(lines[0]), &result); err != nil {
		t.Fatalf("decode probe result %q: %v", lines[0], err)
	}
	return result
}

func parseToolRefusal(t *testing.T, output string) toolRefusalObservation {
	t.Helper()
	start := strings.Index(output, `{"type":"refusal"`)
	if start < 0 {
		t.Fatalf("CLI output does not contain a typed refusal payload:\n%s", output)
	}
	end := strings.Index(output[start:], "\n[session closed:")
	if end < 0 {
		t.Fatalf("typed refusal output is missing the post-refusal session-close boundary:\n%s", output)
	}
	end += start
	var refusal toolRefusalObservation
	if err := json.Unmarshal([]byte(output[start:end]), &refusal); err != nil {
		t.Fatalf("decode typed refusal %q: %v", output[start:end], err)
	}
	return refusal
}

// TestUnknownToolRefusalThroughCLI proves the positive unknown-tool path
// from the command entrypoint. It first observes the active tool registry
// through the public CLI, then runs the real session replay and probe commands
// with their production argv and replay-only transport.
func TestUnknownToolRefusalThroughCLI(t *testing.T) {
	clitest.Test(t, testUnknownToolRefusalThroughCLI)
}

func testUnknownToolRefusalThroughCLI(t *testing.T) {
	fixture := locateUnknownToolFixture(t, unknownToolFixture)
	scenario := locateUnknownToolFixture(t, unknownToolScenario)
	configDir := t.TempDir()

	registry := runAgentCLI(t, "--config-dir", configDir, "tool", "--list")
	if registry.ExitCode != 0 {
		t.Fatalf("tool --list failed: exit=%d stdout=%q stderr=%q", registry.ExitCode, registry.Stdout, registry.Stderr)
	}
	if !strings.Contains("\n"+registry.Stdout, "\n"+registeredToolName+"\n") {
		t.Fatalf("active CLI registry does not contain registered control tool %q: %q", registeredToolName, registry.Stdout)
	}
	if strings.Contains("\n"+registry.Stdout, "\n"+unknownToolName+"\n") {
		t.Fatalf("positive fixture tool %q unexpectedly appears in active CLI registry: %q", unknownToolName, registry.Stdout)
	}

	session := runAgentCLI(t,
		"--config-dir", configDir,
		"session",
		"--replay", fixture,
	)
	if session.ExitCode != 0 {
		t.Fatalf("unknown-tool session replay failed: exit=%d stdout=%q stderr=%q", session.ExitCode, session.Stdout, session.Stderr)
	}
	combined := session.Stdout + session.Stderr
	if strings.Contains(strings.ToLower(combined), "panic") {
		t.Fatalf("unknown-tool replay must not panic:\n%s", combined)
	}
	refusal := parseToolRefusal(t, session.Stdout)
	if refusal.Type != "refusal" || refusal.Classification != "unknown_tool" || refusal.ToolName != unknownToolName {
		t.Fatalf("typed refusal = %+v, want type=refusal classification=unknown_tool tool_name=%q", refusal, unknownToolName)
	}
	if !strings.Contains(session.Stdout, "[session closed: fixture_complete]") ||
		!strings.Contains(session.Stdout, "[session terminal:") {
		t.Fatalf("session output is missing the healthy post-refusal terminal boundary:\n%s", session.Stdout)
	}

	probe := runAgentCLI(t,
		"probe", "run", scenario,
		"--replay", fixture,
		"--json",
	)
	if probe.ExitCode != 0 {
		t.Fatalf("unknown-tool probe run failed: exit=%d stdout=%q stderr=%q", probe.ExitCode, probe.Stdout, probe.Stderr)
	}
	result := parseProbeResult(t, probe.Stdout)
	if !result.Pass {
		t.Fatalf("unknown-tool probe result did not pass: %+v", result)
	}
	if result.Frames != 7 || result.TerminalReason != "synthetic" {
		t.Fatalf("probe result did not record the complete terminal boundary: %+v", result)
	}
	for _, outcome := range result.Expectations {
		if !outcome.Passed {
			t.Fatalf("unknown-tool probe expectation %s failed: expected=%s actual=%s", outcome.Kind, outcome.Expected, outcome.Actual)
		}
	}
	if !strings.Contains(probe.Stderr, `"status":"pass"`) {
		t.Fatalf("probe summary did not report pass: %q", probe.Stderr)
	}
}

// TestRegisteredToolControlRejectsUnknownRefusalExpectation proves the v4e
// assertion discriminates instead of labeling every tool call as unknown. The
// committed negative control preserves the positive interaction shape but
// requests read_file — a name present in the active session registry — so the
// same unknown-tool refusal expectation must fail through the real probe run
// with machine-readable expected-versus-actual evidence.
func TestRegisteredToolControlRejectsUnknownRefusalExpectation(t *testing.T) {
	clitest.Test(t, testRegisteredToolControlRejectsUnknownRefusalExpectation)
}

func testRegisteredToolControlRejectsUnknownRefusalExpectation(t *testing.T) {
	scenario := locateUnknownToolFixture(t, registeredToolScenario)
	fixture := locateUnknownToolFixture(t, registeredToolFixture)

	assertSameExpectationsAsPositiveScenario(t)

	control := runAgentCLI(t,
		"probe", "run", scenario,
		"--replay", fixture,
		"--json",
	)
	if control.ExitCode == 0 {
		t.Fatalf("registered-tool control must fail the unknown-refusal expectation: exit=0 stdout=%q stderr=%q", control.Stdout, control.Stderr)
	}
	if !strings.Contains(control.Stderr, `"status":"fail"`) || !strings.Contains(control.Stderr, `"failed":1`) {
		t.Fatalf("control summary did not report exactly one failed scenario:\n%s", control.Stderr)
	}

	result := parseProbeResult(t, control.Stdout)
	if result.Pass {
		t.Fatalf("registered-tool control scenario reported pass: %+v", result)
	}
	// Error is set only for execution failures (replay divergence, missing
	// fixtures, deadguard timeout, recovered panic). Its absence plus the full
	// recorded frame count and terminal reason prove the run completed the
	// healthy recorded boundary and lost on expectations alone.
	if result.Error != "" {
		t.Fatalf("control failure must be an expectation mismatch, not an execution error: %+v", result)
	}
	if result.Frames != 7 || result.TerminalReason != "synthetic" {
		t.Fatalf("control run did not complete the recorded terminal boundary: %+v", result)
	}

	refusalOutcome, passedKinds := failingTranscriptOutcome(t, result)
	for _, kind := range []string{"frame_count", "terminal_reason"} {
		if !passedKinds[kind] {
			t.Fatalf("control %s expectation must pass so the failure isolates the refusal condition: %+v", kind, result.Expectations)
		}
	}
	if refusalOutcome.Expected != strconv.Quote(unknownToolRefusalPayload) {
		t.Fatalf("control expected evidence = %q, want quoted typed refusal payload %q", refusalOutcome.Expected, unknownToolRefusalPayload)
	}
	var actualTranscript string
	if err := json.Unmarshal([]byte(refusalOutcome.Actual), &actualTranscript); err != nil {
		t.Fatalf("control actual evidence is not a quoted transcript: %q", refusalOutcome.Actual)
	}
	if strings.Contains(actualTranscript, `"classification":"unknown_tool"`) || strings.Contains(actualTranscript, unknownToolName) {
		t.Fatalf("registered-tool transcript unexpectedly contains the unknown-tool refusal: %q", actualTranscript)
	}
	if !strings.Contains(actualTranscript, registeredToolName) {
		t.Fatalf("control transcript does not show the requested registered tool %q, so the failure may not be caused by the registered-name condition: %q", registeredToolName, actualTranscript)
	}
}

// failingTranscriptOutcome returns the single failed transcript_contains
// outcome and the set of passed expectation kinds (in scenario vocabulary) in
// one probe result. Runner kinds alias scenario vocabulary with hyphens
// ("transcript-contains"), so kinds are normalized before comparison.
func failingTranscriptOutcome(t *testing.T, result probeScenarioResult) (*probeExpectationOutcome, map[string]bool) {
	t.Helper()
	passedKinds := map[string]bool{}
	var refusalOutcome *probeExpectationOutcome
	for i := range result.Expectations {
		outcome := &result.Expectations[i]
		kind := scenarioExpectationKind(outcome.Kind)
		if outcome.Passed {
			passedKinds[kind] = true
			continue
		}
		if kind == "transcript_contains" && refusalOutcome == nil {
			refusalOutcome = outcome
		} else {
			t.Fatalf("unexpected additional failed expectation %+v in %+v", outcome, result)
		}
	}
	if refusalOutcome == nil {
		t.Fatalf("control failure did not identify the transcript_contains refusal expectation: %+v", result)
	}
	return refusalOutcome, passedKinds
}

func scenarioExpectationKind(kind string) string {
	return strings.ReplaceAll(kind, "-", "_")
}

// assertSameExpectationsAsPositiveScenario pins the negative control to the
// exact expectation block used by the positive proof: the two committed
// scenario documents must declare identical expected behavior, so the control
// can never weaken the shared unknown-tool refusal assertion.
func assertSameExpectationsAsPositiveScenario(t *testing.T) {
	t.Helper()
	var positive, control struct {
		Expectations []map[string]any `json:"expectations"`
	}
	readScenarioExpectations(t, locateUnknownToolFixture(t, unknownToolScenario), &positive)
	readScenarioExpectations(t, locateUnknownToolFixture(t, registeredToolScenario), &control)
	if !reflect.DeepEqual(positive.Expectations, control.Expectations) {
		t.Fatalf("negative-control expectations diverge from the positive scenario:\npositive: %#v\ncontrol:  %#v", positive.Expectations, control.Expectations)
	}
}

func readScenarioExpectations(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario %s: %v", path, err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode scenario %s: %v", path, err)
	}
}
