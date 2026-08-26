package probe

import "testing"

func TestS2SV6BScenariosRegisterBothCasesWithTerminalTriple(t *testing.T) {
	registered := make(map[string]Scenario)
	for _, scenario := range Scenarios() {
		registered[scenario.ID] = scenario
	}

	for _, id := range []string{ScenarioIDS2SV6BDisconnectMidSession, ScenarioIDS2SV6BHealthyControl} {
		scenario, ok := registered[id]
		if !ok {
			t.Fatalf("scenario %q is not registered", id)
		}
		if err := scenario.Validate(); err != nil {
			t.Fatalf("scenario %q validation failed: %v", id, err)
		}
		if len(scenario.Steps) != 2 || scenario.Steps[0].Type != StepSendText || scenario.Steps[1].Type != StepClose {
			t.Fatalf("scenario %q steps = %#v, want send_text followed by close", id, scenario.Steps)
		}
		if len(scenario.Expectations) != 4 {
			t.Fatalf("scenario %q has %d expectations, want terminal triple plus transcript", id, len(scenario.Expectations))
		}
		for _, kind := range []ExpectationKind{ExpectTerminalReason, ExpectTerminalProvenance, ExpectOutputState} {
			found := false
			for _, expectation := range scenario.Expectations {
				if expectation.Type == kind {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("scenario %q does not declare %q", id, kind)
			}
		}
	}
}
