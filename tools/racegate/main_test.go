package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testGateName      = "example race gate"
	watchdogSignature = "concurrent run did not finish within 2m0s (stuck sessions likely)"
	examplePackage    = "./pkg/example"
)

func requiredTestNames() []string {
	return []string{"TestAlphaConcurrent", "TestBetaConcurrent", "TestGammaConcurrent"}
}

func verify(input string) error {
	return verifyEvents(testGateName, requiredTestNames(), strings.NewReader(input))
}

func gateConfig(t *testing.T, goBinary, retryOnOutput string) config {
	t.Helper()
	return config{name: testGateName, goBinary: goBinary, moduleDir: t.TempDir(), pkg: examplePackage, required: requiredTestNames(), retryOnOutput: retryOnOutput, timeout: time.Second}
}

func TestVerifyEventsAcceptsExactlyOneNonSkippedCompletion(t *testing.T) {
	var output strings.Builder
	for _, testName := range requiredTestNames() {
		output.WriteString(`{"Action":"run","Package":"example/pkg","Test":"`)
		output.WriteString(testName)
		output.WriteString(`"}` + "\n")
		output.WriteString(`{"Action":"pass","Package":"example/pkg","Test":"`)
		output.WriteString(testName)
		output.WriteString(`"}` + "\n")
	}
	if err := verify(output.String()); err != nil {
		t.Fatalf("verifyEvents() error = %v", err)
	}
}

func TestVerifyEventsRejectsMissingRequiredTest(t *testing.T) {
	input := `{"Action":"pass","Package":"example/pkg","Test":"TestAlphaConcurrent"}` + "\n"
	err := verify(input)
	if err == nil || !strings.Contains(err.Error(), "missing required tests") || !strings.Contains(err.Error(), "TestGammaConcurrent") {
		t.Fatalf("verifyEvents() error = %v, want missing-test diagnostic", err)
	}
}

func TestVerifyEventsRejectsSkippedRequiredTest(t *testing.T) {
	input := `{"Action":"skip","Package":"example/pkg","Test":"TestBetaConcurrent"}` + "\n"
	err := verify(input)
	if err == nil || !strings.Contains(err.Error(), "skipped required tests") || !strings.Contains(err.Error(), "TestBetaConcurrent") {
		t.Fatalf("verifyEvents() error = %v, want skipped-test diagnostic", err)
	}
}

func TestVerifyEventsRejectsDuplicateCompletion(t *testing.T) {
	var input strings.Builder
	for _, action := range []string{"pass", "pass"} {
		input.WriteString(`{"Action":"` + action + `","Package":"example/pkg","Test":"TestBetaConcurrent"}` + "\n")
	}
	err := verify(input.String())
	if err == nil || !strings.Contains(err.Error(), "completed more than once") {
		t.Fatalf("verifyEvents() error = %v, want duplicate-completion diagnostic", err)
	}
}

func TestVerifyEventsRejectsMalformedOutput(t *testing.T) {
	err := verify("go test failed before JSON output\n")
	if err == nil || !strings.Contains(err.Error(), "decode go test JSON event") {
		t.Fatalf("verifyEvents() error = %v, want malformed-output diagnostic", err)
	}
}

func TestExecuteDoesNotRetrySuccessfulFirstAttempt(t *testing.T) {
	fakeGo, stateDir := newFakeGo(t, []fakeAttempt{{output: allRequiredPassOutput(t)}})
	var stdout, stderr strings.Builder
	err := execute(t.Context(), gateConfig(t, fakeGo, watchdogSignature), &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute() error = %v, want nil", err)
	}
	if got := fakeAttemptCount(t, stateDir); got != 1 {
		t.Fatalf("fake go invocation count = %d, want one first attempt", got)
	}
	if strings.Contains(stdout.String(), "attempt 2") {
		t.Fatalf("successful first attempt unexpectedly retried: %q", stdout.String())
	}
}

func TestExecuteRetriesOnlyRecognizedWatchdogAndRecovers(t *testing.T) {
	failedTest := requiredTestNames()[0]
	fakeGo, stateDir := newFakeGo(t, []fakeAttempt{
		{output: oneWatchdogFailureOutput(t, failedTest), status: 1},
		{output: retryPassOutput(t, failedTest)},
	})
	var stdout, stderr strings.Builder
	err := execute(t.Context(), gateConfig(t, fakeGo, watchdogSignature), &stdout, &stderr)
	if err != nil {
		t.Fatalf("execute() error = %v, want recovered success", err)
	}
	if got := fakeAttemptCount(t, stateDir); got != 2 {
		t.Fatalf("fake go invocation count = %d, want one retry", got)
	}
	if output := stdout.String(); !strings.Contains(output, "attempt 1") || !strings.Contains(output, "attempt 2") || !strings.Contains(output, failedTest) {
		t.Fatalf("recovery diagnostic = %q, want both attempts and recovered test", output)
	}

	firstArgs := fakeInvocationArgs(t, stateDir, 1)
	if got := argumentAfter(firstArgs, "-run"); got != requiredRunPattern(requiredTestNames()) {
		t.Fatalf("first -run pattern = %q, want full required-test pattern", got)
	}
	retryArgs := fakeInvocationArgs(t, stateDir, 2)
	if got := argumentAfter(retryArgs, "-run"); got != exactTestRunPattern(failedTest) {
		t.Fatalf("retry -run pattern = %q, want exact selected test", got)
	}
	for _, requiredTest := range requiredTestNames() {
		if requiredTest != failedTest && strings.Contains(argumentAfter(retryArgs, "-run"), requiredTest) {
			t.Fatalf("retry -run pattern selected unexpected test %q", requiredTest)
		}
	}
	for _, requiredArg := range []string{"-race", "-tags=nomicrophone", "-count=1", "-timeout", "-json", examplePackage} {
		if !containsArgument(retryArgs, requiredArg) {
			t.Errorf("retry arguments = %v, missing %q", retryArgs, requiredArg)
		}
	}
}

