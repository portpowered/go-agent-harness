package probe

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	ExpectAudioEnergy        ExpectationKind = "audio-energy"
	ExpectTranscriptContains ExpectationKind = "transcript-contains"
	ExpectToolCalled         ExpectationKind = "tool-called"
	ExpectLatencyWithinTicks ExpectationKind = "latency-within-ticks"
	ExpectTerminalReason     ExpectationKind = "terminal-reason"
	ExpectFrameCount         ExpectationKind = "frame-count"
	// ExpectMetricsReconcile requires the emitted per-direction/per-modality
	// metric matrix to equal the summed observed delta stream exactly.
	ExpectMetricsReconcile ExpectationKind = "metrics-reconcile"

	// ExpectToolResultDelivered asserts that the named tool call's result was
	// observed on the client-to-provider path (the result reached the
	// provider) after a mid-tool-call barge-in.
	ExpectToolResultDelivered ExpectationKind = "tool-result-delivered"
	// ExpectToolResultDiscarded asserts that the named tool call's result was
	// explicitly discarded through an observable discard event rather than
	// vanishing silently after a barge-in.
	ExpectToolResultDiscarded ExpectationKind = "tool-result-discarded"
	// ExpectNoOrphanedToolResult enforces the standing barge-in invariant:
	// every issued tool call must be either delivered to the provider or
	// explicitly discarded; any other outcome is an orphaned tool result and
	// fails the run.
	ExpectNoOrphanedToolResult ExpectationKind = "no-orphaned-tool-result"

	// Buffer disposition values observed for buffered input audio. A session
	// that ends with neither disposition left the buffer uncommitted.
	BufferDispositionCommitted = "committed"
	BufferDispositionDiscarded = "discarded"

	ExpectBufferDisposition ExpectationKind = "buffer-disposition"

	// AudioEnergyThreshold is the PCM16 RMS boundary used by the VAD
	// contract. Audio energy must be strictly greater than this value.
	AudioEnergyThreshold = 300.0
)

var (
	ErrExpectationMismatch = errors.New("probe expectation mismatch")
	ErrInvalidExpectation  = errors.New("invalid probe expectation")
)

// ObservationSnapshot is the value-only evidence consumed by evaluation.
// HasObservedTick distinguishes an observed tick of zero from no tick.
type ObservationSnapshot struct {
	PCM16Samples []int16
	Transcript   string
	ToolCalls    []string

	// ToolResultsDelivered lists tool call IDs whose results were observed on
	// the outbound client-to-provider path.
	ToolResultsDelivered []string
	// ToolResultsDiscarded lists tool call IDs whose results were explicitly
	// discarded through an observable discard event.
	ToolResultsDiscarded []string

	ObservedTick    LogicalTime
	HasObservedTick bool
	TerminalReason  string
	FrameCount      int

	// BufferDisposition records what happened to buffered input audio at
	// session end: committed, discarded, or empty when uncommitted.
	BufferDisposition string
	// Metrics carries one reconciliation pair per direction/modality series.
	Metrics []MetricsSeries
}

type Observation = ObservationSnapshot

// MetricsSeries reconciles one direction-and-modality metric series: the
// ReportedTotal emitted by the session's final metric matrix must equal
// ObservedDeltas, the exact sum of that series' structured stream deltas.
type MetricsSeries struct {
	Direction      string
	Modality       string
	ObservedDeltas int64
	ReportedTotal  int64
}

func (s MetricsSeries) key() string {
	return s.Direction + "/" + s.Modality
}

// ExpectationMismatchError identifies a failed predicate and retains its
// structured expectation, expected value, and actual value.
type ExpectationMismatchError struct {
	Kind        ExpectationKind
	Expectation ExpectedBehavior
	Expected    any
	Actual      any
}

func (e *ExpectationMismatchError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("probe expectation %q mismatch: expected %s, actual %s",
		e.Kind, diagnosticValue(e.Expected), diagnosticValue(e.Actual))
}
func (e *ExpectationMismatchError) Unwrap() error { return ErrExpectationMismatch }

// ExpectationValidationError identifies an unknown or malformed declaration.
type ExpectationValidationError struct {
	Kind        ExpectationKind
	Expectation ExpectedBehavior
	Field       string
	Reason      string
}

func (e *ExpectationValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("invalid probe expectation %q at %s: %s", e.Kind, e.Field, e.Reason)
}
func (e *ExpectationValidationError) Unwrap() error { return ErrInvalidExpectation }
func (e *ExpectationValidationError) Is(target error) bool {
	return target == ErrInvalidExpectation || target == ErrInvalidField
}

type ExpectationResult struct {
	Index       int
	Kind        ExpectationKind
	Expectation ExpectedBehavior
	Passed      bool
	Err         error
}

