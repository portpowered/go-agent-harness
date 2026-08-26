package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// ExecFunc is the injected replay-backed execution seam. Implementations run
// one scenario in isolation against recorded fixtures and must not dial the
// network. It matches the shape of the replay transport's single-scenario
// probe pass in go-llm-gateway/pkg/testing.
type ExecFunc func(ctx context.Context, scenario Scenario) (ObservationSnapshot, error)

// ScenarioExpectationOutcome records the evaluated result of one declared expectation,
// including observed-vs-expected detail when it fails.
type ScenarioExpectationOutcome struct {
	Index    int             `json:"index"`
	Kind     ExpectationKind `json:"kind"`
	Passed   bool            `json:"passed"`
	Expected string          `json:"expected,omitempty"`
	Actual   string          `json:"actual,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// ScenarioResult is one JSONL record emitted per executed scenario.
type ScenarioResult struct {
	Name                        string                       `json:"name"`
	Pass                        bool                         `json:"pass"`
	Stuck                       bool                         `json:"stuck,omitempty"`
	StuckReason                 string                       `json:"stuck_reason,omitempty"`
	ScenarioExpectationOutcomes []ScenarioExpectationOutcome `json:"expectations"`
	Ticks                       LogicalTime                  `json:"ticks"`
	Frames                      int                          `json:"frames"`
	TerminalReason              string                       `json:"terminal_reason,omitempty"`
	Error                       string                       `json:"error,omitempty"`
	// InputDropCount and OutputDropCount report the cumulative buffer-full
	// drops observed on the client-to-provider and provider-to-client paths,
	// so a run can never lose messages without an assertable figure.
	InputDropCount  uint64 `json:"input_drop_count"`
	OutputDropCount uint64 `json:"output_drop_count"`
}

// RunStatus is the overall verdict of a probe run.
type RunStatus string

const (
	StatusPass RunStatus = "pass"
	StatusFail RunStatus = "fail"
)

const stuckObservationReason = "execution completed without observable output evidence or a terminal reason"

// RunSummary is the final machine-readable summary object of a run.
type RunSummary struct {
	Total  int       `json:"total"`
	Passed int       `json:"passed"`
	Failed int       `json:"failed"`
	Stuck  int       `json:"stuck"`
	Status RunStatus `json:"status"`
}

// Runner executes ordered probe scenarios against an injected execution
// function and emits one JSON result line per scenario followed by one summary
// line, all through the caller-supplied writer. It performs no network or
// filesystem I/O of its own.
type Runner struct {
	Exec ExecFunc
	Out  io.Writer
	// CorpusLookups are passed to scenario validation so send_audio steps can
	// resolve corpus IDs against caller-provided lookups (e.g. replay-backed
	// offline probes whose audio lives in the recorded fixture).
	CorpusLookups []CorpusLookup
}

// Run executes each scenario in order and returns the aggregated summary.
// A scenario whose validation or execution fails yields exactly one failed
// result line and the run continues with the remaining scenarios. A panic
// inside the injected execution function is recovered and reported as a
// failure rather than crashing the run.
func (r *Runner) Run(ctx context.Context, scenarios []Scenario) (RunSummary, error) {
	if r.Out == nil {
		return RunSummary{}, fmt.Errorf("probe runner requires an output writer")
	}
	if r.Exec == nil {
		return RunSummary{}, fmt.Errorf("probe runner requires an execution function")
	}
	summary := RunSummary{Total: len(scenarios)}
	for _, scenario := range scenarios {
		result := r.runOne(ctx, scenario)
		line, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return summary, fmt.Errorf("encode result for scenario %q: %w", result.Name, encodeErr)
		}
		if _, writeErr := fmt.Fprintf(r.Out, "%s\n", line); writeErr != nil {
			return summary, fmt.Errorf("write result for scenario %q: %w", result.Name, writeErr)
		}
		if result.Pass {
			summary.Passed++
		} else {
			summary.Failed++
		}
		if result.Stuck {
			summary.Stuck++
		}
	}
	summary.Status = StatusFail
	if summary.Failed == 0 && summary.Total > 0 {
		summary.Status = StatusPass
	}
	line, err := json.Marshal(summary)
	if err != nil {
		return summary, fmt.Errorf("encode run summary: %w", err)
	}
	if _, err := fmt.Fprintf(r.Out, "%s\n", line); err != nil {
		return summary, fmt.Errorf("write run summary: %w", err)
	}
	return summary, nil
}

func (r *Runner) runOne(ctx context.Context, scenario Scenario) ScenarioResult {
	result := ScenarioResult{Name: scenarioName(scenario)}
	failed := func(err error) ScenarioResult {
		result.Pass = false
		result.Error = err.Error()
		return result
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = failed(fmt.Errorf("scenario execution panicked: %v", recovered))
		}
	}()
	if validateErr := scenario.Validate(r.CorpusLookups...); validateErr != nil {
		return failed(validateErr)
	}
	observation, execErr := func() (observation ObservationSnapshot, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("execution panicked: %v", recovered)
			}
		}()
		return r.Exec(ctx, scenario)
	}()
	if execErr != nil {
		return failed(execErr)
	}
	results := EvaluateExpectations(scenario.expectedValues(), observation)
	outcomes := make([]ScenarioExpectationOutcome, 0, len(results))
	pass := true
	for _, evaluation := range results {
		outcome := ScenarioExpectationOutcome{
			Index:  evaluation.Index,
			Kind:   evaluation.Kind,
			Passed: evaluation.Passed,
		}
		if !evaluation.Passed {
			pass = false
			detail := expectationDetail(evaluation.Err)
			outcome.Expected = detail.expected
			outcome.Actual = detail.actual
			outcome.Error = evaluation.Err.Error()
		}
		outcomes = append(outcomes, outcome)
	}
	result.ScenarioExpectationOutcomes = outcomes
	result.Ticks = observation.ObservedTick
	result.Frames = observation.FrameCount
	result.TerminalReason = observation.TerminalReason
	result.InputDropCount = observation.InputDrops
	result.OutputDropCount = observation.OutputDrops
	result.Pass = pass
	if isStuckObservation(observation) {
		result.Pass = false
		result.Stuck = true
		result.StuckReason = stuckObservationReason
	}
	return result
}

func isStuckObservation(observation ObservationSnapshot) bool {
	return len(observation.PCM16Samples) == 0 &&
		observation.Transcript == "" &&
		len(observation.ToolCalls) == 0 &&
		len(observation.ToolResultsDelivered) == 0 &&
		len(observation.ToolResultsDiscarded) == 0 &&
		observation.FrameCount <= 0 &&
		observation.TerminalReason == ""
}

func scenarioName(scenario Scenario) string {
	if scenario.Name != "" {
		return scenario.Name
	}
	return scenario.ID
}

type observedVsExpected struct {
	expected, actual string
}

func expectationDetail(err error) observedVsExpected {
	switch value := err.(type) {
	case *ExpectationMismatchError:
		return observedVsExpected{
			expected: diagnosticValue(value.Expected),
			actual:   diagnosticValue(value.Actual),
		}
	default:
		return observedVsExpected{}
	}
}
