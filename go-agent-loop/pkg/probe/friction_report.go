package probe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// StuckTerminalReason is the terminal-reason marker emitted by the probe
// dead-session guard. Stuck results remain failures in the health totals while
// also being counted separately in FrictionReport.Stuck.
const StuckTerminalReason = "stuck"

var (
	// ErrMalformedReport identifies a JSONL line that is neither a ScenarioResult
	// nor a complete RunSummary.
	ErrMalformedReport = errors.New("malformed probe report")
	// ErrMissingReportReader identifies an input without a readable stream.
	ErrMissingReportReader = errors.New("probe report input requires a reader")
)

// FrictionReportInput names one JSONL run artifact. A report can combine any
// number of inputs; the name is used only for error context and need not be a
// filesystem path.
type FrictionReportInput struct {
	Name   string
	Reader io.Reader
}

// ReportArtifact is an expressive alias for callers that already refer to
// probe JSONL streams as artifacts.
type ReportArtifact = FrictionReportInput

// FrictionReport is the deterministic aggregate of the scenario result lines
// in one or more probe run artifacts. All count collections are sorted by
// their key before the report is returned, so json.Marshal produces stable
// bytes for the same inputs.
type FrictionReport struct {
	Total           int                   `json:"total"`
	Passed          int                   `json:"passed"`
	Failed          int                   `json:"failed"`
	Stuck           int                   `json:"stuck"`
	Scenarios       []ScenarioRollup      `json:"scenarios"`
	TerminalReasons []TerminalReasonCount `json:"terminal_reasons"`
	ErrorClasses    []ErrorClassCount     `json:"error_classes"`
}

// ScenarioRollup contains the health totals for one scenario name across all
// supplied artifacts. Stuck is a subset of Failed.
type ScenarioRollup struct {
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
	Stuck  int    `json:"stuck"`
}

// TerminalReasonCount records how often a non-empty terminal reason occurred.
type TerminalReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// ErrorClassCount records how often a result exposed an error class. Classes
// come from an error:<class> terminal reason when present, otherwise from a
// stable classification of the result's error text.
type ErrorClassCount struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// FrictionReportError identifies the source and 1-based line of malformed
// JSONL input. Line is zero for an input-level reader error.
type FrictionReportError struct {
	Source string
	Line   int
	Err    error
}

func (e *FrictionReportError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Line > 0 {
		return fmt.Sprintf("probe report %s:%d: %v", e.Source, e.Line, e.Err)
	}
	return fmt.Sprintf("probe report %s: %v", e.Source, e.Err)
}

