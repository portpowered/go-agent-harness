package probe

import (
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSequencerUsesSharedBarrierAndCanonicalEvidence(t *testing.T) {
	result, crossed, err := runConcurrentProbe()
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Events) != 4 || len(result.Expectations) != 1 || !result.Expectations[0].Passed {
		t.Fatalf("result did not pass: %+v", result)
	}
	if crossed != 6 {
		t.Fatalf("barrier crossings = %d, want driver/client/agent across two ticks", crossed)
	}
	wantEvents := []SequencedEvent{
		{Tick: 1, Participant: ScenarioDriver, Source: "driver", Identity: "stimulus"},
		{Tick: 1, Participant: Client, Source: "client", Identity: "client-observed"},
		{Tick: 1, Participant: Agent, Source: "agent", Identity: "agent-observed"},
		{Tick: 2, Participant: Agent, Source: "agent", Identity: "response"},
	}
	if !reflect.DeepEqual(result.Events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", result.Events, wantEvents)
	}
	if result.Expectations[0] != (ExpectationOutcome{ID: "response-at-two", Tick: 2, Passed: true}) {
		t.Fatalf("expectations = %#v, want exact passing outcome", result.Expectations)
	}
	for repetition := 0; repetition < 100; repetition++ {
		repeated, _, err := runConcurrentProbe()
		if err != nil {
			t.Fatalf("repetition %d Run() error = %v", repetition, err)
		}
		if !reflect.DeepEqual(repeated, result) {
			t.Fatalf("repetition %d result changed:\n got %#v\nwant %#v", repetition, repeated, result)
		}
	}
}

func TestSequencerRejectsResponseBeforeStimulus(t *testing.T) {
	plan := SequencePlan{
		Steps:        []SequenceStep{{Tick: 1}, {Tick: 2}},
		Expectations: []TickExpectation{{ID: "response-at-two", At: 2, EventID: "response"}},
		Causality:    []CausalExpectation{{Stimulus: "stimulus", Response: "response"}},
	}
	sequencer, err := NewSequencer(plan)
	if err != nil {
		t.Fatalf("NewSequencer() error = %v", err)
	}
	result, err := sequencer.Run(ParticipantHandlers{
		Driver: func(ctx *TickContext, _ []SequenceStep) error {
			if ctx.Tick() == 1 {
				ctx.Emit("response")
			} else {
				ctx.Emit("stimulus")
			}
			return nil
		},
	})
	if !errors.Is(err, ErrOrderingViolation) {
		t.Fatalf("Run() error = %v, want ordering violation", err)
	}
	var violation *OrderingViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("Run() error %T, want OrderingViolationError", err)
	}
	if violation.Response.Tick != 1 || violation.Stimulus.Tick != 2 {
		t.Fatalf("violation = %#v, want response tick 1 and stimulus tick 2", violation)
	}
	if !strings.Contains(err.Error(), "logical tick 1") || !strings.Contains(err.Error(), "logical tick 2") {
		t.Fatalf("diagnostic = %q, want both logical ticks", err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %#v, want inverted evidence retained", result.Events)
	}
}

func TestSequencerRejectsWallDurationExpectation(t *testing.T) {
	plan := SequencePlan{
		Steps: []SequenceStep{{Tick: 1}},
		Expectations: []TickExpectation{{
			ID: "wall-latency", At: 1, EventID: "response", WallDuration: time.Second,
		}},
	}
	_, err := NewSequencer(plan)
	if !errors.Is(err, ErrWallDurationExpectation) {
		t.Fatalf("NewSequencer() error = %v, want wall-duration rejection", err)
	}
	var durationError *WallDurationExpectationError
	if !errors.As(err, &durationError) {
		t.Fatalf("NewSequencer() error %T, want WallDurationExpectationError", err)
	}
	if durationError.ID != "wall-latency" {
		t.Fatalf("duration error id = %q, want wall-latency", durationError.ID)
	}
	if !strings.Contains(durationError.Error(), "logical tick") {
		t.Fatalf("duration diagnostic = %q, want logical-tick guidance", durationError.Error())
	}
}

func runConcurrentProbe() (SequenceResult, int32, error) {
	plan := SequencePlan{
		Steps:        []SequenceStep{{Tick: 1}, {Tick: 2}},
		Expectations: []TickExpectation{{ID: "response-at-two", At: 2, EventID: "response"}},
		Causality:    []CausalExpectation{{Stimulus: "stimulus", Response: "response"}},
	}
	sequencer, err := NewSequencer(plan)
	if err != nil {
		return SequenceResult{}, 0, err
	}
	stimulus := make(chan struct{}, 2)
	var crossed atomic.Int32
	handlers := ParticipantHandlers{
		Driver: func(ctx *TickContext, _ []SequenceStep) error {
			crossed.Add(1)
			if ctx.Tick() == 1 {
				ctx.Emit("stimulus")
				stimulus <- struct{}{}
				stimulus <- struct{}{}
			}
			return nil
		},
		Client: func(ctx *TickContext, _ []SequenceStep) error {
			crossed.Add(1)
			if ctx.Tick() == 1 {
				<-stimulus
				ctx.Emit("client-observed")
			}
			return nil
		},
		Agent: func(ctx *TickContext, _ []SequenceStep) error {
			crossed.Add(1)
			if ctx.Tick() == 1 {
				<-stimulus
				ctx.Emit("agent-observed")
			} else {
				ctx.Emit("response")
			}
			return nil
		},
	}
	result, err := sequencer.Run(handlers)
	return result, crossed.Load(), err
}
