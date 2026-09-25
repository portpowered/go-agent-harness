package scenariov2

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/bundle"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// DefaultDeadline bounds one scenario execution when Runner.Deadline is unset.
const DefaultDeadline = 30 * time.Second

// Divergence is the stable, redacted explanation of the first failed
// expectation.
type Divergence = objective.Divergence

// EvidenceSummary locates the finalized evidence bundle of one run.
type EvidenceSummary = bundle.Summary

// Result is intentionally compatible with the legacy result vocabulary while
// retaining the stable v2 scenario identity and the paths to the
// independently finalized evidence bundle.
type Result struct {
	ID                 string                        `json:"id"`
	Name               string                        `json:"name"`
	SchemaVersion      string                        `json:"schema_version"`
	Pass               bool                          `json:"pass"`
	Stuck              bool                          `json:"stuck,omitempty"`
	StuckReason        string                        `json:"stuck_reason,omitempty"`
	StepCount          int                           `json:"step_count"`
	Steps              []probe.ScenarioV2Step        `json:"steps"`
	Expectations       []probe.ScenarioV2Expectation `json:"expectations"`
	ExpectationResults []ExpectationResult           `json:"expectation_results"`
	Ticks              probe.LogicalTime             `json:"ticks"`
	Frames             int                           `json:"frames"`
	TerminalReason     string                        `json:"terminal_reason,omitempty"`
	TerminalProvenance string                        `json:"terminal_provenance,omitempty"`
	OutputState        string                        `json:"output_state,omitempty"`
	BrowserExecutor    BrowserExecutorMode           `json:"browser_executor"`
	Error              string                        `json:"error,omitempty"`
	ErrorCode          string                        `json:"error_code,omitempty"`
	Divergence         *Divergence                   `json:"divergence,omitempty"`
	InputDropCount     uint64                        `json:"input_drop_count"`
	OutputDropCount    uint64                        `json:"output_drop_count"`
	ObjectiveEvidence  probe.ObjectiveEvidence       `json:"objective_evidence"`
	Evidence           *EvidenceSummary              `json:"evidence,omitempty"`
}

// ExpectationResult is the live verdict for one declared expectation.
type ExpectationResult struct {
	Index    int                             `json:"index"`
	Type     probe.ScenarioV2ExpectationType `json:"type"`
	Passed   bool                            `json:"passed"`
	Expected string                          `json:"expected,omitempty"`
	Actual   string                          `json:"actual,omitempty"`
	Error    string                          `json:"error,omitempty"`
}

// ProviderAnalyzer replays a provider capture fixture for the provider
// session steps of a scenario.
type ProviderAnalyzer func(
	ctx context.Context,
	replay runtimeReplay.Service,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
) (runtimeReplay.CaptureProbeObservation, probe.ObservationSnapshot, error)

// Runner executes v2 scenarios against injected replay services.
type Runner struct {
	Replay   runtimeReplay.Service
	Analyze  ProviderAnalyzer
	Deadline time.Duration
}

