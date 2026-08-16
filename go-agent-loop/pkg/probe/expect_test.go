package probe

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEachMeasurableExpectationPassesAndFails(t *testing.T) {
	tests := []struct {
		name string
		e    ExpectedBehavior
		pass ObservationSnapshot
		bad  ObservationSnapshot
	}{
		{"audio-energy", expect(ExpectAudioEnergy, "", 0), ObservationSnapshot{PCM16Samples: []int16{1000, -1000}}, ObservationSnapshot{PCM16Samples: []int16{0, 0}}},
		{"transcript-contains", expect(ExpectTranscriptContains, "ready", 0), ObservationSnapshot{Transcript: "agent ready"}, ObservationSnapshot{Transcript: "agent waiting"}},
		{"tool-called", expect(ExpectToolCalled, "calendar", 0), ObservationSnapshot{ToolCalls: []string{"weather", "calendar"}}, ObservationSnapshot{ToolCalls: []string{"weather"}}},
		{"latency-within-ticks", expect(ExpectLatencyWithinTicks, "", 3), ObservationSnapshot{ObservedTick: 13, HasObservedTick: true}, ObservationSnapshot{ObservedTick: 14, HasObservedTick: true}},
		{"terminal-reason", expect(ExpectTerminalReason, "complete", 0), ObservationSnapshot{TerminalReason: "complete"}, ObservationSnapshot{TerminalReason: "cancelled"}},
		{"frame-count", expect(ExpectFrameCount, "", 4), ObservationSnapshot{FrameCount: 4}, ObservationSnapshot{FrameCount: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Evaluate(test.e, test.pass); err != nil {
				t.Fatalf("satisfying observation failed: %v", err)
			}
			if err := Evaluate(test.e, test.bad); err == nil {
				t.Fatal("violating observation unexpectedly passed")
			}
		})
	}
}

func TestAudioEnergyIsStrictlyAboveThresholdAndRejectsEmptyAudio(t *testing.T) {
	e := expect(ExpectAudioEnergy, "", 0)
	for _, test := range []struct {
		name string
		data []int16
		pass bool
	}{{"silence", []int16{0, 0}, false}, {"threshold", []int16{300, -300}, false}, {"utterance", []int16{301, -301}, true}} {
		t.Run(test.name, func(t *testing.T) {
			err := Evaluate(e, ObservationSnapshot{PCM16Samples: test.data})
			if (err == nil) != test.pass {
				t.Fatalf("pass=%v, err=%v", test.pass, err)
			}
		})
	}
	if err := Evaluate(e, ObservationSnapshot{}); err == nil {
		t.Fatal("empty PCM16 audio unexpectedly passed")
	}
}

func TestS4MismatchDiagnosticsCoverEveryKind(t *testing.T) {
	tests := []struct {
		name string
		e    ExpectedBehavior
		o    ObservationSnapshot
		want any
		got  any
	}{
		{"audio-energy", expect(ExpectAudioEnergy, "", 0), ObservationSnapshot{PCM16Samples: []int16{0, 0}}, "RMS > 300.0", float64(0)},
		{"transcript-contains", expect(ExpectTranscriptContains, "ready", 0), ObservationSnapshot{Transcript: "waiting"}, "ready", "waiting"},
		{"tool-called", expect(ExpectToolCalled, "calendar", 0), ObservationSnapshot{ToolCalls: []string{"weather"}}, "calendar", []string{"weather"}},
		{"latency-within-ticks", expect(ExpectLatencyWithinTicks, "", 3), ObservationSnapshot{ObservedTick: 14, HasObservedTick: true}, "non-negative tick delta <= 3", LogicalTime(4)},
		{"terminal-reason", expect(ExpectTerminalReason, "complete", 0), ObservationSnapshot{TerminalReason: "cancelled"}, "complete", "cancelled"},
		{"frame-count", expect(ExpectFrameCount, "", 3), ObservationSnapshot{FrameCount: 2}, 3, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Evaluate(test.e, test.o)
			var mismatch *ExpectationMismatchError
			if !errors.As(err, &mismatch) || !errors.Is(err, ErrExpectationMismatch) {
				t.Fatalf("mismatch identity/type: %v", err)
			}
			if mismatch.Kind != ExpectationKind(test.name) || !reflect.DeepEqual(mismatch.Expectation, test.e) ||
				!reflect.DeepEqual(mismatch.Expected, test.want) || !reflect.DeepEqual(mismatch.Actual, test.got) {
				t.Fatalf("structured mismatch: %#v", mismatch)
			}
			message := err.Error()
			for _, value := range []string{test.name, diagnosticValue(test.want), diagnosticValue(test.got)} {
				if !strings.Contains(message, value) {
					t.Fatalf("message %q does not contain %q", message, value)
				}
			}
		})
	}
}

