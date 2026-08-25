package probe

// Registered scenarios for the v6d malformed-response error-path vertical.
//
// Negative control (documented contract): the malformed case asserts a
// parse/malformed-classified terminal error ("error:invalid_request"). Feeding
// the healthy well-formed control fixture into that same malformed expectation
// must FAIL: the healthy session terminates with a plain disconnect and no
// error classification, so the expectation cannot be satisfied. This negative
// control is exercised as a CI test case in agent-cli/internal/cli/probe_test.go.

const (
	// ScenarioIDS2SV6DErrorMalformedResponse selects the whole
	// malformed-response vertical suite; every registered case whose ID
	// extends this prefix runs when it is selected.
	ScenarioIDS2SV6DErrorMalformedResponse = "s2s-v6d-error-malformed-response"
	// ScenarioIDS2SV6DMalformed is the malformed case backed by a fixture whose
	// provider frame is truncated mid-JSON and unparseable. The session must
	// terminate with the typed parse/malformed classification instead of
	// panicking, hanging, or silently succeeding.
	ScenarioIDS2SV6DMalformed = ScenarioIDS2SV6DErrorMalformedResponse + "-malformed"
	// ScenarioIDS2SV6DHealthyControl is the passing control case backed by a
	// well-formed fixture ending in a clean disconnect.
	ScenarioIDS2SV6DHealthyControl = ScenarioIDS2SV6DErrorMalformedResponse + "-healthy-control"
)

func init() {
	for _, registration := range []struct {
		id          string
		name        string
		description string
		text        string
		expectation ExpectedBehavior
	}{
		{
			id:          ScenarioIDS2SV6DMalformed,
			name:        "v6d malformed response: truncated provider frame",
			description: "Session receiving an unparseable truncated-JSON provider frame must terminate with a parse/malformed-classified typed error",
			text:        "probe input",
			expectation: ExpectedBehavior{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "error:invalid_request"},
		},
		{
			id:          ScenarioIDS2SV6DHealthyControl,
			name:        "v6d malformed response: healthy control",
			description: "Session receiving well-formed provider frames must terminate cleanly without firing the error or deadguard paths",
			text:        "probe input",
			expectation: ExpectedBehavior{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "disconnect"},
		},
	} {
		scenario := Scenario{
			ID:          registration.id,
			Name:        registration.id,
			Description: registration.description,
			Steps: []Step{
				{Type: StepSendText, Text: registration.text},
				{Type: StepClose},
			},
			Expectations:     []ExpectedBehavior{registration.expectation},
			Expected:         []ExpectedBehavior{registration.expectation},
			ExpectedBehavior: []ExpectedBehavior{registration.expectation},
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}