func (e *FrictionReportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AggregateFrictionReport reads one or more JSONL run artifacts and returns a
// deterministic aggregate. Scenario result lines are counted; RunSummary
// lines are derived from those results and are ignored. No inputs, or inputs
// containing only whitespace, produce a valid empty report.
func AggregateFrictionReport(inputs ...FrictionReportInput) (FrictionReport, error) {
	results := make([]ScenarioResult, 0)
	for index, input := range inputs {
		parsed, err := readFrictionReportInput(input, index)
		if err != nil {
			return FrictionReport{}, err
		}
		results = append(results, parsed...)
	}
	return AggregateScenarioResults(results), nil
}

// AggregateScenarioResults aggregates already-decoded scenario results. It is
// useful to callers whose artifact reader is managed elsewhere and shares the
// same deterministic ordering as AggregateFrictionReport.
func AggregateScenarioResults(results []ScenarioResult) FrictionReport {
	type scenarioCounts struct {
		total, passed, failed, stuck int
	}

	report := FrictionReport{
		Scenarios:       make([]ScenarioRollup, 0),
		TerminalReasons: make([]TerminalReasonCount, 0),
		ErrorClasses:    make([]ErrorClassCount, 0),
	}
	byScenario := make(map[string]*scenarioCounts)
	terminalReasons := make(map[string]int)
	errorClasses := make(map[string]int)

	for _, result := range results {
		report.Total++
		counts := byScenario[result.Name]
		if counts == nil {
			counts = &scenarioCounts{}
			byScenario[result.Name] = counts
		}
		counts.total++

		stuck := result.TerminalReason == StuckTerminalReason
		if stuck {
			report.Stuck++
			counts.stuck++
		}
		if result.Pass && !stuck {
			report.Passed++
			counts.passed++
		} else {
			report.Failed++
			counts.failed++
		}

		if result.TerminalReason != "" {
			terminalReasons[result.TerminalReason]++
		}
		if class := resultErrorClass(result); class != "" {
			errorClasses[class]++
		}
	}

	scenarioNames := make([]string, 0, len(byScenario))
	for name := range byScenario {
		scenarioNames = append(scenarioNames, name)
	}
	sort.Strings(scenarioNames)
	for _, name := range scenarioNames {
		counts := byScenario[name]
		report.Scenarios = append(report.Scenarios, ScenarioRollup{
			Name:   name,
			Total:  counts.total,
			Passed: counts.passed,
			Failed: counts.failed,
			Stuck:  counts.stuck,
		})
	}

	for _, reason := range sortedCountKeys(terminalReasons) {
		report.TerminalReasons = append(report.TerminalReasons, TerminalReasonCount{
			Reason: reason,
			Count:  terminalReasons[reason],
		})
	}
	for _, class := range sortedCountKeys(errorClasses) {
		report.ErrorClasses = append(report.ErrorClasses, ErrorClassCount{
			Class: class,
			Count: errorClasses[class],
		})
	}
	return report
}

// Aggregate is a short alias for aggregating decoded ScenarioResult values.
func Aggregate(results []ScenarioResult) FrictionReport {
	return AggregateScenarioResults(results)
}

func readFrictionReportInput(input FrictionReportInput, index int) ([]ScenarioResult, error) {
	source := strings.TrimSpace(input.Name)
	if source == "" {
		source = fmt.Sprintf("input-%d", index+1)
	}
	if input.Reader == nil {
		return nil, &FrictionReportError{Source: source, Err: ErrMissingReportReader}
	}

	results := make([]ScenarioResult, 0)
	scanner := bufio.NewScanner(input.Reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if isCompleteRunSummary(line) {
			continue
		}

		var result ScenarioResult
		if err := json.Unmarshal(line, &result); err != nil {
			return nil, malformedReportLine(source, lineNumber, err)
		}
		if err := validateScenarioResultLine(line, result); err != nil {
			return nil, malformedReportLine(source, lineNumber, err)
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		return nil, &FrictionReportError{Source: source, Err: fmt.Errorf("read JSONL: %w", err)}
	}
	return results, nil
}

func isCompleteRunSummary(line []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil {
		return false
	}
	if _, hasName := fields["name"]; hasName {
		return false
	}
	if _, hasStatus := fields["status"]; !hasStatus {
		return false
	}
	var summary RunSummary
	if json.Unmarshal(line, &summary) != nil || (summary.Status != StatusPass && summary.Status != StatusFail) {
		return false
	}
	for _, field := range []string{"total", "passed", "failed"} {
		if _, ok := fields[field]; !ok {
			return false
		}
	}
	return true
}

func validateScenarioResultLine(line []byte, result ScenarioResult) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return err
	}
	if strings.TrimSpace(result.Name) == "" {
		return errors.New("scenario result requires a non-empty name")
	}
	if _, ok := fields["pass"]; !ok {
		return errors.New("scenario result requires a pass field")
	}
	var pass bool
	if err := json.Unmarshal(fields["pass"], &pass); err != nil {
		return fmt.Errorf("scenario result pass field: %w", err)
	}
	return nil
}

func malformedReportLine(source string, line int, err error) error {
	return &FrictionReportError{
		Source: source,
		Line:   line,
		Err:    fmt.Errorf("%w: %v", ErrMalformedReport, err),
	}
}

func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resultErrorClass(result ScenarioResult) string {
	if reason := strings.TrimSpace(result.TerminalReason); strings.HasPrefix(reason, "error:") {
		return strings.TrimSpace(strings.TrimPrefix(reason, "error:"))
	}
	if strings.TrimSpace(result.Error) == "" {
		return ""
	}
	return classifyProbeError(result.Error)
}

func classifyProbeError(message string) string {
	text := strings.ToLower(message)
	switch {
	case strings.Contains(text, "authentication"), strings.Contains(text, "unauthorized"),
		strings.Contains(text, "invalid api key"), strings.Contains(text, "permission denied"):
		return "authentication"
	case strings.Contains(text, "rate limit"), strings.Contains(text, "rate_limit"),
		strings.Contains(text, "too many requests"), strings.Contains(text, "throttl"):
		return "rate_limited"
	case strings.Contains(text, "unsupported"):
		return "unsupported_request"
	case strings.Contains(text, "invalid request"), strings.Contains(text, "malformed request"),
		strings.Contains(text, "bad request"):
		return "invalid_request"
	case strings.Contains(text, "replay mismatch"), strings.Contains(text, "replay divergence"),
		strings.Contains(text, "divergence"):
		return "replay_mismatch"
	case strings.Contains(text, "replay incomplete"), strings.Contains(text, "incomplete replay"):
		return "replay_incomplete"
	case strings.Contains(text, "cancel"):
		return "cancellation"
	case strings.Contains(text, "transport"), strings.Contains(text, "network"),
		strings.Contains(text, "connection"):
		return "transport"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"):
		return "timeout"
	case strings.Contains(text, "panic"):
		return "panic"
	default:
		return "unknown"
	}
}
