package probe

import (
	"fmt"
	"strings"
	"testing"
)

// v3cCorpusLookup accepts exactly the synthetic utterance corpora the v3c
// scenarios reference, mirroring the replay corpus lookup's role in the CLI.
type v3cCorpusLookup struct{}

func (v3cCorpusLookup) Has(id string) bool { return strings.HasPrefix(id, "v3c-utterance-") }

func registeredS2SV3CScenario(t *testing.T, id string) Scenario {
	t.Helper()
	for _, scenario := range Scenarios() {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %q is not registered", id)
	return Scenario{}
}

// Both lane invariants pass on the clean observation the committed positive
// fixture produces.
func v3cCleanObservation() ObservationSnapshot {
	return ObservationSnapshot{
		UserTurnsCommitted:      7,
		AssistantTurnsDelivered: 4,
		ResponsesCreated:        7,
		ResponsesCancelled:      3,
		ResponseCancels:         3,
		TerminalReason:          "synthetic",
	}
}

func TestS2SV3CCancelOnceAndReconciliationPassOnCleanObservation(t *testing.T) {
	scenario := registeredS2SV3CScenario(t, ScenarioIDS2SV3CBargeInRepeated)
	for _, result := range EvaluateScenario(scenario, v3cCleanObservation()) {
		if !result.Passed {
			t.Fatalf("expectation %s must pass on clean observation: %v", result.Kind, result.Err)
		}
	}
}

// Duplication, loss, post-cancel leakage, and double-cancelling each fail the
// matching assertion with expected-vs-actual detail.
func TestS2SV3CReconciliationFailsOnDuplicationLossAndDoubleCancel(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*ObservationSnapshot)
		failingKind ExpectationKind
		detail      string
	}{
		{
			name: "duplicated delivered assistant turn",
			mutate: func(observation *ObservationSnapshot) {
				observation.AssistantTurnsDelivered++
				observation.ResponsesCreated++
			},
			failingKind: ExpectMessageCountsReconcile,
			detail:      "assistant_delivered",
		},
		{
			name: "dropped committed user turn",
			mutate: func(observation *ObservationSnapshot) {
				observation.UserTurnsCommitted--
			},
			failingKind: ExpectMessageCountsReconcile,
			detail:      "user_turns",
		},
		{
			name: "post-cancel delta from cancelled turn",
			mutate: func(observation *ObservationSnapshot) {
				observation.PostCancelDeltas++
			},
			failingKind: ExpectMessageCountsReconcile,
			detail:      "post_cancel_deltas",
		},
		{
			name: "double-cancelled response",
			mutate: func(observation *ObservationSnapshot) {
				observation.ResponseCancels++
				observation.SpuriousCancels++
			},
			failingKind: ExpectBargeInCancelOnce,
			detail:      "stray or duplicate cancels",
		},
		{
			name: "wrong interruption total",
			mutate: func(observation *ObservationSnapshot) {
				observation.ResponseCancels--
				observation.ResponsesCancelled--
			},
			failingKind: ExpectBargeInCancelOnce,
			detail:      "barge-in-cancel-once",
		},
	}
	for _, testCase := range cases {
		observation := v3cCleanObservation()
		testCase.mutate(&observation)
		results := EvaluateScenario(registeredS2SV3CScenario(t, ScenarioIDS2SV3CBargeInRepeated), observation)
		var failures []string
		for index := range results {
			if !results[index].Passed {
				failures = append(failures, fmt.Sprintf("%s: %v", results[index].Kind, results[index].Err))
			}
		}
		if len(failures) == 0 {
			t.Fatalf("%s: mutation must fail at least one expectation", testCase.name)
		}
		matched := false
		for index := range results {
			if results[index].Passed {
				continue
			}
			if results[index].Kind == testCase.failingKind && strings.Contains(results[index].Err.Error(), testCase.detail) {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("%s: failure must hit %s naming %q, got %q", testCase.name, testCase.failingKind, testCase.detail, failures)
		}
	}
}

// Cancel-exactly-once also fails when a response is left streaming at session
// end — the lost-completion symptom of an unbounded barge-in.
func TestS2SV3CCancelOnceFailsOnResponseLeftInFlight(t *testing.T) {
	scenario := registeredS2SV3CScenario(t, ScenarioIDS2SV3CBargeInRepeated)
	observation := v3cCleanObservation()
	observation.InFlightAtEnd = true
	results := EvaluateScenario(scenario, observation)
	for _, result := range results {
		if result.Kind == ExpectBargeInCancelOnce && result.Passed {
			t.Fatal("a response left streaming at session end must fail cancel-exactly-once")
		}
	}
}
