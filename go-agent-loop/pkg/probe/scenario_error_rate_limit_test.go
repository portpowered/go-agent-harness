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
