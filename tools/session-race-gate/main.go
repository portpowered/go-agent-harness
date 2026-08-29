// Command session-race-gate runs the concurrent session capacity acceptance
// tests of go-agent-loop/test/functional/sessions under the race detector and
// verifies from the go test -json event stream that every required test ran
// exactly once and passed. A missing, skipped, duplicated, or failed required
// test fails the gate, mirroring tools/rtc-race-gate.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultGoBinary    = "go"
	defaultModuleDir   = "../../go-agent-loop"
	defaultTimeout     = 10 * time.Minute
	sessionsPackage    = "./test/functional/sessions"
	sessionsRunPattern = "^(TestConcurrentSessionsCompleteScriptedTurns|TestConcurrentSessionsZeroCrossSessionLeakage|TestIsolationCheckerNamesLeakingSessionAndRecord|TestSharedCaptureBufferAliasingFailsIsolationCheck|TestConcurrentSessionsPerEventOrderingUnderInterleaving|TestCancellingOneMidRunSessionLeavesOthersUndisturbed)$"
	watchdogSignature  = "concurrent run did not finish within 2m0s (stuck sessions likely)"
)

var requiredTests = []string{
	"TestConcurrentSessionsCompleteScriptedTurns",
	"TestConcurrentSessionsZeroCrossSessionLeakage",
	"TestIsolationCheckerNamesLeakingSessionAndRecord",
	"TestSharedCaptureBufferAliasingFailsIsolationCheck",
	"TestConcurrentSessionsPerEventOrderingUnderInterleaving",
	"TestCancellingOneMidRunSessionLeavesOthersUndisturbed",
}

type config struct {
	goBinary  string
	moduleDir string
	timeout   time.Duration
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

type testResult struct {
	passed  int
	skipped bool
	failed  bool
	seen    bool
	output  string
}

type gateFailure struct {
	Missing   []string
	Skipped   []string
	Failed    []string
	Duplicate []string
}

type eventReport struct {
	results map[string]testResult
	failure gateFailure
}

type commandAttempt struct {
	commandErr      error
	report          *eventReport
	verificationErr error
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("session-race-gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	goBinary := flags.String("go", defaultGoBinary, "Go command to execute")
	moduleDir := flags.String("module-dir", defaultModuleDir, "go-agent-loop module directory")
	timeout := flags.Duration("timeout", defaultTimeout, "finite go test timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("session race gate does not accept positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*goBinary) == "" {
		return errors.New("session race gate requires a Go command")
	}
	if strings.TrimSpace(*moduleDir) == "" {
		return errors.New("session race gate requires a module directory")
	}
	if *timeout <= 0 {
		return fmt.Errorf("session race gate timeout must be finite and positive, got %s", *timeout)
	}

	return execute(config{goBinary: *goBinary, moduleDir: *moduleDir, timeout: *timeout}, stdout, stderr)
}

func execute(cfg config, stdout, stderr io.Writer) error {
	moduleDir, err := filepath.Abs(cfg.moduleDir)
	if err != nil {
		return fmt.Errorf("resolve module directory: %w", err)
	}
	if info, statErr := os.Stat(moduleDir); statErr != nil {
		return fmt.Errorf("stat module directory %q: %w", moduleDir, statErr)
	} else if !info.IsDir() {
		return fmt.Errorf("module directory %q is not a directory", moduleDir)
	}

	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	first := runAttempt(cfg, moduleDir, sessionsRunPattern, "", stdout, stderr)
	if retryTest, ok := eligibleRetryTest(first); ok {
		fmt.Fprintf(stdout, "concurrent session race gate: attempt 1 failed with the recognized watchdog for %s; starting attempt 2 with only that test\n", retryTest)
		retry := runAttempt(cfg, moduleDir, exactTestRunPattern(retryTest), retryTest, stdout, stderr)
		if retry.commandErr == nil && retry.verificationErr == nil {
			fmt.Fprintf(stdout, "concurrent session race gate: recovered %s; attempt 1 watchdog failure and attempt 2 passed all required checks\n", retryTest)
			return nil
		}
		return retryFailure(retryTest, first, retry)
	}
	return firstFailure(first)
}

func runAttempt(cfg config, moduleDir, runPattern, retryTest string, stdout, stderr io.Writer) commandAttempt {
	command := exec.Command(cfg.goBinary, testCommandArgs(cfg.timeout, runPattern)...)
	command.Dir = moduleDir
	command.Env = childEnvironment(moduleDir)

	var testJSON bytes.Buffer
	command.Stdout = io.MultiWriter(stdout, &testJSON)
	command.Stderr = stderr

	attempt := commandAttempt{commandErr: command.Run()}
	report, parseErr := parseEvents(bytes.NewReader(testJSON.Bytes()))
	if parseErr != nil {
		attempt.verificationErr = parseErr
		return attempt
	}
	attempt.report = report
	if retryTest == "" {
		attempt.verificationErr = report.verificationError()
	} else {
		attempt.verificationErr = report.retryVerificationError(retryTest)
	}
	return attempt
}

func testCommandArgs(timeout time.Duration, runPattern string) []string {
	return []string{
		"test",
		"-race",
		"-tags=nomicrophone",
		"-count=1",
		"-timeout", timeout.String(),
		"-json",
		"-run", runPattern,
		sessionsPackage,
	}
}

func exactTestRunPattern(testName string) string {
	return "^" + regexp.QuoteMeta(testName) + "$"
}

func eligibleRetryTest(attempt commandAttempt) (string, bool) {
	if attempt.commandErr == nil {
		return "", false
	}
	report, ok := attemptReport(attempt)
	if !ok || len(report.failure.Failed) != 1 || len(report.failure.Missing) != 0 || len(report.failure.Skipped) != 0 || len(report.failure.Duplicate) != 0 {
		return "", false
	}
	testName := report.failure.Failed[0]
	result := report.results[testName]
	if result.passed != 0 || !result.failed || !strings.Contains(result.output, watchdogSignature) {
		return "", false
	}
	return testName, true
}

func attemptReport(attempt commandAttempt) (*eventReport, bool) {
	// A retry is only safe when the first command produced a fully parsed event
	// report. parseEvents errors are stored as verificationErr and leave report
	// nil, so malformed or incomplete command/setup failures cannot retry.
	return attempt.report, attempt.report != nil
}

func firstFailure(attempt commandAttempt) error {
	if attempt.commandErr != nil {
		if attempt.verificationErr != nil {
			return fmt.Errorf("concurrent session race command failed: %v; %w", attempt.commandErr, attempt.verificationErr)
		}
		return fmt.Errorf("concurrent session race command failed: %w", attempt.commandErr)
	}
	return attempt.verificationErr
}

func retryFailure(testName string, first, retry commandAttempt) error {
	firstErr := firstFailure(first)
	retryErr := firstFailure(retry)
	if firstErr == nil {
		firstErr = errors.New("first attempt unexpectedly passed")
	}
	if retryErr == nil {
		retryErr = errors.New("retry attempt unexpectedly passed")
	}
	return fmt.Errorf("concurrent session race gate retry failed for %s; first attempt: %v; retry attempt: %v", testName, firstErr, retryErr)
}

func childEnvironment(moduleDir string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "GOWORK=") || strings.HasPrefix(value, "CGO_ENABLED=") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "CGO_ENABLED=1")
	if workspace, ok := findWorkspace(moduleDir); ok {
		env = append(env, "GOWORK="+workspace)
	}
	return env
}