func TestExecuteDoesNotRetryIneligibleFailure(t *testing.T) {
	failedTest := requiredTestNames()[0]
	fakeGo, stateDir := newFakeGo(t, []fakeAttempt{{output: oneFailureOutput(t, failedTest, "assertion failed"), status: 1}})
	var stdout, stderr strings.Builder
	err := execute(t.Context(), gateConfig(t, fakeGo, watchdogSignature), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "failed required tests") {
		t.Fatalf("execute() error = %v, want terminal required-test failure", err)
	}
	if got := fakeAttemptCount(t, stateDir); got != 1 {
		t.Fatalf("fake go invocation count = %d, want no retry", got)
	}
}

func TestExecuteReportsBothAttemptsWhenWatchdogRetryFails(t *testing.T) {
	failedTest := requiredTestNames()[0]
	fakeGo, stateDir := newFakeGo(t, []fakeAttempt{
		{output: oneWatchdogFailureOutput(t, failedTest), status: 1},
		{output: oneFailureOutput(t, failedTest, "second assertion failed"), status: 1},
	})
	var stdout, stderr strings.Builder
	err := execute(t.Context(), gateConfig(t, fakeGo, watchdogSignature), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "retry failed") || !strings.Contains(err.Error(), "first attempt:") || !strings.Contains(err.Error(), "retry attempt:") || !strings.Contains(err.Error(), failedTest) {
		t.Fatalf("execute() error = %v, want both-attempt retry diagnostic", err)
	}
	if got := fakeAttemptCount(t, stateDir); got != 2 {
		t.Fatalf("fake go invocation count = %d, want exactly one retry", got)
	}
}

func TestRetryVerificationRejectsUnexpectedRequiredTest(t *testing.T) {
	selected := requiredTestNames()[0]
	unexpected := requiredTestNames()[1]
	input := eventJSON(t, "run", selected, "") +
		eventJSON(t, "pass", selected, "") +
		eventJSON(t, "run", unexpected, "") +
		eventJSON(t, "pass", unexpected, "")
	report, err := parseEvents(testGateName, requiredTestNames(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseEvents() error = %v", err)
	}
	if err := report.retryVerificationError(selected); err == nil || !strings.Contains(err.Error(), "unexpected required test") {
		t.Fatalf("retryVerificationError() = %v, want unexpected-test diagnostic", err)
	}
}

const fakeGoEnvironment = "RACEGATE_FAKE_GO"

func TestMain(m *testing.M) {
	if os.Getenv(fakeGoEnvironment) == "1" {
		runFakeGoProcess()
		return
	}
	os.Exit(m.Run())
}

type fakeAttempt struct {
	output string
	status int
}

func newFakeGo(t *testing.T, attempts []fakeAttempt) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv(fakeGoEnvironment, "1")
	t.Setenv("RACEGATE_FAKE_ROOT", stateDir)
	for index, attempt := range attempts {
		index++
		if err := os.WriteFile(filepath.Join(stateDir, fmt.Sprintf("output-%d", index)), []byte(attempt.output), 0600); err != nil {
			t.Fatalf("write fake output %d: %v", index, err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, fmt.Sprintf("status-%d", index)), []byte(strconv.Itoa(attempt.status)), 0600); err != nil {
			t.Fatalf("write fake status %d: %v", index, err)
		}
	}
	fakeGo, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	return fakeGo, stateDir
}

