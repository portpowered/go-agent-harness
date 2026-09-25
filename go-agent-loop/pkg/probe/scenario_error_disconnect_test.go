package probe

import "testing"

func TestS2SV6BTerminalExpectationAliasesLoadAndEvaluate(t *testing.T) {
	scenario, err := Load(`{
		"id":"s2s-v6b-terminal-aliases",
		"steps":[{"type":"close"}],
		"expectations":[
			{"type":"terminal-reason","value":"disconnect"},
			{"kind":"terminal-provenance","value":"provider"},
			{"type":"terminal-output-state","value":"partial"}
		]
	}`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	results := EvaluateScenario(scenario, ObservationSnapshot{
		TerminalReason:     "disconnect",
		TerminalProvenance: "provider",
		OutputState:        "partial",
	})
	if len(results) != 3 {
		t.Fatalf("expectation result count = %d, want 3", len(results))
	}
	for index, result := range results {
		if !result.Passed || result.Err != nil {
			t.Fatalf("terminal expectation %d failed: %#v", index, result)
		}
	}
}
