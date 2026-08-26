package probe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type v3aCorpusLookup map[string]bool

func (lookup v3aCorpusLookup) Has(id string) bool { return lookup[id] }

func registeredV3AScenarios(t *testing.T) []Scenario {
	t.Helper()
	suite := make([]Scenario, 0)
	for _, scenario := range Scenarios() {
		if strings.HasPrefix(scenario.ID, ScenarioIDS2SV3ABargeInBasic+"-") {
			suite = append(suite, scenario)
		}
	}
	if len(suite) != 3 {
		t.Fatalf("v3a barge-in suite size = %d, want 3", len(suite))
	}
	return suite
}

func findV3AScenario(t *testing.T, id string) Scenario {
	t.Helper()
	for _, scenario := range Scenarios() {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %s is not registered", id)
	return Scenario{}
}

func TestS2SV3ABargeInSuiteRegistersAndValidates(t *testing.T) {
	corpus := v3aCorpusLookup{v3aCorpus16k: true, v3aCorpus24k: true}
	for _, scenario := range registeredV3AScenarios(t) {
		if err := scenario.Validate(corpus); err != nil {
			t.Fatalf("scenario %s does not validate: %v", scenario.ID, err)
		}
		last := scenario.Steps[len(scenario.Steps)-1]
		if last.Type != StepClose {
			t.Fatalf("scenario %s must end with close, got %q", scenario.ID, last.Type)
		}
	}

	cancelled := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicCancelled16k)
	if len(cancelled.Steps) != 3 || !stepHasCorpus(cancelled.Steps[1]) || cancelled.Steps[1].CorpusID != v3aCorpus16k {
		t.Fatalf("cancelled-16k must deliver the overlap_16k interrupting audio: %+v", cancelled.Steps)
	}
	if len(cancelled.Expectations) < 2 || cancelled.Expectations[1].Type != ExpectLatencyWithinTicks || cancelled.Expectations[1].HasAt {
		t.Fatalf("cancelled-16k must declare latency separately with a dynamic start tick: %+v", cancelled.Expectations)
	}
	cancelled24 := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicCancelled24k)
	if cancelled24.Steps[1].CorpusID != v3aCorpus24k {
		t.Fatalf("cancelled-24k must deliver the overlap_24k interrupting audio: %+v", cancelled24.Steps)
	}
	if len(cancelled24.Expectations) < 2 || cancelled24.Expectations[1].Type != ExpectLatencyWithinTicks || cancelled24.Expectations[1].HasAt {
		t.Fatalf("cancelled-24k must declare latency separately with a dynamic start tick: %+v", cancelled24.Expectations)
	}
	noInterruption := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicNoInterruption)
	for _, step := range noInterruption.Steps {
		if step.Type == StepSendAudio {
			t.Fatal("no-interruption control must not carry user audio input")
		}
	}
}

func TestS2SV3ACancelledCasesPassThroughRunner(t *testing.T) {
	corpus := v3aCorpusLookup{v3aCorpus16k: true, v3aCorpus24k: true}
	for _, id := range []string{ScenarioIDS2SV3ABargeInBasicCancelled16k, ScenarioIDS2SV3ABargeInBasicCancelled24k} {
		scenario := findV3AScenario(t, id)
		if err := scenario.Validate(corpus); err != nil {
			t.Fatalf("scenario %s validation: %v", id, err)
		}
		runner := &Runner{Out: &strings.Builder{}, CorpusLookups: []CorpusLookup{corpus},
			Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
				return ObservationSnapshot{
					TerminalReason:     "synthetic",
					InterruptTick:      11,
					HasInterruptTick:   true,
					HasResponseCancel:  true,
					ResponseCancelTick: 12,
				}, nil
			}}

		outcome := runV3AScenario(t, runner, scenario)
		if outcome["pass"] != true {
			t.Fatalf("%s must pass when the cancel is observed within bounds: %v", id, outcome)
		}
		kinds := outcomeKinds(t, outcome)
		wantKinds := []ExpectationKind{ExpectResponseCancel, ExpectLatencyWithinTicks, ExpectTerminalReason}
		if len(kinds) != len(wantKinds) {
			t.Fatalf("%s outcome kinds = %v, want %v", id, kinds, wantKinds)
		}
		for index, kind := range wantKinds {
			if kinds[index] != kind {
				t.Fatalf("%s outcome kind[%d] = %s, want %s", id, index, kinds[index], kind)
			}
		}
	}
}

func TestS2SV3ASuppressedCancelFailsAsNegativeControl(t *testing.T) {
	corpus := v3aCorpusLookup{v3aCorpus16k: true, v3aCorpus24k: true}
	for _, id := range []string{ScenarioIDS2SV3ABargeInBasicCancelled16k, ScenarioIDS2SV3ABargeInBasicCancelled24k} {
		scenario := findV3AScenario(t, id)
		if err := scenario.Validate(corpus); err != nil {
			t.Fatalf("scenario %s validation: %v", id, err)
		}
		// Broken cancel path: the fixture is delivered and the session exits
		// cleanly, but the loop never emits RESPONSE.CANCEL.
		runner := &Runner{Out: &strings.Builder{}, CorpusLookups: []CorpusLookup{corpus},
			Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
				return ObservationSnapshot{TerminalReason: "synthetic"}, nil
			}}

		outcome := runV3AScenario(t, runner, scenario)
		if outcome["pass"] != false {
			t.Fatalf("%s must fail when the cancel is suppressed: %v", id, outcome)
		}
		first := firstFailedOutcomeError(t, outcome)
		if !strings.Contains(first, `probe expectation "response-cancel" mismatch`) ||
			!strings.Contains(first, "RESPONSE.CANCEL observed") {
			t.Fatalf("%s failure must name the response-cancel mismatch reason, got: %s", id, first)
		}
	}
}

