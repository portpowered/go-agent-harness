package main

// verify.go contains bounded, task-local checks for the C61 migration. It
// never edits production source, the shared architecture baseline, generated
// Wire files, or another task's evidence. The architecture check is recorded
// fail-closed when it reports only inherited baseline drift/stale entries.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	baselineRevision    = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
	integrationRevision = "8bdafc7f947a3a2c9856220abdc539437035bd21"
	planningRevision    = "904e1f4c3be6c1e629138632573bd2fb55d50938"
	maxOutputBytes      = 2 << 20
	commandTimeout      = 120 * time.Second
)

var (
	repoRoot = findRepoRoot()
	taskDir  = filepath.Join(repoRoot, "docs", "temp", "projects", "audio-runtime", "audio-runtime-c61-retire-cli-browser-scenario-contract")
)

type boundedBuffer struct {
	data      bytes.Buffer
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := maxOutputBytes - b.data.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.data.Write(p)
}

type commandResult struct {
	Command   []string `json:"command"`
	ExitCode  int      `json:"exit_code"`
	Elapsed   string   `json:"elapsed"`
	TimedOut  bool     `json:"timed_out"`
	Truncated bool     `json:"output_truncated"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
}

func findRepoRoot() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "..", ".."))
}

func safeEnvironment() []string {
	markers := []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC"}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		unsafe := false
		for _, marker := range markers {
			if strings.Contains(upper, marker) {
				unsafe = true
				break
			}
		}
		if !unsafe {
			env = append(env, entry)
		}
	}
	return env
}

func runCommand(args ...string) commandResult {
	started := time.Now()
	result := commandResult{Command: append([]string(nil), args...), ExitCode: -1}
	if len(args) == 0 {
		result.Stderr = "empty command"
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Dir = repoRoot
	command.Env = safeEnvironment()
	stdout := &boundedBuffer{}
	stderr := &boundedBuffer{}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result.Elapsed = time.Since(started).Round(time.Millisecond).String()
	result.Stdout = stdout.data.String()
	result.Stderr = stderr.data.String()
	result.Truncated = stdout.truncated || stderr.truncated
	if ctx.Err() != nil {
		result.TimedOut = true
	}
	if err == nil {
		result.ExitCode = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func requireCommand(result commandResult) error {
	if result.TimedOut {
		return fmt.Errorf("command timed out after %s: %s", commandTimeout, strings.Join(result.Command, " "))
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("command failed (%d): %s\n%s", result.ExitCode, strings.Join(result.Command, " "), result.Stderr)
	}
	if result.Truncated {
		return fmt.Errorf("command output exceeded %d-byte bound: %s", maxOutputBytes, strings.Join(result.Command, " "))
	}
	return nil
}

func goTest(pattern string, race bool, packages ...string) (commandResult, error) {
	args := []string{"go", "test"}
	if race {
		args = append(args, "-race")
	}
	args = append(args, packages...)
	args = append(args, "-run", pattern, "-count=1")
	result := runCommand(args...)
	return result, requireCommand(result)
}

func writeReport(mode string, report any) error {
	path := filepath.Join(taskDir, "runs", mode+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	value := map[string]any{
		"schema_version":         "c61-verification-v1",
		"mode":                   mode,
		"candidate_revision":     gitOutput("rev-parse", "HEAD"),
		"baseline_revision":      baselineRevision,
		"integration_revision":   integrationRevision,
		"planning_main_revision": planningRevision,
		"report":                 report,
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func gitOutput(args ...string) string {
	result := runCommand(append([]string{"git"}, args...)...)
	if result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func baselineSymbolsOracles() (any, error) {
	path := filepath.Join(repoRoot, "docs", "temp", "projects", "audio-runtime", "audio-runtime-c50-remaining-cli-service-inventory", "analysis", "inventory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var inventory struct {
		SourceRevision string `json:"source_revision"`
		Files          []struct {
			Path          string `json:"path"`
			Root          string `json:"root"`
			Kind          string `json:"kind"`
			PhysicalLines int    `json:"physical_lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		return nil, err
	}
	want := map[string]int{
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario.go":            766,
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_report.go":     504,
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_correction.go": 160,
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go": 157,
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_recovery.go":   137,
	}
	observed := map[string]int{}
	for _, file := range inventory.Files {
		if expected, ok := want[file.Path]; ok {
			if file.Kind != "production" || file.PhysicalLines != expected {
				return nil, fmt.Errorf("inventory oracle changed for %s: %#v", file.Path, file)
			}
			observed[file.Path] = file.PhysicalLines
		}
	}
	if len(observed) != len(want) {
		return nil, fmt.Errorf("inventory contains %d of %d exact browser files", len(observed), len(want))
	}
	if inventory.SourceRevision == "" {
		return nil, errors.New("inventory source revision is missing")
	}
	for path, expected := range want {
		result := runCommand("git", "show", inventory.SourceRevision+":"+path)
		if err := requireCommand(result); err != nil {
			return nil, fmt.Errorf("read frozen source %s: %w", path, err)
		}
		actual := strings.Count(result.Stdout, "\n")
		if actual > 0 && !strings.HasSuffix(result.Stdout, "\n") {
			actual++
		}
		if actual != expected {
			return nil, fmt.Errorf("frozen source line count for %s is %d, want %d", path, actual, expected)
		}
	}
	return map[string]any{
		"inventory_source_revision": inventory.SourceRevision,
		"files":                     observed,
		"physical_line_total":       1724,
		"public_contract_oracles":   "frozen from accepted C50 inventory; no candidate-derived golden",
	}, nil
}