// Evaluate is pure: it reads only the declaration and snapshot and never
// waits, polls, reads a clock, performs I/O, or mutates either input.
func Evaluate(expectation ExpectedBehavior, observation ObservationSnapshot) error {
	kind, err := validKind(expectation)
	if err != nil {
		return err
	}
	switch kind {
	case ExpectAudioEnergy:
		rms := pcm16RMS(observation.PCM16Samples)
		if rms <= AudioEnergyThreshold {
			return mismatch(expectation, kind, "RMS > 300.0", rms)
		}
	case ExpectTranscriptContains:
		want, err := aliasString(expectation, kind, "substring", expectation.Text, expectation.Value)
		if err != nil {
			return err
		}
		if !strings.Contains(observation.Transcript, want) {
			return mismatch(expectation, kind, want, observation.Transcript)
		}
	case ExpectToolCalled:
		want, err := aliasString(expectation, kind, "tool_name", expectation.ToolName, expectation.Value)
		if err != nil {
			return err
		}
		for _, name := range observation.ToolCalls {
			if name == want {
				return nil
			}
		}
		return mismatch(expectation, kind, want, append([]string(nil), observation.ToolCalls...))
	case ExpectLatencyWithinTicks:
		start, err := startTick(expectation)
		if err != nil {
			return err
		}
		if expectation.Count < 0 {
			return invalid(expectation, kind, "count", "maximum tick delta must not be negative")
		}
		observed, ok := observationTick(observation)
		if !ok {
			return mismatch(expectation, kind,
				fmt.Sprintf("non-negative tick delta <= %d", expectation.Count), "missing observed tick")
		}
		if observed < start {
			return mismatch(expectation, kind,
				fmt.Sprintf("non-negative tick delta <= %d", expectation.Count),
				fmt.Sprintf("observed tick %d precedes start tick %d", observed, start))
		}
		delta := observed - start
		if delta > LogicalTime(expectation.Count) {
			return mismatch(expectation, kind,
				fmt.Sprintf("non-negative tick delta <= %d", expectation.Count), delta)
		}
	case ExpectTerminalReason:
		want, err := aliasString(expectation, kind, "reason", expectation.Value, expectation.Text)
		if err != nil {
			return err
		}
		if observation.TerminalReason != want {
			return mismatch(expectation, kind, want, observation.TerminalReason)
		}
	case ExpectFrameCount:
		if expectation.Count < 0 {
			return invalid(expectation, kind, "count", "expected frame count must not be negative")
		}
		if observation.FrameCount != expectation.Count {
			return mismatch(expectation, kind, expectation.Count, observation.FrameCount)
		}
	case ExpectToolResultDelivered:
		want, err := aliasString(expectation, kind, "tool_call_id", expectation.ToolCallID, expectation.Value)
		if err != nil {
			return err
		}
		if !containsID(observation.ToolResultsDelivered, want) {
			return mismatch(expectation, kind, "delivered tool result for "+want,
				observedIDs(observation.ToolResultsDelivered))
		}
	case ExpectToolResultDiscarded:
		want, err := aliasString(expectation, kind, "tool_call_id", expectation.ToolCallID, expectation.Value)
		if err != nil {
			return err
		}
		if !containsID(observation.ToolResultsDiscarded, want) {
			return mismatch(expectation, kind, "explicit discard event for "+want,
				observedIDs(observation.ToolResultsDiscarded))
		}
	case ExpectNoOrphanedToolResult:
		orphaned := orphanedToolCalls(observation)
		if len(orphaned) != 0 {
			return mismatch(expectation, kind,
				"every tool call delivered or explicitly discarded",
				"orphaned: "+diagnosticValue(orphaned))
		}
	case ExpectBufferDisposition:
		want, err := aliasString(expectation, kind, "value", expectation.Value, expectation.Text)
		if err != nil {
			return err
		}
		switch want {
		case BufferDispositionCommitted, BufferDispositionDiscarded:
		default:
			return invalid(expectation, kind, "value",
				"buffer disposition must be committed or discarded")
		}
		if observedBufferDisposition(observation.BufferDisposition) != want {
			return mismatch(expectation, kind, want,
				observedBufferDisposition(observation.BufferDisposition))
		}
	case ExpectMetricsReconcile:
		return evaluateMetricsReconciliation(expectation, observation.Metrics)
	default:
		return invalid(expectation, kind, "type", "unsupported measurable expectation")
	}
	return nil
}

func EvaluateExpectation(expectation ExpectedBehavior, observation ObservationSnapshot) error {
	return Evaluate(expectation, observation)
}

