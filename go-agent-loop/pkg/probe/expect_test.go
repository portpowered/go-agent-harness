package probe

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
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
	utterance := corpusPCM16(t, "utt_short_16k.wav")
	silence := corpusPCM16(t, "silence_16k.wav")
	if err := Evaluate(e, ObservationSnapshot{PCM16Samples: utterance}); err != nil {
		t.Fatalf("utterance corpus failed: %v", err)
	}
	err := Evaluate(e, ObservationSnapshot{PCM16Samples: silence})
	var mismatch *ExpectationMismatchError
	if !errors.As(err, &mismatch) || mismatch.Actual != float64(0) {
		t.Fatalf("silence corpus diagnostic: %v", err)
	}
	if err := Evaluate(e, ObservationSnapshot{PCM16Samples: []int16{300}}); err == nil {
		t.Fatal("RMS equal to the threshold unexpectedly passed")
	}
	if err := Evaluate(e, ObservationSnapshot{}); err == nil {
		t.Fatal("empty PCM16 audio unexpectedly passed")
	}
}

func corpusPCM16(t *testing.T, name string) []int16 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "audio", name))
	if err != nil {
		t.Fatalf("read audio corpus %q: %v", name, err)
	}
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		end := offset + 8 + size
		if end > len(data) {
			t.Fatalf("audio corpus %q has truncated chunk", name)
		}
		if string(data[offset:offset+4]) == "data" {
			if size == 0 || size%2 != 0 {
				t.Fatalf("audio corpus %q has invalid PCM16 data size %d", name, size)
			}
			samples := make([]int16, size/2)
			for index := range samples {
				samples[index] = int16(binary.LittleEndian.Uint16(data[offset+8+index*2 : offset+10+index*2]))
			}
			return samples
		}
		offset = end + size%2
	}
	t.Fatalf("audio corpus %q has no data chunk", name)
	return nil
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
		{Type: ExpectLatencyWithinTicks, Kind: ExpectLatencyWithinTicks, At: -1, HasAt: true},
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

func TestLatencyRejectsNegativeStartsAndAvoidsTickOverflow(t *testing.T) {
	const (
		maxLogicalTime LogicalTime = 1<<63 - 1
		minLogicalTime LogicalTime = -1 << 63
	)

	negativeStart := ExpectedBehavior{
		Type: ExpectLatencyWithinTicks, Kind: ExpectLatencyWithinTicks,
		At: -1, HasAt: true, Count: 0,
	}
	err := Evaluate(negativeStart, ObservationSnapshot{ObservedTick: maxLogicalTime, HasObservedTick: true})
	var validation *ExpectationValidationError
	if !errors.As(err, &validation) || validation.Field != "start_tick" {
		t.Fatalf("negative start identity: %v", err)
	}

	latestStart := ExpectedBehavior{
		Type: ExpectLatencyWithinTicks, Kind: ExpectLatencyWithinTicks,
		At: maxLogicalTime, HasAt: true, Count: 0,
	}
	err = Evaluate(latestStart, ObservationSnapshot{ObservedTick: minLogicalTime, HasObservedTick: true})
	var mismatch *ExpectationMismatchError
	if !errors.As(err, &mismatch) || !strings.Contains(mismatch.Error(), "precedes start tick") {
		t.Fatalf("earlier extreme tick identity: %v", err)
	}

	firstTick := ExpectedBehavior{
		Type: ExpectLatencyWithinTicks, Kind: ExpectLatencyWithinTicks,
		At: 1, HasAt: true, Count: 0,
	}
	err = Evaluate(firstTick, ObservationSnapshot{ObservedTick: maxLogicalTime, HasObservedTick: true})
	if !errors.As(err, &mismatch) || mismatch.Actual != maxLogicalTime-1 {
		t.Fatalf("latest extreme tick diagnostic: %v", err)
	}
}

func TestDiagnosticErrorsAndEvaluationWrapper(t *testing.T) {
	if err := EvaluateExpectation(expect(ExpectTranscriptContains, "ready", 0), ObservationSnapshot{Transcript: "ready"}); err != nil {
		t.Fatalf("evaluation wrapper: %v", err)
	}

	err := EvaluateExpectation(ExpectedBehavior{Type: ExpectationKind("unknown")}, ObservationSnapshot{})
	var validation *ExpectationValidationError
	if !errors.As(err, &validation) || validation.Error() == "" || validation.Unwrap() != ErrInvalidExpectation {
		t.Fatalf("validation diagnostic: %v", err)
	}

	var nilValidation *ExpectationValidationError
	if nilValidation.Error() != "<nil>" {
		t.Fatalf("nil validation diagnostic: %q", nilValidation.Error())
	}
	var nilMismatch *ExpectationMismatchError
	if nilMismatch.Error() != "<nil>" {
		t.Fatalf("nil mismatch diagnostic: %q", nilMismatch.Error())
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

func TestEvaluateBufferDisposition(t *testing.T) {
	committed := ExpectedBehavior{Type: ExpectBufferDisposition, Value: BufferDispositionCommitted}
	if err := Evaluate(committed, ObservationSnapshot{BufferDisposition: BufferDispositionCommitted}); err != nil {
		t.Fatalf("committed observation should satisfy committed expectation: %v", err)
	}
	if err := Evaluate(ExpectedBehavior{Type: ExpectBufferDisposition, Value: "uncommitted"}, ObservationSnapshot{}); err == nil {
		t.Fatal("uncommitted is not a declarable disposition")
	}
	err := Evaluate(committed, ObservationSnapshot{})
	var mismatchErr *ExpectationMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("empty observation should mismatch committed expectation, got %v", err)
	}
	if mismatchErr.Actual != "uncommitted" {
		t.Fatalf("mismatch actual = %v, want uncommitted", mismatchErr.Actual)
	}
	discarded := ExpectedBehavior{Type: ExpectBufferDisposition, Value: BufferDispositionDiscarded}
	if err := Evaluate(discarded, ObservationSnapshot{BufferDisposition: BufferDispositionCommitted}); err == nil {
		t.Fatal("committed observation must not satisfy discarded expectation")
	}
	if err := Evaluate(ExpectedBehavior{Type: ExpectBufferDisposition}, ObservationSnapshot{}); err == nil {
		t.Fatal("missing declared value must be invalid")
	}
}