func TestS2SV3ALatencyBoundRejectsLateCancellation(t *testing.T) {
	scenario := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicCancelled16k)
	corpus := v3aCorpusLookup{v3aCorpus16k: true, v3aCorpus24k: true}
	runner := &Runner{Out: &strings.Builder{}, CorpusLookups: []CorpusLookup{corpus},
		Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
			return ObservationSnapshot{
				TerminalReason:     "synthetic",
				InterruptTick:      11,
				HasInterruptTick:   true,
				HasResponseCancel:  true,
				ResponseCancelTick: 14,
			}, nil
		}}

	outcome := runV3AScenario(t, runner, scenario)
	if outcome["pass"] != false {
		t.Fatalf("late cancellation must violate the tick bound: %v", outcome)
	}
	errText := firstFailedOutcomeError(t, outcome)
	if !strings.Contains(errText, "tick delta") {
		t.Fatalf("failure must report the bounded cancel-tick delta: %s", errText)
	}
}

func TestS2SV3ANoInterruptionControlPassesWithoutCancel(t *testing.T) {
	scenario := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicNoInterruption)
	passing := &Runner{Out: &strings.Builder{}, Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
		return ObservationSnapshot{TerminalReason: "synthetic"}, nil
	}}
	if outcome := runV3AScenario(t, passing, scenario); outcome["pass"] != true {
		t.Fatalf("no-interruption control must complete cleanly without a cancel: %v", outcome)
	}

	violated := &Runner{Out: &strings.Builder{}, Exec: func(ctx context.Context, s Scenario) (ObservationSnapshot, error) {
		return ObservationSnapshot{TerminalReason: "synthetic", HasResponseCancel: true, ResponseCancelTick: 2}, nil
	}}
	violatedOutcome := runV3AScenario(t, violated, scenario)
	if violatedOutcome["pass"] != false {
		t.Fatalf("an unexpected cancel must fail the no-interruption control: %v", violatedOutcome)
	}
	if !strings.Contains(firstFailedOutcomeError(t, violatedOutcome), "no RESPONSE.CANCEL") {
		t.Fatal("control failure must name the asserted absence")
	}
}

func TestS2SV3ARunnerFailureCarriesMismatchIdentity(t *testing.T) {
	scenario := findV3AScenario(t, ScenarioIDS2SV3ABargeInBasicCancelled16k)
	err := Evaluate(scenario.Expectations[0], ObservationSnapshot{})
	if !errors.Is(err, ErrExpectationMismatch) {
		t.Fatalf("public expect API must surface ErrExpectationMismatch for missing cancels: %v", err)
	}
}

// runV3AScenario drives one scenario through the runner and decodes its JSONL
// result record back out of the runner's output stream.
func runV3AScenario(t *testing.T, runner *Runner, scenario Scenario) map[string]any {
	t.Helper()
	summary, err := runner.Run(context.Background(), []Scenario{scenario})
	if err != nil {
		t.Fatalf("runner error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(runner.Out.(*strings.Builder).String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("runner emitted %d lines, want result+summary", len(lines))
	}
	var result map[string]any
	if jsonErr := json.Unmarshal([]byte(lines[0]), &result); jsonErr != nil {
		t.Fatalf("decode result line: %v", jsonErr)
	}
	var decodedSummary map[string]any
	if jsonErr := json.Unmarshal([]byte(lines[1]), &decodedSummary); jsonErr != nil {
		t.Fatalf("decode summary line: %v", jsonErr)
	}
	wantStatus := "fail"
	if result["pass"] == true {
		wantStatus = "pass"
	}
	if decodedSummary["status"] != wantStatus {
		t.Fatalf("summary status = %v, want %s", decodedSummary["status"], wantStatus)
	}
	if summary.Total != 1 {
		t.Fatalf("summary total = %d, want 1", summary.Total)
	}
	return result
}

func outcomeKinds(t *testing.T, result map[string]any) []ExpectationKind {
	t.Helper()
	outcomes, ok := result["expectations"].([]any)
	if !ok || len(outcomes) == 0 {
		t.Fatalf("result carries no expectation outcomes: %v", result)
	}
	kinds := make([]ExpectationKind, 0, len(outcomes))
	for _, raw := range outcomes {
		outcome := raw.(map[string]any)
		kinds = append(kinds, ExpectationKind(outcome["kind"].(string)))
	}
	return kinds
}

func firstFailedOutcomeError(t *testing.T, result map[string]any) string {
	t.Helper()
	outcomes, ok := result["expectations"].([]any)
	if !ok || len(outcomes) == 0 {
		t.Fatalf("result carries no expectation outcomes to inspect: %v", result)
	}
	for _, raw := range outcomes {
		outcome := raw.(map[string]any)
		if outcome["passed"] == false {
			message, _ := outcome["error"].(string)
			if message == "" {
				t.Fatalf("failed outcome lacks an error reason: %v", outcome)
			}
			return message
		}
	}
	t.Fatalf("result has no failed outcome: %v", result)
	return ""
}
