package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The v2d vertical is verified exclusively through the actual agent binary:
// every assertion below reads CLI output (JSONL result lines, summary
// artifact, exit codes) produced by an exec of the built executable over the
// hermetic record/replay transport. No internal Go function is called.
//
// Fixture structure (agent-cli/test/integration/testdata/s2s-v2d):
//   - happy path: three distinct audio-in utterances separated by gaps; each
//     utterance contributes exactly append + commit (client-to-server) and
//     response.created + transcript delta + response.done (server-to-client),
//     so a correctly segmented run records 18 frames and 7 outbound events
//     (1 session setup + 3x(append+commit) = 3 commits).
//   - negative control: the mis-segmented fixture merges utterances one and
//     two into a single commit/turn, recording only 14 frames.

func TestMain(m *testing.M) {
	// os.Exit skips deferred calls, so the build directory is cleaned up in
	// runIntegrationTests before the exit code reaches os.Exit here. Inlining
	// the defer into this function would leak the 44MB agent binary on every
	// run of this package.
	os.Exit(runIntegrationTests(m))
}

func runIntegrationTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "s2s-v2d-agent-binary")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	binary := filepath.Join(dir, "agent")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/agent")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build agent binary: " + err.Error())
	}
	agentBinaryPath = binary
	return m.Run()
}

var agentBinaryPath string

type s2sV2DCLIResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runAgentBinary(t *testing.T, args ...string) s2sV2DCLIResult {
	t.Helper()
	cmd := exec.Command(agentBinaryPath, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run agent %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return s2sV2DCLIResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func decodeS2SV2DJSONL(t *testing.T, text string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(text), "\n")
	decoded := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("decode JSONL line %q: %v", line, err)
		}
		decoded = append(decoded, value)
	}
	return decoded
}

// s2sV2DSummaryLine returns the run-summary JSON object embedded in mixed CLI
// output (the summary line shares stderr with cobra error decoration), or nil.
func s2sV2DSummaryLine(t *testing.T, text string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var value map[string]any
		if json.Unmarshal([]byte(line), &value) == nil && value["status"] != nil && value["total"] != nil {
			return value
		}
	}
	return nil
}

const (
	s2sV2DFixtureDir    = "s2s-v2d"
	s2sV2DHappyFrames   = 18.0
	s2sV2DHappyOutbound = 7.0
)

func TestS2SV2DMultiUtteranceHappyPathOneCommitPerUtterance(t *testing.T) {
	scenario := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance.scenario.json"))

	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := runAgentBinary(t, "probe", "run", scenario,
		"--replay", locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "s2s_v2d_multi_utterance.session.json")),
		"--json", "--out", outPath, "--summary", summaryPath)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	results := decodeS2SV2DJSONL(t, readFile(t, outPath))
	if len(results) != 1 {
		t.Fatalf("result line count = %d, want 1", len(results))
	}
	result := results[0]
	if result["name"] != "s2s_v2d_multi_utterance" || result["pass"] != true {
		t.Fatalf("happy-path scenario did not pass: %v", result)
	}
	if result["frames"] != s2sV2DHappyFrames {
		t.Fatalf("frames = %v, want %v (3 utterances x 5 turn events + setup/close)", result["frames"], s2sV2DHappyFrames)
	}
	if result["ticks"] != s2sV2DHappyOutbound {
		t.Fatalf("outbound ticks = %v, want %v (1 session update + 3x(append+commit) = exactly one commit per utterance)", result["ticks"], s2sV2DHappyOutbound)
	}
	if result["terminal_reason"] != "disconnect" {
		t.Fatalf("terminal reason = %v, want disconnect", result["terminal_reason"])
	}
	for _, expectation := range result["expectations"].([]any) {
		if expectation.(map[string]any)["passed"] != true {
			t.Fatalf("every segmentation expectation must pass on the happy path: %v", expectation)
		}
	}

	summary := decodeS2SV2DJSONL(t, readFile(t, summaryPath))
	if len(summary) != 1 || summary[0]["status"] != "pass" || summary[0]["passed"] != float64(1) || summary[0]["failed"] != float64(0) {
		t.Fatalf("summary artifact must count the case as passed: %v", summary)
	}
}

func TestS2SV2DMisSegmentedFixtureFailsViaCLI(t *testing.T) {
	scenario := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance_missegmented.scenario.json"))

	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := runAgentBinary(t, "probe", "run", scenario,
		"--replay", locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "s2s_v2d_multi_utterance_merged.session.json")),
		"--json", "--out", outPath, "--summary", summaryPath)
	if run.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero for the mis-segmented fixture; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "1 of 1 probe scenarios failed") {
		t.Fatalf("failure not reported on stderr: %q", run.stderr)
	}

	results := decodeS2SV2DJSONL(t, readFile(t, outPath))
	if len(results) != 1 || results[0]["pass"] != false {
		t.Fatalf("mis-segmented scenario must fail: %v", results)
	}
	failedKinds := map[string]bool{}
	for _, expectation := range results[0]["expectations"].([]any) {
		outcome := expectation.(map[string]any)
		if outcome["passed"] == false {
			failedKinds[outcome["kind"].(string)] = true
			if outcome["expected"] == "" || outcome["actual"] == "" {
				t.Fatalf("failed expectation lacks expected/actual detail: %v", outcome)
			}
		}
	}
	if !failedKinds["frame-count"] {
		t.Fatalf("failure must name the unmet segmentation frame-count expectation: %v", results[0]["expectations"])
	}

	summary := decodeS2SV2DJSONL(t, readFile(t, summaryPath))
	if len(summary) != 1 || summary[0]["status"] != "fail" || summary[0]["failed"] != float64(1) {
		t.Fatalf("summary artifact must reflect the failure: %v", summary)
	}
}

func TestS2SV2DSuiteSelectsEachFixtureByNameAndBothPassOrFailCorrectly(t *testing.T) {
	happy := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance.scenario.json"))
	mis := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance_missegmented.scenario.json"))

	run := runAgentBinary(t, "probe", "run",
		"--replay", locateCLIFixture(t, s2sV2DFixtureDir),
		happy, mis, "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 when the negative control runs alongside the happy path; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	results := decodeS2SV2DJSONL(t, run.stdout)
	if len(results) != 2 { // one JSONL scenario result line per selected scenario
		t.Fatalf("stdout line count = %d, want 2: %q", len(results), run.stdout)
	}
	byName := map[string]map[string]any{}
	for _, result := range results {
		byName[result["name"].(string)] = result
	}
	if byName["s2s_v2d_multi_utterance"]["pass"] != true {
		t.Fatalf("happy-path case must still pass in the combined run: %v", byName["s2s_v2d_multi_utterance"])
	}
	if byName["s2s_v2d_multi_utterance_merged"]["pass"] != false {
		t.Fatalf("mis-segmented case must fail in the combined run: %v", byName["s2s_v2d_multi_utterance_merged"])
	}
	summary := s2sV2DSummaryLine(t, run.stderr)
	if summary == nil || summary["status"] != "fail" ||
		summary["total"] != float64(2) || summary["passed"] != float64(1) || summary["failed"] != float64(1) {
		t.Fatalf("unexpected combined-run summary on stderr: %q", run.stderr)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
