package probe

import (
	"errors"
	"testing"
)

func registeredS2SV6CErrorRateLimitScenario(t *testing.T) Scenario {
	t.Helper()
	for _, scenario := range Scenarios() {
		if scenario.ID == ScenarioIDS2SV6CErrorRateLimitThrottled {
			return scenario
		}
	}
	t.Fatalf("scenario %q is not registered", ScenarioIDS2SV6CErrorRateLimitThrottled)
	return Scenario{}
}

func TestS2SV6CErrorRateLimitScenarioRegisteredAndValid(t *testing.T) {
	scenario := registeredS2SV6CErrorRateLimitScenario(t)
	if err := scenario.Validate(); err != nil {
		t.Fatalf("registered scenario does not validate: %v", err)
	}
	if scenario.Name != ScenarioIDS2SV6CErrorRateLimitThrottled {
		t.Fatalf("scenario name = %q, want %q", scenario.Name, ScenarioIDS2SV6CErrorRateLimitThrottled)
	}
	if len(scenario.Steps) != 2 || scenario.Steps[0].Type != StepSendText || scenario.Steps[0].Text == "" || scenario.Steps[1].Type != StepClose {
		t.Fatalf("scenario must send text then close, got %#v", scenario.Steps)
	}
	if len(scenario.Expectations) != 1 {
		t.Fatalf("scenario must declare one terminal-reason expectation, got %d", len(scenario.Expectations))
	}
	expectation := scenario.Expectations[0]
	if expectation.Type != ExpectTerminalReason || expectation.Kind != ExpectTerminalReason || expectation.Value != "error:rate_limited" {
		t.Fatalf("unexpected terminal-reason expectation: %#v", expectation)
	}
	if len(scenario.Expected) != 1 || len(scenario.ExpectedBehavior) != 1 {
		t.Fatalf("scenario expectation aliases must be populated: expected=%#v expected_behavior=%#v", scenario.Expected, scenario.ExpectedBehavior)
	}

	var constructed Scenario
	registerS2SV6CErrorRateLimitScenario(func(got Scenario, controls ...DeadSessionControl) error {
		if len(controls) != 0 {
			t.Fatalf("scenario registration unexpectedly supplied controls: %v", controls)
		}
		constructed = got
		return nil
	})
	if constructed.ID != scenario.ID || constructed.Description != scenario.Description {
		t.Fatalf("registration constructed a different scenario: got=%#v registered=%#v", constructed, scenario)
	}
}

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