func runFakeGoProcess() {
	stateDir := os.Getenv("RACEGATE_FAKE_ROOT")
	if stateDir == "" {
		fmt.Fprintln(os.Stderr, "fake go missing RACEGATE_FAKE_ROOT")
		os.Exit(2)
	}
	countFile := filepath.Join(stateDir, "count")
	count := 0
	if data, err := os.ReadFile(countFile); err == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "fake go invalid invocation count: %v\n", parseErr)
			os.Exit(2)
		}
		count = parsed
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "fake go read invocation count: %v\n", err)
		os.Exit(2)
	}
	count++
	if err := os.WriteFile(countFile, []byte(strconv.Itoa(count)), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "fake go write invocation count: %v\n", err)
		os.Exit(2)
	}
	args := strings.Join(os.Args[1:], "\n") + "\n"
	if err := os.WriteFile(filepath.Join(stateDir, fmt.Sprintf("args-%d", count)), []byte(args), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "fake go write invocation args: %v\n", err)
		os.Exit(2)
	}
	output, err := os.ReadFile(filepath.Join(stateDir, fmt.Sprintf("output-%d", count)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake go read output %d: %v\n", count, err)
		os.Exit(2)
	}
	statusData, err := os.ReadFile(filepath.Join(stateDir, fmt.Sprintf("status-%d", count)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake go read status %d: %v\n", count, err)
		os.Exit(2)
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(statusData)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake go parse status %d: %v\n", count, err)
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(output); err != nil {
		fmt.Fprintf(os.Stderr, "fake go write output: %v\n", err)
		os.Exit(2)
	}
	os.Exit(status)
}

func fakeAttemptCount(t *testing.T, stateDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "count"))
	if err != nil {
		t.Fatalf("read fake invocation count: %v", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse fake invocation count %q: %v", data, err)
	}
	return count
}

func fakeInvocationArgs(t *testing.T, stateDir string, attempt int) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, fmt.Sprintf("args-%d", attempt)))
	if err != nil {
		t.Fatalf("read fake invocation args %d: %v", attempt, err)
	}
	return strings.Fields(string(data))
}

func argumentAfter(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func allRequiredPassOutput(t *testing.T) string {
	t.Helper()
	var output strings.Builder
	for _, testName := range requiredTestNames() {
		output.WriteString(eventJSON(t, "run", testName, ""))
		output.WriteString(eventJSON(t, "pass", testName, ""))
	}
	return output.String()
}

func oneWatchdogFailureOutput(t *testing.T, failedTest string) string {
	t.Helper()
	return oneFailureOutput(t, failedTest, watchdogSignature)
}

func oneFailureOutput(t *testing.T, failedTest, diagnostic string) string {
	t.Helper()
	var output strings.Builder
	for _, testName := range requiredTestNames() {
		output.WriteString(eventJSON(t, "run", testName, ""))
		if testName == failedTest {
			output.WriteString(eventJSON(t, "output", testName, diagnostic+"\n"))
			output.WriteString(eventJSON(t, "fail", testName, ""))
			continue
		}
		output.WriteString(eventJSON(t, "pass", testName, ""))
	}
	return output.String()
}

func retryPassOutput(t *testing.T, testName string) string {
	t.Helper()
	return eventJSON(t, "run", testName, "") + eventJSON(t, "pass", testName, "")
}

func eventJSON(t *testing.T, action, testName, output string) string {
	t.Helper()
	data, err := json.Marshal(testEvent{Action: action, Package: "example/pkg", Test: testName, Output: output})
	if err != nil {
		t.Fatalf("marshal test event: %v", err)
	}
	return string(data) + "\n"
}

func TestExecuteWithoutRetryOutputDoesNotRetryWatchdog(t *testing.T) {
	failedTest := requiredTestNames()[0]
	fakeGo, stateDir := newFakeGo(t, []fakeAttempt{{output: oneWatchdogFailureOutput(t, failedTest), status: 1}})
	var stdout, stderr strings.Builder
	err := execute(t.Context(), gateConfig(t, fakeGo, ""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), testGateName+" command failed") || !strings.Contains(err.Error(), failedTest) {
		t.Fatalf("execute() error = %v, want terminal failure naming the gate and test", err)
	}
	if got := fakeAttemptCount(t, stateDir); got != 1 {
		t.Fatalf("fake go invocation count = %d, want no retry without -retry-on-output", got)
	}
}

func TestPrintRunPatternPrintsRequiredTestsWithoutRunningGo(t *testing.T) {
	var stdout, stderr strings.Builder
	args := []string{"-print-run-pattern", "-go", "/nonexistent/go", "-required", " TestAlphaConcurrent,TestBetaConcurrent,,TestGammaConcurrent,TestAlphaConcurrent "}
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run -print-run-pattern: %v (stderr %q)", err, stderr.String())
	}
	want := "^(TestAlphaConcurrent|TestBetaConcurrent|TestGammaConcurrent)$"
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("printed pattern %q, want %q", got, want)
	}
}

func TestRunRejectsIncompleteConfiguration(t *testing.T) {
	for name, args := range map[string][]string{
		"no required tests": {"-module-dir", ".", "-package", "./x"},
		"no module":         {"-required", "TestA", "-package", "./x"},
		"no package":        {"-required", "TestA", "-module-dir", "."},
		"zero timeout":      {"-required", "TestA", "-module-dir", ".", "-package", "./x", "-timeout", "0s"},
		"positional":        {"-required", "TestA", "-module-dir", ".", "-package", "./x", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := run(t.Context(), args, &stdout, &stderr); err == nil {
				t.Fatalf("run(%v) = nil, want configuration error", args)
			}
		})
	}
}
