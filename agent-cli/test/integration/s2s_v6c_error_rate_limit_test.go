package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// The s2s-v6c vertical proves that a provider rate-limit response classifies
// distinctly through the production CLI: the registered throttle case exits
// zero offline with terminal_reason=error:rate_limited, while the comparable
// authentication and invalid-request controls fed into the UNCHANGED
// expectation fail closed with their own classifications named beside the
// expected one. All evidence flows through real "agent probe run" argv over
// committed session fixtures under a hard parent deadline; no probe runner,
// replay helper, or classifier is called directly, and no credential or
// network is touched.

const (
	// v6cSuitePrefix selects every registered v6c case by suite prefix.
	v6cSuitePrefix = "s2s-v6c-error-rate-limit"
	// v6cCaseID is the single registered throttled case.
	v6cCaseID = v6cSuitePrefix + "-throttled"
	// v6cProbeDeadline is the hard parent deadline the proof doc documents:
	// every CLI run below must finish hermetically inside it.
	v6cProbeDeadline = 2 * time.Second

	v6cThrottledFixture       = "s2s-v6c-error-rate-limit-throttled.session.json"
	v6cNegativeAuthFixture    = "s2s-v6c-error-rate-limit-negative-auth.session.json"
	v6cNegativeInvalidFixture = "s2s-v6c-error-rate-limit-negative-invalid-request.session.json"
)

// v6cExecution captures everything a bounded CLI run produced so failures can
// be diagnosed without re-running anything.
type v6cExecution struct {
	argv             []string
	fixture          string
	exitCode         int
	stdout           string
	stderr           string
	execErr          error
	deadlineExceeded bool
}