// EvaluateExpectations retains one ordered result for every declaration.
func EvaluateExpectations(expectations []ExpectedBehavior, observation ObservationSnapshot) []ExpectationResult {
	results := make([]ExpectationResult, len(expectations))
	for index, expectation := range expectations {
		err := Evaluate(expectation, observation)
		results[index] = ExpectationResult{
			Index: index, Kind: declaredKind(expectation), Expectation: expectation,
			Passed: err == nil, Err: err,
		}
	}
	return results
}

func EvaluateScenario(scenario Scenario, observation ObservationSnapshot) []ExpectationResult {
	return EvaluateExpectations(scenario.expectedValues(), observation)
}

func validKind(expectation ExpectedBehavior) (ExpectationKind, error) {
	kind := declaredKind(expectation)
	if expectation.Type != "" && expectation.Kind != "" && expectation.Type != expectation.Kind {
		return kind, invalid(expectation, kind, "type", "type and kind disagree")
	}
	switch kind {
	case ExpectAudioEnergy, ExpectTranscriptContains, ExpectToolCalled,
		ExpectLatencyWithinTicks, ExpectTerminalReason, ExpectFrameCount,
		ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult,
		ExpectBufferDisposition, ExpectMetricsReconcile:
		return kind, nil
	default:
		return kind, invalid(expectation, kind, "type", "unknown measurable expectation")
	}
}
func declaredKind(expectation ExpectedBehavior) ExpectationKind {
	if expectation.Type != "" {
		return expectation.Type
	}
	return expectation.Kind
}
func aliasString(e ExpectedBehavior, kind ExpectationKind, field, first, second string) (string, error) {
	if first != "" && second != "" && first != second {
		return "", invalid(e, kind, field, "value aliases disagree")
	}
	if first == "" {
		first = second
	}
	if first == "" {
		return "", invalid(e, kind, field, "expected value must not be empty")
	}
	return first, nil
}
func startTick(e ExpectedBehavior) (LogicalTime, error) {
	if e.At != 0 && e.Time != 0 && e.At != e.Time {
		return 0, invalid(e, declaredKind(e), "start_tick", "at and time aliases disagree")
	}
	var start LogicalTime
	if e.HasAt || e.At != 0 {
		start = e.At
	} else if e.Time != 0 {
		start = e.Time
	} else {
		return 0, invalid(e, declaredKind(e), "start_tick", "declared start tick is required")
	}
	if start < 0 {
		return 0, invalid(e, declaredKind(e), "start_tick", "declared start tick must not be negative")
	}
	return start, nil
}
func observationTick(o ObservationSnapshot) (LogicalTime, bool) {
	return o.ObservedTick, o.HasObservedTick || o.ObservedTick != 0
}
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// orphanedToolCalls returns the tool call IDs that were neither delivered to
// the provider nor explicitly discarded.
func orphanedToolCalls(observation ObservationSnapshot) []string {
	var orphaned []string
	for _, call := range observation.ToolCalls {
		if containsID(observation.ToolResultsDelivered, call) || containsID(observation.ToolResultsDiscarded, call) {
			continue
		}
		orphaned = append(orphaned, call)
	}
	return orphaned
}

func observedIDs(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func pcm16RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}
func observedBufferDisposition(value string) string {
	if value == "" {
		return "uncommitted"
	}
	return value
}
func mismatch(e ExpectedBehavior, kind ExpectationKind, expected, actual any) error {
	return &ExpectationMismatchError{Kind: kind, Expectation: e, Expected: expected, Actual: actual}
}
func invalid(e ExpectedBehavior, kind ExpectationKind, field, reason string) error {
	return &ExpectationValidationError{Kind: kind, Expectation: e, Field: field, Reason: reason}
}
func diagnosticValue(value any) string {
	switch value := value.(type) {
	case string:
		return fmt.Sprintf("%q", value)
	case float64:
		return fmt.Sprintf("%.6f", value)
	case []string:
		return fmt.Sprintf("%q", value)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// evaluateMetricsReconciliation enforces exact equality between the emitted
// metric matrix and the summed observed delta stream. Every provided series
// must carry identical observed and reported values; the failure names the
// offending series with both values.
func evaluateMetricsReconciliation(expectation ExpectedBehavior, series []MetricsSeries) error {
	if len(series) == 0 {
		return mismatch(expectation, ExpectMetricsReconcile,
			"per-direction/per-modality metric series", "none provided")
	}
	for _, entry := range series {
		if entry.ObservedDeltas != entry.ReportedTotal {
			key := entry.key()
			return mismatch(expectation, ExpectMetricsReconcile,
				fmt.Sprintf("%s observed delta sum %d", key, entry.ObservedDeltas),
				fmt.Sprintf("%s reported total %d", key, entry.ReportedTotal))
		}
	}
	return nil
}
