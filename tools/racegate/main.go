// Command racegate runs a required set of tests of one package under the race
// detector and verifies from the go test -json event stream that every
// required test ran exactly once and passed. A missing, skipped, duplicated,
// or failed required test fails the gate.
//
// With -retry-on-output, a first attempt in which exactly one required test
// failed and its output contains that text (a recognized watchdog) is retried
// once with only that test; any other failure is terminal.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultGoBinary = "go"
	defaultTimeout  = 10 * time.Minute
)

type config struct {
	name          string
	goBinary      string
	moduleDir     string
	pkg           string
	required      []string
	retryOnOutput string
	timeout       time.Duration
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

type requiredTestsError struct {
	name      string
	Missing   []string
	Skipped   []string
	Failed    []string
	Duplicate []string
}

type eventReport struct {
	required []string
	results  map[string]testResult
	failure  requiredTestsError
}

type commandAttempt struct {
	commandErr      error
	report          *eventReport
	verificationErr error
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("racegate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "race gate", "gate name used in diagnostics")
	goBinary := flags.String("go", defaultGoBinary, "Go command to execute")
	moduleDir := flags.String("module-dir", "", "module directory the package is tested from")
	pkg := flags.String("package", "", "package to test, relative to -module-dir")
	required := flags.String("required", "", "comma-separated names of the tests that must each pass exactly once")
	retryOnOutput := flags.String("retry-on-output", "", "retry once a single required test whose failure output contains this text")
	timeout := flags.Duration("timeout", defaultTimeout, "finite go test timeout")
	printRunPattern := flags.Bool("print-run-pattern", false, "print the -run pattern of the required tests and exit (other race runs of the package skip them)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments: %s", *name, strings.Join(flags.Args(), " "))
	}
	cfg := config{name: *name, goBinary: *goBinary, moduleDir: *moduleDir, pkg: *pkg, required: splitRequired(*required), retryOnOutput: *retryOnOutput, timeout: *timeout}
	if len(cfg.required) == 0 {
		return fmt.Errorf("%s requires -required test names", cfg.name)
	}
	if *printRunPattern {
		_, err := fmt.Fprintln(stdout, requiredRunPattern(cfg.required))
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	return execute(ctx, cfg, stdout, stderr)
}

func splitRequired(value string) []string {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func (c config) validate() error {
	switch {
	case strings.TrimSpace(c.goBinary) == "":
		return fmt.Errorf("%s requires a Go command", c.name)
	case strings.TrimSpace(c.moduleDir) == "":
		return fmt.Errorf("%s requires a module directory", c.name)
	case strings.TrimSpace(c.pkg) == "":
		return fmt.Errorf("%s requires a package", c.name)
	case c.timeout <= 0:
		return fmt.Errorf("%s timeout must be finite and positive, got %s", c.name, c.timeout)
	}
	return nil
}

func requiredRunPattern(required []string) string {
	quoted := make([]string, len(required))
	for index, name := range required {
		quoted[index] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func execute(ctx context.Context, cfg config, stdout, stderr io.Writer) error {
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

	first := runAttempt(ctx, cfg, moduleDir, requiredRunPattern(cfg.required), "", stdout, stderr)
	if retryTest, ok := eligibleRetryTest(cfg, first); ok {
		if _, err := fmt.Fprintf(stdout, "%s: attempt 1 failed with the recognized watchdog for %s; starting attempt 2 with only that test\n", cfg.name, retryTest); err != nil {
			return fmt.Errorf("report retry start: %w", err)
		}
		retry := runAttempt(ctx, cfg, moduleDir, exactTestRunPattern(retryTest), retryTest, stdout, stderr)
		if retry.commandErr == nil && retry.verificationErr == nil {
			if _, err := fmt.Fprintf(stdout, "%s: recovered %s; attempt 1 watchdog failure and attempt 2 passed all required checks\n", cfg.name, retryTest); err != nil {
				return fmt.Errorf("report retry recovery: %w", err)
			}
			return nil
		}
		return retryFailure(cfg.name, retryTest, first, retry)
	}
	return firstFailure(cfg.name, first)
}

func runAttempt(ctx context.Context, cfg config, moduleDir, runPattern, retryTest string, stdout, stderr io.Writer) commandAttempt {
	command := exec.CommandContext(ctx, cfg.goBinary, testCommandArgs(cfg.timeout, runPattern, cfg.pkg)...)
	command.Dir = moduleDir
	command.Env = childEnvironment(moduleDir)

	var testJSON bytes.Buffer
	command.Stdout = io.MultiWriter(stdout, &testJSON)
	command.Stderr = stderr

	attempt := commandAttempt{commandErr: command.Run()}
	report, parseErr := parseEvents(cfg.name, cfg.required, bytes.NewReader(testJSON.Bytes()))
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

func testCommandArgs(timeout time.Duration, runPattern, pkg string) []string {
	return []string{
		"test",
		"-race",
		"-tags=nomicrophone",
		"-count=1",
		"-timeout", timeout.String(),
		"-json",
		"-run", runPattern,
		pkg,
	}
}

func exactTestRunPattern(testName string) string {
	return "^" + regexp.QuoteMeta(testName) + "$"
}

func eligibleRetryTest(cfg config, attempt commandAttempt) (string, bool) {
	if cfg.retryOnOutput == "" || attempt.commandErr == nil {
		return "", false
	}
	report, ok := attemptReport(attempt)
	if !ok || len(report.failure.Failed) != 1 || len(report.failure.Missing) != 0 || len(report.failure.Skipped) != 0 || len(report.failure.Duplicate) != 0 {
		return "", false
	}
	testName := report.failure.Failed[0]
	result := report.results[testName]
	if result.passed != 0 || !result.failed || !strings.Contains(result.output, cfg.retryOnOutput) {
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

func firstFailure(name string, attempt commandAttempt) error {
	if attempt.commandErr != nil {
		if attempt.verificationErr != nil {
			return fmt.Errorf("%s command failed: %w; %w", name, attempt.commandErr, attempt.verificationErr)
		}
		return fmt.Errorf("%s command failed: %w", name, attempt.commandErr)
	}
	return attempt.verificationErr
}

func retryFailure(name, testName string, first, retry commandAttempt) error {
	firstErr := firstFailure(name, first)
	retryErr := firstFailure(name, retry)
	if firstErr == nil {
		firstErr = errors.New("first attempt unexpectedly passed")
	}
	if retryErr == nil {
		retryErr = errors.New("retry attempt unexpectedly passed")
	}
	return fmt.Errorf("%s retry failed for %s; first attempt: %w; retry attempt: %w", name, testName, firstErr, retryErr)
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
