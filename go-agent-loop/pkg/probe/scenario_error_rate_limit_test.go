package probe

import (
	"errors"
	"testing"
)

func TestS2SV6CErrorRateLimitRegistrationFailsFast(t *testing.T) {
	want := errors.New("registration failed")
	defer func() {
		got, ok := recover().(error)
		if !ok || !errors.Is(got, want) {
			t.Fatalf("registration panic = %v, want %v", got, want)
		}
	}()
	registerS2SV6CErrorRateLimitScenario(func(Scenario, ...DeadSessionControl) error {
		return want
	})
}

// anyCorpus accepts every corpus reference so built-in scenarios validate
// structurally; the CLI supplies the real replay corpus lookup.
type anyCorpus struct{}

func (anyCorpus) Has(string) bool { return true }

// Every built-in scenario validates, ends by closing the session, and
// declares at least one expectation. The scenarios the probe entrypoints
// select by ID are registered and carry the invariants their lanes assert.
// Registration does not validate, so this is the only registry-wide guard.
func TestBuiltinScenariosValidateAndDeclareLaneInvariants(t *testing.T) {
	registered := map[string]Scenario{}
	for _, scenario := range newBuiltinScenarioRegistry().Snapshot() {
		registered[scenario.ID] = scenario
		if err := scenario.Validate(anyCorpus{}); err != nil {
			t.Errorf("scenario %q does not validate: %v", scenario.ID, err)
			continue
		}
		if last := scenario.Steps[len(scenario.Steps)-1]; last.Type != StepClose {
			t.Errorf("scenario %q must end with close, got %q", scenario.ID, last.Type)
		}
		if len(scenario.Expectations) == 0 {
			t.Errorf("scenario %q declares no expectations", scenario.ID)
		}
	}

	v3c := []ExpectationKind{ExpectBargeInCancelOnce, ExpectMessageCountsReconcile}
	terminalTriple := []ExpectationKind{ExpectTerminalReason, ExpectTerminalProvenance, ExpectOutputState}
	required := map[string][]ExpectationKind{
		"s2s-v1-text-in-audio-out":                    nil,
		ScenarioIDS2SV3ABargeInBasicCancelled16k:      {ExpectLatencyWithinTicks},
		ScenarioIDS2SV3ABargeInBasicCancelled24k:      {ExpectLatencyWithinTicks},
		ScenarioIDS2SV3ABargeInBasicNoInterruption:    nil,
		ScenarioIDS2SV3CBargeInRepeated:               v3c,
		ScenarioIDS2SV3CBargeInRepeatedDuplicatedTurn: v3c,
		ScenarioIDS2SV3CBargeInRepeatedDroppedCommit:  v3c,
		ScenarioIDS2SV3CBargeInRepeatedDoubleCancel:   v3c,
		ScenarioIDS2SV6BDisconnectMidSession:          terminalTriple,
		ScenarioIDS2SV6BHealthyControl:                terminalTriple,
		ScenarioIDS2SV6CErrorRateLimitThrottled:       {ExpectTerminalReason},
		ScenarioIDS2SV7AMetricsModality:               {ExpectMetricsReconcile},
		ScenarioIDS2SV7AMetricsModalityOvercount:      {ExpectMetricsReconcile},
	}
	for id, kinds := range required {
		scenario, ok := registered[id]
		if !ok {
			t.Errorf("scenario %q is not registered", id)
			continue
		}
		declared := map[ExpectationKind]bool{}
		for _, expectation := range scenario.Expectations {
			declared[expectation.Type] = true
			declared[expectation.Kind] = true
		}
		for _, kind := range kinds {
			if !declared[kind] {
				t.Errorf("scenario %q must declare %q", id, kind)
			}
		}
	}
}
