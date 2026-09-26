package integration

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
)

// The v2d vertical is verified through CLI output only: every assertion below
// reads JSONL result lines, the summary artifact and the exit status produced
// by the shipped command over the hermetic record/replay transport. The two
// scenario tests run the command entrypoint in-process on a virtual clock
// (clitest); TestAgentBinaryProbeRunReportsExitStatusAcrossProcessBoundary
// runs the same invocations through the built executable.
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
	flag.Parse()
	if listFlag := flag.Lookup("test.list"); listFlag != nil && listFlag.Value.String() != "" {
		// Listing tests (used by scripts/go-test-shards.sh) runs no test, so
		// it needs none of the process-boundary binaries.
		return m.Run()
	}
	if os.Getenv(toolErrorPanicHelperEnv) != "" {
		// The panic control's re-executed helper runs one in-process test and
		// execs no process-boundary binary.
		return m.Run()
	}

	// A shared directory (scripts/go-test-shards.sh --shared-dir-env) lets
	// every shard process of one run reuse a single build of the binaries.
	dir := os.Getenv(sharedBinaryDirEnv)
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "s2s-v2d-agent-binary")
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(os.Stderr, "remove integration binary directory: %v\n", err)
			}
		}()
	}
	if err := buildIntegrationBinaries(dir); err != nil {
		panic(err.Error())
	}
	return m.Run()
}

// sharedBinaryDirEnv names a directory holding the process-boundary binaries
// for this package. Missing binaries are built into it and kept for reuse.
const sharedBinaryDirEnv = "AGENT_CLI_INTEGRATION_SHARED_DIR"

func buildIntegrationBinaries(dir string) error {
	agentBinaryPath = filepath.Join(dir, "agent")
	audioDeviceServerBinaryPath = filepath.Join(dir, "audio-device-server")
	mockToolAgentBinaryPath = filepath.Join(dir, "mock-tool-agent")
	// The binaries are independent link targets over a shared build cache;
	// building them concurrently removes two serial links from package setup.
	builds := []struct{ name, output, source string }{
		{name: "agent", output: agentBinaryPath, source: "../../cmd/agent"},
		{name: "audio-device-server", output: audioDeviceServerBinaryPath, source: "../../cmd/audio-device-server"},
		{name: "mock-tool-agent", output: mockToolAgentBinaryPath, source: "./testcmd/mock-tool-agent"},
	}
	errs := make([]error, len(builds))
	var wg sync.WaitGroup
	for index, build := range builds {
		if _, err := os.Stat(build.output); err == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Link to a temporary name and rename, so a concurrent reader of
			// the shared directory never executes a partially written binary.
			partial := fmt.Sprintf("%s.partial-%d", build.output, os.Getpid())
			cmd := exec.Command("go", "build", "-o", partial, build.source)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				errs[index] = fmt.Errorf("build %s binary: %w", build.name, err)
				return
			}
			if err := os.Rename(partial, build.output); err != nil {
				errs[index] = fmt.Errorf("install %s binary: %w", build.name, err)
				return
			}
			warmBinary(build.output)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// warmBinary executes a freshly linked binary once. The first exec of a new
// binary on macOS waits for a code assessment that can take seconds on a
// loaded machine; paying it here keeps it out of tests' readiness bounds.
func warmBinary(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Only the exec matters; a non-zero help exit status is irrelevant.
	var exitErr *exec.ExitError
	if err := exec.CommandContext(ctx, path, "--help").Run(); err != nil && !errors.As(err, &exitErr) {
		fmt.Fprintf(os.Stderr, "warm %s: %v\n", path, err)
	}
}

var (
	agentBinaryPath             string
	audioDeviceServerBinaryPath string
	mockToolAgentBinaryPath     string
)

type s2sV2DCLIResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// s2sV2DRunner runs one agent invocation and reports it as a process would.
type s2sV2DRunner func(t *testing.T, args ...string) s2sV2DCLIResult

func runAgentInProcess(t *testing.T, args ...string) s2sV2DCLIResult {
	t.Helper()
	run := clitest.Run(t, clitest.Invocation{Args: args})
	return s2sV2DCLIResult{exitCode: run.ExitCode, stdout: run.Stdout, stderr: run.Stderr}
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

const (
	s2sV2DFixtureDir    = "s2s-v2d"
	s2sV2DHappyFrames   = 18.0
	s2sV2DHappyOutbound = 7.0
)

func TestS2SV2DMultiUtteranceHappyPathOneCommitPerUtterance(t *testing.T) {
	clitest.Test(t, func(t *testing.T) { assertS2SV2DHappyPath(t, runAgentInProcess) })
}

func TestS2SV2DMisSegmentedFixtureFailsViaCLI(t *testing.T) {
	clitest.Test(t, func(t *testing.T) { assertS2SV2DMisSegmented(t, runAgentInProcess) })
}

// TestAgentBinaryProbeRunReportsExitStatusAcrossProcessBoundary is the
// real-process proof for argv parsing, the process exit status, and the
// stdout/stderr/file outputs of the built executable, for a passing (exit 0)
// and a failing (non-zero) probe run.
func TestAgentBinaryProbeRunReportsExitStatusAcrossProcessBoundary(t *testing.T) {
	assertS2SV2DHappyPath(t, runAgentBinary)
	assertS2SV2DMisSegmented(t, runAgentBinary)
}

func assertS2SV2DHappyPath(t *testing.T, runAgent s2sV2DRunner) {
	t.Helper()
	scenario := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance.scenario.json"))

	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := runAgent(t, "probe", "run", scenario,
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
	for _, expectation := range mustAs[[]any](t, result["expectations"]) {
		if mustAs[map[string]any](t, expectation)["passed"] != true {
			t.Fatalf("every segmentation expectation must pass on the happy path: %v", expectation)
		}
	}

	summary := decodeS2SV2DJSONL(t, readFile(t, summaryPath))
	if len(summary) != 1 || summary[0]["status"] != "pass" || summary[0]["passed"] != float64(1) || summary[0][rtStatusFailed] != float64(0) {
		t.Fatalf("summary artifact must count the case as passed: %v", summary)
	}
}

func assertS2SV2DMisSegmented(t *testing.T, runAgent s2sV2DRunner) {
	t.Helper()
	scenario := locateCLIFixture(t, filepath.Join(s2sV2DFixtureDir, "scenarios", "s2s_v2d_multi_utterance_missegmented.scenario.json"))

	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := runAgent(t, "probe", "run", scenario,
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
	for _, expectation := range mustAs[[]any](t, results[0]["expectations"]) {
		outcome := mustAs[map[string]any](t, expectation)
		if outcome["passed"] == false {
			failedKinds[mustAs[string](t, outcome["kind"])] = true
			if outcome["expected"] == "" || outcome["actual"] == "" {
				t.Fatalf("failed expectation lacks expected/actual detail: %v", outcome)
			}
		}
	}
	if !failedKinds["frame-count"] {
		t.Fatalf("failure must name the unmet segmentation frame-count expectation: %v", results[0]["expectations"])
	}

	summary := decodeS2SV2DJSONL(t, readFile(t, summaryPath))
	if len(summary) != 1 || summary[0]["status"] != "fail" || summary[0][rtStatusFailed] != float64(1) {
		t.Fatalf("summary artifact must reflect the failure: %v", summary)
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
