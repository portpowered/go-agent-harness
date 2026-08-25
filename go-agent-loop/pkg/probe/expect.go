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

	ObservedTick    LogicalTime
	HasObservedTick bool
	TerminalReason  string
	FrameCount      int

	// BufferDisposition records what happened to buffered input audio at
	// session end: committed, discarded, or empty when uncommitted.
	BufferDisposition string
}

type Observation = ObservationSnapshot

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
		ExpectBufferDisposition:
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