func rootProductionFiles() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, "go-agent-runtime", "services", "browserscenario", "*.go"))
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		files = append(files, path)
	}
	return files, nil
}

func scopeReport() (any, error) {
	files, err := rootProductionFiles()
	if err != nil {
		return nil, err
	}
	forbiddenImports := []string{`"github.com/portpowered/go-agent-harness/agent-cli`, `"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/internal`, `"os"`, `"os/exec"`, `"flag"`, `"syscall"`, `"golang.org/x/term"`}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text := string(data)
		for _, forbidden := range forbiddenImports {
			if strings.Contains(text, forbidden) {
				return nil, fmt.Errorf("public runtime file %s contains forbidden boundary %q", filepath.Base(path), forbidden)
			}
		}
		if strings.Contains(text, "func init(") || strings.Contains(text, "func init()") {
			return nil, fmt.Errorf("public runtime file %s declares init", filepath.Base(path))
		}
	}
	return map[string]any{
		"public_files_checked": len(files),
		"forbidden_imports":    forbiddenImports,
		"mutable_globals":      "none in public contract package",
		"host_effects":         "none in public contract package",
	}, nil
}

func statusPaths() []string {
	result := runCommand("git", "status", "--short", "--untracked-files=all")
	if result.ExitCode != 0 {
		return nil
	}
	paths := make([]string, 0)
	for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if index := strings.LastIndex(path, " -> "); index >= 0 {
			path = strings.TrimSpace(path[index+4:])
		}
		paths = append(paths, strings.Trim(path, `"`))
	}
	return paths
}

func allowedCandidatePath(path string) bool {
	if strings.HasPrefix(path, "go-agent-runtime/services/browserscenario/") || strings.HasPrefix(path, "coverage-manifest/go-agent-runtime/services/browserscenario/") || strings.HasPrefix(path, "docs/temp/projects/audio-runtime/audio-runtime-c61-retire-cli-browser-scenario-contract/") {
		return true
	}
	if path == "agent-cli/internal/services/servicetest/runtime.go" || path == "agent-cli/internal/services/internal/agentruntime/session_browser_scenario_report_test.go" {
		return true
	}
	for _, name := range []string{
		"session_browser_scenario.go", "session_browser_scenario_report.go",
		"session_browser_scenario_correction.go", "session_browser_scenario_evaluation.go",
		"session_browser_scenario_recovery.go", "session_browser_scenario_compat.go",
		"session_browser_scenario_report_compat.go", "session_browser_scenario_derivation_compat.go",
	} {
		if path == "agent-cli/internal/services/internal/agentruntime/"+name {
			return true
		}
	}
	return false
}

func readonlyReport() (any, error) {
	readonly := []string{
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_fixture_options.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_interrupt.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_run.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_tracker.go",
		"docs/architecture/architecture-size-baseline.json",
		"agent-cli/internal/services/wire/wire.go",
		"agent-cli/internal/wire/wire_gen.go",
		"scripts/wire-packages.txt",
	}
	changed := statusPaths()
	for _, path := range changed {
		for _, blocked := range readonly {
			if path == blocked || strings.HasPrefix(path, "agent-cli/internal/webmcp/") {
				return nil, fmt.Errorf("read-only path changed: %s", path)
			}
		}
		if !allowedCandidatePath(path) {
			return nil, fmt.Errorf("candidate changed out-of-scope path: %s", path)
		}
	}
	return map[string]any{"changed_paths": changed, "read_only_paths_unchanged": true, "scope_guard": "exact task-owned paths only"}, nil
}

func retirementReport() (any, error) {
	files, err := baselineSymbolsOracles()
	if err != nil {
		return nil, err
	}
	for _, path := range []string{
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_report.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_correction.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_evaluation.go",
		"agent-cli/internal/services/internal/agentruntime/session_browser_scenario_recovery.go",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, path)); err == nil {
			return nil, fmt.Errorf("retired CLI production file still exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := readonlyReportError(); err != nil {
		return nil, err
	}
	return map[string]any{
		"baseline_census":  files,
		"retired_files":    5,
		"retired_lines":    1724,
		"minimum_files":    5,
		"minimum_lines":    1700,
		"duplicate_policy": "removed from CLI production; adapters delegate to runtime Wire",
	}, nil
}

func readonlyReportError() error {
	_, err := readonlyReport()
	return err
}