func TestScenarioResultsPreserveOrderAndRejectNoOpEvidence(t *testing.T) {
	expectations := []ExpectedBehavior{
		expect(ExpectAudioEnergy, "", 0),
		expect(ExpectTranscriptContains, "ready", 0),
		expect(ExpectToolCalled, "calendar", 0),
		expect(ExpectLatencyWithinTicks, "", 3),
		expect(ExpectTerminalReason, "complete", 0),
		expect(ExpectFrameCount, "", 4),
	}
	scenario := Scenario{ID: "full-vocabulary", Expectations: expectations}
	passing := ObservationSnapshot{
		PCM16Samples: []int16{1000, -1000}, Transcript: "ready",
		ToolCalls: []string{"calendar"}, ObservedTick: 13, HasObservedTick: true,
		TerminalReason: "complete", FrameCount: 4,
	}
	for index, result := range EvaluateScenario(scenario, passing) {
		if result.Index != index || result.Kind != expectations[index].Type || !result.Passed || result.Err != nil {
			t.Fatalf("ordered passing result %d: %#v", index, result)
		}
	}
	results := EvaluateScenario(scenario, ObservationSnapshot{})
	if len(results) != len(expectations) {
		t.Fatalf("result count: got %d, want %d", len(results), len(expectations))
	}
	for index, result := range results {
		var mismatch *ExpectationMismatchError
		if result.Index != index || result.Kind != expectations[index].Type || result.Passed || !errors.As(result.Err, &mismatch) {
			t.Fatalf("ordered no-op result %d: %#v", index, result)
		}
	}
}

func TestMalformedExpectationsHaveTypedValidationIdentity(t *testing.T) {
	tests := []ExpectedBehavior{
		{Type: ExpectationKind("unknown")},
		expect(ExpectTranscriptContains, "", 0),
		expect(ExpectToolCalled, "", 0),
		expect(ExpectLatencyWithinTicks, "", -1),
		expect(ExpectFrameCount, "", -1),
		{Type: ExpectLatencyWithinTicks, Kind: ExpectAudioEnergy},
	}
	for _, e := range tests {
		err := Evaluate(e, ObservationSnapshot{})
		var validation *ExpectationValidationError
		if !errors.As(err, &validation) || !errors.Is(err, ErrInvalidExpectation) || !errors.Is(err, ErrInvalidField) {
			t.Fatalf("validation identity/type for %#v: %v", e, err)
		}
	}
}

func TestEvaluationDoesNotMutateInputs(t *testing.T) {
	e := expect(ExpectToolCalled, "calendar", 0)
	o := ObservationSnapshot{PCM16Samples: []int16{1000}, ToolCalls: []string{"weather"}}
	wantE, wantO := e, ObservationSnapshot{
		PCM16Samples: append([]int16(nil), o.PCM16Samples...),
		ToolCalls:    append([]string(nil), o.ToolCalls...),
	}
	_ = Evaluate(e, o)
	_ = EvaluateExpectations([]ExpectedBehavior{e}, o)
	if !reflect.DeepEqual(e, wantE) || !reflect.DeepEqual(o, wantO) {
		t.Fatalf("evaluation mutated inputs: got %#v / %#v", e, o)
	}
}

func expect(kind ExpectationKind, value string, count int) ExpectedBehavior {
	e := ExpectedBehavior{Type: kind, Kind: kind}
	switch kind {
	case ExpectTranscriptContains:
		e.Text = value
	case ExpectToolCalled:
		e.ToolName = value
	case ExpectTerminalReason:
		e.Value = value
	case ExpectLatencyWithinTicks:
		e.At, e.HasAt, e.Count = 10, true, count
	case ExpectFrameCount:
		e.Count = count
	}
	return e
}
