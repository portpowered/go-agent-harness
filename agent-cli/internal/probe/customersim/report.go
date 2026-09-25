package customersim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

const (
	reportDirectoryPermission = 0o700
	reportFilePermission      = 0o600
	redactedSecret            = "<redacted>"
)

// EncodeReport renders the indented JSON report with every secret redacted.
func EncodeReport(result probe.CustomerSimulationSuiteResult, secrets ...string) ([]byte, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode customer simulation report: %w", err)
	}
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			data = bytes.ReplaceAll(data, []byte(secret), []byte(redactedSecret))
		}
	}
	return append(data, '\n'), nil
}

// WriteReport writes the encoded report to the private report path, or to
// out when no path is configured.
func WriteReport(out io.Writer, reportPath string, data []byte) error {
	if strings.TrimSpace(reportPath) == "" {
		if _, err := out.Write(data); err != nil {
			return fmt.Errorf("write customer simulation report: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), reportDirectoryPermission); err != nil {
		return fmt.Errorf("create customer simulation report directory: %w", err)
	}
	if err := os.WriteFile(reportPath, data, reportFilePermission); err != nil {
		return fmt.Errorf("write customer simulation report %q: %w", reportPath, err)
	}
	return nil
}

// WorkedCount counts runs whose independent validator verdict is WORKED.
func WorkedCount(result probe.CustomerSimulationSuiteResult) int {
	passed := 0
	for _, run := range result.Runs {
		if run.Validator.Pass() {
			passed++
		}
	}
	return passed
}

// ValidateResult is the final fail-closed boundary. The production runner
// already returns an aggregate error, but a command must also reject
// incomplete or contradictory results from any runner implementation before
// it reports success to an operator.
func ValidateResult(result probe.CustomerSimulationSuiteResult, scenarios []probe.CustomerScenario) error {
	var failures []error
	if strings.TrimSpace(result.Root) == "" {
		failures = append(failures, errors.New("customer simulation result has no evidence root"))
	}
	if len(result.Runs) != len(scenarios) {
		failures = append(failures, fmt.Errorf("customer simulation returned %d run results, want %d", len(result.Runs), len(scenarios)))
	}
	expected := make(map[string]probe.CustomerScenario, len(scenarios))
	for _, scenario := range scenarios {
		expected[scenario.ID] = scenario
	}
	seen := make(map[string]struct{}, len(result.Runs))
	for index, run := range result.Runs {
		failures = append(failures, validateRun(index, run, expected, seen)...)
	}
	for _, scenario := range scenarios {
		if _, ok := seen[scenario.ID]; !ok {
			failures = append(failures, fmt.Errorf("selected scenario %q has no result", scenario.ID))
		}
	}
	return errors.Join(failures...)
}

// validateRun checks one run against its selected scenario and records its
// identity in seen.
func validateRun(index int, run probe.CustomerSimulationRunResult, expected map[string]probe.CustomerScenario, seen map[string]struct{}) []error {
	var failures []error
	label := fmt.Sprintf("customer simulation result %d", index+1)
	if strings.TrimSpace(run.RunID) == "" {
		failures = append(failures, fmt.Errorf("%s has no run ID", label))
	}
	scenario, ok := expected[run.ScenarioID]
	if !ok {
		return append(failures, fmt.Errorf("%s identifies unexpected scenario %q", label, run.ScenarioID))
	}
	if _, duplicate := seen[run.ScenarioID]; duplicate {
		failures = append(failures, fmt.Errorf("scenario %q appears more than once in the result", run.ScenarioID))
	}
	seen[run.ScenarioID] = struct{}{}
	if run.Family != scenario.Family || run.Termination != scenario.Termination {
		failures = append(failures, fmt.Errorf("scenario %q returned contradictory family or termination facts", run.ScenarioID))
	}
	if strings.TrimSpace(run.BundleRoot) == "" || strings.TrimSpace(run.RecordRoot) == "" || strings.TrimSpace(run.WorkspaceRoot) == "" {
		failures = append(failures, fmt.Errorf("scenario %q is incomplete: evidence, record, and workspace roots are required", run.ScenarioID))
	}
	if !run.Mechanical.Pass || !run.Validator.Mechanical.Pass || !run.Validator.Pass() {
		status := string(run.Validator.Status)
		if status == "" {
			status = "missing"
		}
		failures = append(failures, fmt.Errorf("scenario %q did not produce an accepted WORKED verdict (status %s)", run.ScenarioID, status))
	}
	return failures
}