// Run executes entries in order, finalizing each run's evidence under a
// fresh directory of recordingRoot, and hands every result to emit before
// the next scenario starts. It returns the aggregate summary; an emit error
// stops the run.
func (r Runner) Run(ctx context.Context, entries []Selection, recordingRoot string, emit func(Result) error, options ...BrowserExecutorOption) (probe.RunSummary, error) {
	summary := probe.RunSummary{Total: len(entries)}
	for index, entry := range entries {
		result := r.Execute(ctx, entry, RecordingDirectory(recordingRoot, index, entry), options...)
		if err := emit(result); err != nil {
			return summary, err
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
	summary.Status = probe.StatusFail
	if summary.Failed == 0 && summary.Total > 0 {
		summary.Status = probe.StatusPass
	}
	return summary, nil
}

// Execute runs one scenario and, when recordingDirectory is set, finalizes
// and verifies its evidence bundle. Failures are reported in the result.
func (r Runner) Execute(parent context.Context, entry Selection, recordingDirectory string, options ...BrowserExecutorOption) Result {
	result, ready := initialResult(entry, options...)
	if !ready {
		return result
	}
	ctx, cancel := context.WithTimeout(parent, r.deadline())
	defer cancel()
	e, err := newExecutor(ctx, entry.Scenario, r, options...)
	if err == nil {
		err = e.execute(ctx)
	}
	if err != nil {
		return failedResult(ctx, e, result, err)
	}
	result = e.populateResult(ctx, result)
	if recordingDirectory == "" {
		return result
	}
	return e.attachEvidence(ctx, result, recordingDirectory)
}

func (r Runner) deadline() time.Duration {
	if r.Deadline <= 0 {
		return DefaultDeadline
	}
	return r.Deadline
}

// initialResult seeds the result from the selection; ready is false when the
// result is already final because options or the document were invalid.
func initialResult(entry Selection, options ...BrowserExecutorOption) (Result, bool) {
	resolved, optionsErr := resolveBrowserExecutorOptions(options...)
	result := Result{
		ID:              entry.Scenario.ID,
		Name:            entry.Scenario.Name,
		SchemaVersion:   entry.Scenario.SchemaVersion,
		StepCount:       len(entry.Scenario.Steps),
		Steps:           entry.Scenario.Steps,
		Expectations:    entry.Scenario.Expectations,
		Pass:            false,
		BrowserExecutor: BrowserExecutorHermetic,
	}
	if optionsErr != nil {
		result.Error = optionsErr.Error()
		result.ErrorCode = ErrorCode(optionsErr)
		return result, false
	}
	result.BrowserExecutor = resolved.Mode
	if result.Name == "" {
		result.Name = result.ID
	}
	if entry.Err != nil {
		result.ID = entry.Selection
		result.Name = entry.Selection
		result.SchemaVersion = probe.ScenarioV2Version
		result.Error = entry.Err.Error()
		return result, false
	}
	return result, true
}

func failedResult(ctx context.Context, e *executor, result Result, err error) Result {
	result.Error = err.Error()
	result.ErrorCode = ErrorCode(err)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Stuck = true
		result.StuckReason = "v2 scenario execution exceeded the deadguard deadline"
	}
	if e != nil {
		e.cleanup(ctx)
		result = e.populateResult(ctx, result)
	}
	return result
}

func (e *executor) attachEvidence(ctx context.Context, result Result, destination string) Result {
	finalized, err := e.finalizeEvidence(ctx, destination)
	if err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
		result.Pass = false
		return result
	}
	result.Evidence = &finalized.Summary
	result.ObjectiveEvidence = finalized.Objective
	if result.Divergence == nil && e.objectiveDivergence != nil {
		result.Divergence = e.objectiveDivergence
	}
	if finalized.Objective.Verified {
		return result
	}
	result.Pass = false
	if result.Error == "" {
		if e.objectiveDivergence != nil {
			result.Error = e.objectiveDivergence.Error()
		} else {
			result.Error = "objective evidence did not verify the declared browser objectives"
		}
	}
	return result
}

func (e *executor) finalizeEvidence(ctx context.Context, destination string) (bundle.Output, error) {
	if e == nil {
		return bundle.Output{}, errors.New("v2 executor is nil")
	}
	pageState, _ := e.currentPageState()
	finalized, err := bundle.Finalize(context.WithoutCancel(ctx), bundle.Input{
		Destination:    destination,
		Scenario:       e.scenario,
		Replay:         e.replayService,
		ProviderPath:   e.providerPath,
		ProviderReport: e.providerReport,
		PageState:      pageState,
		BrowserEvents:  e.eventOutput.Bytes(),
		Transport:      e.evidenceTransport(),
		ClockBase:      e.evidenceClockBase(),
	})
	if err != nil {
		return bundle.Output{}, err
	}
	e.objectiveDivergence = finalized.Divergence
	return finalized, nil
}

func (e *executor) persistedBrowserEvidence() *objective.BrowserEvidence {
	if e == nil || len(e.eventOutput.Bytes()) == 0 {
		return nil
	}
	events, err := testkit.ValidateEventStream(e.eventOutput.Bytes())
	if err != nil || len(events) == 0 {
		return nil
	}
	evidence := objective.Index(events)
	return &evidence
}
