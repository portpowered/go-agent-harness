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

// StuckTerminalReason is the reserved terminal_reason sentinel marking a
// scenario whose session never reached a terminal state. A result line
// carrying it is stuck evidence: the fleet gate counts it as a failure even
// when the producer marked the line itself as passing, because a session that
// never terminated cannot count as green.
const StuckTerminalReason = "stuck"

// Errors reported by EvaluateFleetGate. Use errors.Is to distinguish them.
var (
	// ErrNoFleetArtifacts is returned when no artifact was supplied at all.
	// An empty fleet must not collapse into a passing verdict.
	ErrNoFleetArtifacts = errors.New("fleet gate requires at least one run artifact")
	// ErrEmptyFleetSource is returned when a supplied artifact decodes to zero
	// scenario results. A silent source is reported instead of dropped.
	ErrEmptyFleetSource = errors.New("run artifact contains no scenario results")
)

// FleetArtifact is one named run-artifact input. Name is the stable source
// label carried into the verdict (a file path, or "-" for standard input);
// Reader yields the raw JSONL content of one probe run.
type FleetArtifact struct {
	Name   string
	Reader io.Reader
}

// FleetGateError is a typed aggregation error naming the offending source and,
// for line-level problems, the 1-based line number within that source. Line is
// zero for artifact-level conditions such as an empty source.
type FleetGateError struct {
	Source string
	Line   int
	Err    error
}

func (e *FleetGateError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("fleet gate %s:%d: %v", e.Source, e.Line, e.Err)
	}
	return fmt.Sprintf("fleet gate %s: %v", e.Source, e.Err)
}

func (e *FleetGateError) Unwrap() error { return e.Err }

// FleetSourceSummary is the per-source breakdown entry of a verdict.
type FleetSourceSummary struct {
	Source string    `json:"source"`
	Total  int       `json:"total"`
	Passed int       `json:"passed"`
	Failed int       `json:"failed"`
	Stuck  int       `json:"stuck"`
	Status RunStatus `json:"status"`
}

// FleetGateVerdict is the single deterministic pass/fail verdict over every
// scenario in every source. Sources are sorted by name and Failing is sorted
// and de-duplicated, so identical inputs marshal to byte-identical JSON.
type FleetGateVerdict struct {
	Status  RunStatus            `json:"status"`
	Total   int                  `json:"total"`
	Passed  int                  `json:"passed"`
	Failed  int                  `json:"failed"`
	Stuck   int                  `json:"stuck"`
	Sources []FleetSourceSummary `json:"sources"`
	// Failing lists every failing or stuck scenario as "<source>:<scenario>".
	Failing []string `json:"failing,omitempty"`
}

// EvaluateFleetGate turns one or more run artifacts into a single fleet-wide
// verdict. Each artifact holds the JSONL output of one probe run: scenario
// result lines plus an optional trailing summary line, which is derived data
// and skipped. The verdict status is pass only when every scenario result in
// every source passed without stuck evidence; otherwise it is fail with the
// failing scenarios listed per source.
//
// Malformed input lines surface as a *FleetGateError naming the source and
// line; an artifact without any scenario result surfaces as a *FleetGateError
// wrapping ErrEmptyFleetSource; zero artifacts yields ErrNoFleetArtifacts.
func EvaluateFleetGate(artifacts []FleetArtifact) (FleetGateVerdict, error) {
	if len(artifacts) == 0 {
		return FleetGateVerdict{}, ErrNoFleetArtifacts
	}
	type tally struct {
		total, passed, failed, stuck int
		failing                      map[string]bool
	}
	bySource := map[string]*tally{}
	order := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		results, err := parseFleetArtifact(artifact)
		if err != nil {
			return FleetGateVerdict{}, err
		}
		entry, seen := bySource[artifact.Name]
		if !seen {
			entry = &tally{failing: map[string]bool{}}
			bySource[artifact.Name] = entry
			order = append(order, artifact.Name)
		}
		for _, result := range results {
			entry.total++
			switch {
			case result.TerminalReason == StuckTerminalReason:
				entry.stuck++
				entry.failing[result.Name] = true
			case result.Pass:
				entry.passed++
			default:
				entry.failed++
				entry.failing[result.Name] = true
			}
		}
	}

	sort.Strings(order)
	verdict := FleetGateVerdict{Sources: make([]FleetSourceSummary, 0, len(order))}
	failing := map[string]bool{}
	for _, name := range order {
		entry := bySource[name]
		if entry.total == 0 {
			return FleetGateVerdict{}, &FleetGateError{Source: name, Err: ErrEmptyFleetSource}
		}
		status := StatusPass
		if entry.failed > 0 || entry.stuck > 0 {
			status = StatusFail
		}
		verdict.Sources = append(verdict.Sources, FleetSourceSummary{
			Source: name,
			Total:  entry.total,
			Passed: entry.passed,
			Failed: entry.failed,
			Stuck:  entry.stuck,
			Status: status,
		})
		verdict.Total += entry.total
		verdict.Passed += entry.passed
		verdict.Failed += entry.failed
		verdict.Stuck += entry.stuck
		for scenario := range entry.failing {
			failing[name+":"+scenario] = true
		}
	}
	verdict.Failing = make([]string, 0, len(failing))
	for qualified := range failing {
		verdict.Failing = append(verdict.Failing, qualified)
	}
	sort.Strings(verdict.Failing)
	verdict.Status = StatusPass
	if verdict.Failed > 0 || verdict.Stuck > 0 {
		verdict.Status = StatusFail
	}
	return verdict, nil
}

// parseFleetArtifact decodes one artifact's JSONL stream into its scenario
// results in file order. Blank lines are formatting, not evidence; a line that
// decodes as a run summary is derived data and skipped; any other line that is
// not a recognizable scenario result (non-empty name) is a malformed-input
// error carrying the 1-based line number.
func parseFleetArtifact(artifact FleetArtifact) ([]ScenarioResult, error) {
	if strings.TrimSpace(artifact.Name) == "" {
		return nil, &FleetGateError{Err: errors.New("run artifact requires a source name")}
	}
	if artifact.Reader == nil {
		return nil, &FleetGateError{Source: artifact.Name, Err: errors.New("run artifact requires a reader")}
	}
	var results []ScenarioResult
	scanner := bufio.NewScanner(artifact.Reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var summary RunSummary
		if json.Unmarshal(line, &summary) == nil && summary.Status != "" {
			continue
		}
		var result ScenarioResult
		if err := json.Unmarshal(line, &result); err != nil || strings.TrimSpace(result.Name) == "" {
			reason := "not a scenario result or run summary"
			if err != nil {
				reason = err.Error()
			}
			return nil, &FleetGateError{
				Source: artifact.Name,
				Line:   lineNumber,
				Err:    fmt.Errorf("malformed result line (%s)", reason),
			}
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		return nil, &FleetGateError{Source: artifact.Name, Err: fmt.Errorf("read result lines: %w", err)}
	}
	return results, nil
}