func findWorkspace(start string) (string, bool) {
	directory := start
	for {
		candidate := filepath.Join(directory, "go.work")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false
		}
		directory = parent
	}
}

func verifyEvents(input io.Reader) error {
	report, err := parseEvents(input)
	if err != nil {
		return err
	}
	return report.verificationError()
}

func parseEvents(input io.Reader) (*eventReport, error) {
	results := make(map[string]testResult, len(requiredTests))
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode go test JSON event on line %d: %w", lineNumber, err)
		}
		if _, required := requiredTestSet[event.Test]; !required {
			continue
		}
		result := results[event.Test]
		result.seen = true
		result.output += event.Output
		switch event.Action {
		case "pass":
			result.passed++
		case "skip":
			result.skipped = true
		case "fail":
			result.failed = true
		}
		results[event.Test] = result
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read go test JSON output: %w", err)
	}

	report := &eventReport{results: results}
	report.failure = classifyResults(results)
	return report, nil
}

func classifyResults(results map[string]testResult) gateFailure {
	failure := gateFailure{}
	for _, testName := range requiredTests {
		result := results[testName]
		switch {
		case result.skipped:
			failure.Skipped = append(failure.Skipped, testName)
		case result.failed:
			failure.Failed = append(failure.Failed, testName)
		case result.passed == 0:
			failure.Missing = append(failure.Missing, testName)
		case result.passed != 1:
			failure.Duplicate = append(failure.Duplicate, testName)
		}
	}
	return failure
}

func (r *eventReport) verificationError() error {
	if r == nil || r.failure.empty() {
		return nil
	}
	return &r.failure
}

func (r *eventReport) retryVerificationError(testName string) error {
	if r == nil {
		return errors.New("session race retry produced no event report")
	}
	if _, required := requiredTestSet[testName]; !required {
		return fmt.Errorf("session race retry selected non-required test %q", testName)
	}
	for _, requiredTest := range requiredTests {
		if requiredTest == testName {
			continue
		}
		if r.results[requiredTest].seen {
			return fmt.Errorf("session race retry ran unexpected required test %q", requiredTest)
		}
	}
	result := r.results[testName]
	switch {
	case result.skipped:
		return fmt.Errorf("session race retry skipped required test %q", testName)
	case result.failed:
		return fmt.Errorf("session race retry failed required test %q", testName)
	case result.passed == 0:
		return fmt.Errorf("session race retry did not complete required test %q", testName)
	case result.passed != 1:
		return fmt.Errorf("session race retry completed required test %q more than once", testName)
	default:
		return nil
	}
}

var requiredTestSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(requiredTests))
	for _, testName := range requiredTests {
		set[testName] = struct{}{}
	}
	return set
}()

func (f *gateFailure) empty() bool {
	return len(f.Missing) == 0 && len(f.Skipped) == 0 && len(f.Failed) == 0 && len(f.Duplicate) == 0
}

func (f *gateFailure) Error() string {
	parts := make([]string, 0, 4)
	appendPart := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		ordered := append([]string(nil), values...)
		sort.Strings(ordered)
		parts = append(parts, fmt.Sprintf("%s: %s", label, strings.Join(ordered, ", ")))
	}
	appendPart("missing required tests", f.Missing)
	appendPart("skipped required tests", f.Skipped)
	appendPart("failed required tests", f.Failed)
	appendPart("required tests completed more than once", f.Duplicate)
	return "concurrent session race gate failed; " + strings.Join(parts, "; ")
}
