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
}

type testResult struct {
	passed  int
	skipped bool
	failed  bool
}

type gateFailure struct {
	Missing   []string
	Skipped   []string
	Failed    []string
	Duplicate []string
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

	command := exec.Command(cfg.goBinary,
		"test",
		"-race",
		"-tags=nomicrophone",
		"-count=1",
		"-timeout", cfg.timeout.String(),
		"-json",
		"-run", sessionsRunPattern,
		sessionsPackage,
	)
	command.Dir = moduleDir
	command.Env = childEnvironment(moduleDir)

	var testJSON bytes.Buffer
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	command.Stdout = io.MultiWriter(stdout, &testJSON)
	command.Stderr = stderr

	commandErr := command.Run()
	verificationErr := verifyEvents(bytes.NewReader(testJSON.Bytes()))
	if commandErr != nil {
		if verificationErr != nil {
			return fmt.Errorf("concurrent session race command failed: %v; %w", commandErr, verificationErr)
		}
		return fmt.Errorf("concurrent session race command failed: %w", commandErr)
	}
	return verificationErr
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
			return fmt.Errorf("decode go test JSON event on line %d: %w", lineNumber, err)
		}
		if _, required := requiredTestSet[event.Test]; !required {
			continue
		}
		result := results[event.Test]
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
		return fmt.Errorf("read go test JSON output: %w", err)
	}

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
	if failure.empty() {
		return nil
	}
	return &failure
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
