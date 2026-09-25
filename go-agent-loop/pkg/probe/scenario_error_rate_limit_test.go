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
