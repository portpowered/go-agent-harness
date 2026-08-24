package probe

import "testing"

func registeredS2SV1TextInAudioOut(t *testing.T) Scenario {
	t.Helper()
	for _, scenario := range Scenarios() {
		if scenario.ID == "s2s-v1-text-in-audio-out" {
			return scenario
		}
	}
	t.Fatalf("scenario s2s-v1-text-in-audio-out is not registered")
	return Scenario{}
}

func TestS2SV1TextInAudioOutRegisteredAndValid(t *testing.T) {
	scenario := registeredS2SV1TextInAudioOut(t)
	if err := scenario.Validate(); err != nil {
		t.Fatalf("registered scenario does not validate: %v", err)
	}
	if len(scenario.Steps) < 2 {
		t.Fatalf("expected prompt and close steps, got %d", len(scenario.Steps))
	}
	if scenario.Steps[0].Type != StepSendText || scenario.Steps[0].Text == "" {
		t.Fatalf("first step must send a text prompt, got %+v", scenario.Steps[0])
	}
	last := scenario.Steps[len(scenario.Steps)-1]
	if last.Type != StepClose {
		t.Fatalf("last step must be close, got %q", last.Type)
	}
	if len(scenario.Expectations) == 0 {
		t.Fatalf("at least one expectation is required")
	}
}

func TestS2SV1TextInAudioOutEvaluateAgainstAudioResponseObservation(t *testing.T) {
	scenario := registeredS2SV1TextInAudioOut(t)

	passing := ObservationSnapshot{FrameCount: 9, HasObservedTick: true, ObservedTick: 1, TerminalReason: "synthetic"}
	results := EvaluateScenario(scenario, passing)
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("expectation %s should pass on audio response observation: %v", result.Kind, result.Err)
		}
	}

	emptyResponse := ObservationSnapshot{FrameCount: 4, HasObservedTick: true, ObservedTick: 1, TerminalReason: "synthetic_failure"}
	failed := 0
	for _, result := range EvaluateScenario(scenario, emptyResponse) {
		if !result.Passed {
			failed++
		}
	}
	if failed == 0 {
		t.Fatalf("empty-response observation must fail at least one expectation")
	}
}