// v6cSecretPattern matches argv elements carrying credential-shaped content;
// matched elements are redacted before any diagnostic leaves the test.
var v6cSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)`)

func v6cRedactArgv(argv []string) []string {
	redacted := make([]string, len(argv))
	for i, arg := range argv {
		if v6cSecretPattern.MatchString(arg) {
			redacted[i] = "[REDACTED]"
			continue
		}
		redacted[i] = arg
	}
	return redacted
}

func (run v6cExecution) failureDetail() string {
	return fmt.Sprintf(
		"argv=%v\nfixture=%s\nexit=%d\ndeadlineExceeded=%v\nexecErr=%v\nstdout=%q\nstderr=%q",
		v6cRedactArgv(run.argv), run.fixture, run.exitCode, run.deadlineExceeded,
		run.execErr, run.stdout, run.stderr)
}

// runV6CProbe drives the production root command with real argv under the
// hard two-second parent deadline. Exit status mirrors the binary contract:
// any command error means a non-zero exit.
func runV6CProbe(t *testing.T, fixture string, argv ...string) v6cExecution {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize production CLI composition: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), v6cProbeDeadline)
	defer cancel()

	rootCmd := agentCLI.Generate()
	writer := NewTestWriter()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs(argv)

	execErr := rootCmd.ExecuteContext(ctx)
	run := v6cExecution{
		argv:             argv,
		fixture:          fixture,
		stdout:           writer.StdoutString(),
		stderr:           writer.StderrString(),
		execErr:          execErr,
		deadlineExceeded: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	if execErr != nil {
		run.exitCode = 1
	}
	return run
}

// v6cSharedFixture resolves a committed capture from the gateway-owned
// session-fixture root.
func v6cSharedFixture(t *testing.T, name string) string {
	t.Helper()
	return locateSharedSessionFixture(t, name)
}

// v6cDecodeResults splits the pure JSONL stream: one result object per stdout
// line plus the machine summary on stderr.
func v6cDecodeResults(t *testing.T, run v6cExecution) ([]map[string]any, map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(run.stdout), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("no JSONL result lines:\n%s", run.failureDetail())
	}
	results := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode result line %q: %v\n%s", line, err, run.failureDetail())
		}
		results = append(results, decoded)
	}
	summaryLine := strings.SplitN(strings.TrimSpace(run.stderr), "\n", 2)[0]
	var summary map[string]any
	if err := json.Unmarshal([]byte(summaryLine), &summary); err != nil || summary["status"] == nil {
		t.Fatalf("stderr first line %q is not a run summary: %v\n%s", summaryLine, err, run.failureDetail())
	}
	return results, summary
}

// v6cRequireSingleResult asserts exactly one result line and returns it.
func v6cRequireSingleResult(t *testing.T, run v6cExecution) map[string]any {
	t.Helper()
	results, _ := v6cDecodeResults(t, run)
	if len(results) != 1 {
		t.Fatalf("result line count = %d, want 1:\n%s", len(results), run.failureDetail())
	}
	return results[0]
}

// v6cOutcome extracts the terminal-reason expectation outcome from a result.
func v6cOutcome(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	outcomes, ok := result["expectations"].([]any)
	if !ok || len(outcomes) != 1 {
		t.Fatalf("result must carry exactly one expectation outcome: %v", result)
	}
	outcome, _ := outcomes[0].(map[string]any)
	return outcome
}

func TestV6CErrorRateLimitThrottledExitsZeroOffline(t *testing.T) {
	fixture := v6cSharedFixture(t, v6cThrottledFixture)
	run := runV6CProbe(t, fixture,
		"probe", "run", "--scenario", v6cCaseID, "--replay", fixture, "--json")

	if run.exitCode != 0 {
		t.Fatalf("throttled case must exit zero:\n%s", run.failureDetail())
	}
	if run.deadlineExceeded {
		t.Fatalf("hermetic replay exceeded its %s deadline:\n%s", v6cProbeDeadline, run.failureDetail())
	}
	result := v6cRequireSingleResult(t, run)
	if result["name"] != v6cCaseID {
		t.Fatalf("result name = %v, want %q:\n%s", result["name"], v6cCaseID, run.failureDetail())
	}
	if result["pass"] != true {
		t.Fatalf("throttled case must pass:\n%s", run.failureDetail())
	}
	if result["terminal_reason"] != "error:rate_limited" {
		t.Fatalf("terminal_reason = %v, want error:rate_limited:\n%s", result["terminal_reason"], run.failureDetail())
	}
	outcome := v6cOutcome(t, result)
	if outcome["kind"] != "terminal-reason" || outcome["passed"] != true {
		t.Fatalf("terminal-reason outcome must pass: %v", outcome)
	}
	_, summary := v6cDecodeResults(t, run)
	if summary["status"] != "pass" || summary["passed"] != float64(1) || summary["failed"] != float64(0) {
		t.Fatalf("summary must report status=pass passed=1 failed=0: %v", summary)
	}
}

func TestV6CSuitePrefixSelectionResolvesThrottledCase(t *testing.T) {
	fixtureDir := filepath.Dir(gwtesting.SharedSessionFixturePath(v6cThrottledFixture))
	run := runV6CProbe(t, fixtureDir,
		"probe", "run", "--scenario", v6cSuitePrefix, "--replay", fixtureDir, "--json")

	if run.exitCode != 0 {
		t.Fatalf("suite-prefix selection must exit zero:\n%s", run.failureDetail())
	}
	if run.deadlineExceeded {
		t.Fatalf("suite-prefix selection exceeded its %s deadline:\n%s", v6cProbeDeadline, run.failureDetail())
	}
	result := v6cRequireSingleResult(t, run)
	if result["name"] != v6cCaseID || result["pass"] != true || result["terminal_reason"] != "error:rate_limited" {
		t.Fatalf("suite prefix %q must resolve to the passing throttled case: %v\n%s",
			v6cSuitePrefix, result, run.failureDetail())
	}
}

// TestV6CNegativeAuthControlFailsClosed feeds the invalid_api_key capture into
// the unchanged rate_limited expectation: the run must fail closed naming
// observed actual=error:authentication beside expected=error:rate_limited.
func TestV6CNegativeAuthControlFailsClosed(t *testing.T) {
	fixture := v6cSharedFixture(t, v6cNegativeAuthFixture)
	run := runV6CProbe(t, fixture,
		"probe", "run", "--scenario", v6cCaseID, "--replay", fixture, "--json")

	if run.exitCode == 0 {
		t.Fatalf("auth control must not satisfy the rate_limited expectation:\n%s", run.failureDetail())
	}
	if run.deadlineExceeded {
		t.Fatalf("auth control exceeded its %s deadline instead of failing on classification:\n%s",
			v6cProbeDeadline, run.failureDetail())
	}
	if run.execErr == nil || !strings.Contains(run.execErr.Error(), "1 of 1 probe scenarios failed") {
		t.Fatalf("control must fail as a scenario classification mismatch, not a launch or fixture-read error:\n%s",
			run.failureDetail())
	}
	result := v6cRequireSingleResult(t, run)
	if result["pass"] != false || result["terminal_reason"] != "error:authentication" {
		t.Fatalf("auth control must fail with observed error:authentication: %v", result)
	}
	outcome := v6cOutcome(t, result)
	if !strings.Contains(fmt.Sprint(outcome["expected"]), "error:rate_limited") ||
		!strings.Contains(fmt.Sprint(outcome["actual"]), "error:authentication") {
		t.Fatalf("failed outcome must report expected vs actual classifications: %v", outcome)
	}
}

// TestV6CNegativeInvalidRequestControlFailsClosed feeds the
// invalid_request_error/bad_request capture into the unchanged rate_limited
// expectation: the run must fail closed naming observed
// actual=error:provider_rejected beside expected=error:rate_limited.
func TestV6CNegativeInvalidRequestControlFailsClosed(t *testing.T) {
	fixture := v6cSharedFixture(t, v6cNegativeInvalidFixture)
	run := runV6CProbe(t, fixture,
		"probe", "run", "--scenario", v6cCaseID, "--replay", fixture, "--json")

	if run.exitCode == 0 {
		t.Fatalf("invalid-request control must not satisfy the rate_limited expectation:\n%s", run.failureDetail())
	}
	if run.deadlineExceeded {
		t.Fatalf("invalid-request control exceeded its %s deadline instead of failing on classification:\n%s",
			v6cProbeDeadline, run.failureDetail())
	}
	if run.execErr == nil || !strings.Contains(run.execErr.Error(), "1 of 1 probe scenarios failed") {
		t.Fatalf("control must fail as a scenario classification mismatch, not a launch or fixture-read error:\n%s",
			run.failureDetail())
	}
	result := v6cRequireSingleResult(t, run)
	if result["pass"] != false || result["terminal_reason"] != "error:provider_rejected" {
		t.Fatalf("invalid-request control must fail with observed error:provider_rejected: %v", result)
	}
	outcome := v6cOutcome(t, result)
	if !strings.Contains(fmt.Sprint(outcome["expected"]), "error:rate_limited") ||
		!strings.Contains(fmt.Sprint(outcome["actual"]), "error:provider_rejected") {
		t.Fatalf("failed outcome must report expected vs actual classifications: %v", outcome)
	}
}

// TestV6CControlsReplayCleanlyUnderOwnClassification proves both controls are
// healthy replays: when each capture's own classification is asserted through
// the same CLI route, the run exits zero inside the deadline — so the failing
// runs above failed on the classification mismatch alone. Assertion scenarios
// live in temp directories; shared fixture state is never mutated.
func TestV6CControlsReplayCleanlyUnderOwnClassification(t *testing.T) {
	cases := []struct {
		name           string
		fixtureName    string
		classification string
	}{
		{name: "auth", fixtureName: v6cNegativeAuthFixture, classification: "error:authentication"},
		{name: "invalid-request", fixtureName: v6cNegativeInvalidFixture, classification: "error:provider_rejected"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := v6cSharedFixture(t, testCase.fixtureName)
			document := fmt.Sprintf(`{
				"id": "v6c-control-%s",
				"name": "v6c-control-%s",
				"steps": [
					{"type": "send_text", "text": "probe input"},
					{"type": "close"}
				],
				"expectations": [{"type": "terminal_reason", "value": %q}]
			}`, testCase.name, testCase.name, testCase.classification)
			scenarioPath := filepath.Join(t.TempDir(), "v6c-control.scenario.json")
			if err := os.WriteFile(scenarioPath, []byte(document), 0o644); err != nil {
				t.Fatalf("write own-classification scenario: %v", err)
			}
			run := runV6CProbe(t, fixture,
				"probe", "run", scenarioPath, "--replay", fixture, "--json")
			if run.exitCode != 0 {
				t.Fatalf("capture must replay cleanly under its own classification %q:\n%s",
					testCase.classification, run.failureDetail())
			}
			if run.deadlineExceeded {
				t.Fatalf("own-classification replay exceeded its %s deadline:\n%s", v6cProbeDeadline, run.failureDetail())
			}
			result := v6cRequireSingleResult(t, run)
			if result["pass"] != true {
				t.Fatalf("own-classification assertion must pass: %v", result)
			}
		})
	}
}