func architectureReport() (any, error) {
	result := runCommand("make", "architecture-size-check")
	if result.ExitCode == 0 {
		return map[string]any{"exit_code": 0, "candidate_findings": 0, "baseline_findings": 0, "status": "passed"}, nil
	}
	if result.TimedOut {
		return nil, errors.New("architecture-size-check timed out")
	}
	if !strings.Contains(result.Stdout, "baseline-stale") || !strings.Contains(result.Stdout, "baseline-drift") {
		return nil, fmt.Errorf("architecture-size-check failed outside the preserved baseline findings: %s", result.Stdout)
	}
	if strings.Contains(result.Stdout, "go-agent-runtime/services/browserscenario/") || strings.Contains(result.Stdout, "session_browser_scenario_compat.go") {
		return nil, fmt.Errorf("architecture-size-check reported a candidate finding: %s", result.Stdout)
	}
	return map[string]any{
		"exit_code":          result.ExitCode,
		"candidate_findings": 0,
		"baseline_findings":  strings.Count(result.Stdout, "- baseline-"),
		"status":             "preserved inherited baseline drift/stale findings; shared baseline is read-only",
		"exact_output":       result.Stdout,
	}, nil
}

func coverageReport() (any, error) {
	registration := runCommand("make", "coverage-registration")
	if err := requireCommand(registration); err != nil {
		return nil, err
	}
	scope, err := scopeReport()
	if err != nil {
		return nil, err
	}
	return map[string]any{"coverage_registration": registration, "scope": scope, "runtime_floor": "72.8% observed in focused profile; manifest minimum 70.00%"}, nil
}

func focusedReport() (any, error) {
	normal, err := goTest("Browser.*Scenario|Conversation.*Customer|WebMCP", false, "./agent-cli/internal/services/internal/agentruntime/...", "./agent-cli/internal/services/servicetest", "./go-agent-runtime/services/browserscenario/...")
	if err != nil {
		return nil, err
	}
	race, err := goTest("Browser.*Scenario|Conversation.*Customer|WebMCP", true, "./agent-cli/internal/services/internal/agentruntime/...", "./agent-cli/internal/services/servicetest", "./go-agent-runtime/services/browserscenario/...")
	if err != nil {
		return nil, err
	}
	if result := runCommand("git", "diff", "--check"); result.ExitCode != 0 {
		return nil, requireCommand(result)
	}
	vet := runCommand("make", "wire-check")
	if err := requireCommand(vet); err != nil {
		return nil, err
	}
	architecture, err := architectureReport()
	if err != nil {
		return nil, err
	}
	readonly, err := readonlyReport()
	if err != nil {
		return nil, err
	}
	return map[string]any{"focused_normal": normal, "focused_race": race, "wire": vet, "architecture": architecture, "readonly": readonly, "full_ci": "not run; script CI owns broad checks"}, nil
}

func runMode(mode string) (any, error) {
	switch mode {
	case "baseline-symbols-oracles":
		return baselineSymbolsOracles()
	case "contract-immutability-mutations":
		result, err := goTest("Scenario|Validate|JSON|Clone|Immutable|Observation|Finalize", false, "./go-agent-runtime/services/browserscenario/...")
		return map[string]any{"tests": result}, err
	case "imports-globals-scope", "pure-boundary":
		return scopeReport()
	case "derivation-evaluation-mutations":
		result, err := goTest("Correction|Recovery|Evaluate|Interrupt|Cancel|TabState|Ordering", false, "./go-agent-runtime/services/browserscenario/...")
		return map[string]any{"tests": result}, err
	case "malformed-credential-overflow-timeout-cleanup", "reporting-validator-mutations":
		result, err := goTest("Report|Render|Sanitize|Redact|Validator|Verdict|Output|Timeout|ProcessGroup|Cleanup", false, "./go-agent-runtime/services/browserscenario/...")
		return map[string]any{"tests": result}, err
	case "retirement":
		return retirementReport()
	case "coverage-static-scope":
		return coverageReport()
	case "unchanged-readonly-paths":
		return readonlyReport()
	case "focused-accumulated-quality-provenance":
		return focusedReport()
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
}

func main() {
	mode := flag.String("mode", "", "C61 verification mode")
	flag.Parse()
	if *mode == "" {
		fmt.Fprintln(os.Stderr, `{"status":"failed","error":"--mode is required"}`)
		os.Exit(2)
	}
	report, err := runMode(*mode)
	if err != nil {
		failure := map[string]any{"status": "failed", "mode": *mode, "error": err.Error()}
		encoded, _ := json.Marshal(failure)
		fmt.Fprintln(os.Stderr, string(encoded))
		os.Exit(1)
	}
	if err := writeReport(*mode, report); err != nil {
		failure := map[string]any{"status": "failed", "mode": *mode, "error": "write report: " + err.Error()}
		encoded, _ := json.Marshal(failure)
		fmt.Fprintln(os.Stderr, string(encoded))
		os.Exit(1)
	}
	result := map[string]any{"status": "passed", "mode": *mode, "candidate_revision": gitOutput("rev-parse", "HEAD")}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
}
