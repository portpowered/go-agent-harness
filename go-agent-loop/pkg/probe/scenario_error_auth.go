package probe

// Registered scenarios for the v6a auth-failure error-path vertical. The
// invalid-credentials case requires the session to terminate with an
// auth-classified error (surfaced by the replay exec seam as the terminal
// reason "error:<classification>"); the healthy control case requires a clean
// disconnect. Both run offline over recorded fixtures selected by scenario
// name or ID.

const (
	// ScenarioIDS2SV6AErrorAuth selects the whole auth-error vertical suite;
	// every registered case whose ID extends this prefix runs when it is
	// selected.
	ScenarioIDS2SV6AErrorAuth = "s2s-v6a-error-auth"
	// ScenarioIDS2SV6AErrorAuthInvalidCredentials is the invalid-credentials
	// case backed by a recorded 401-style provider fixture.
	ScenarioIDS2SV6AErrorAuthInvalidCredentials = ScenarioIDS2SV6AErrorAuth + "-invalid-credentials"
	// ScenarioIDS2SV6AErrorAuthHealthyControl is the passing control case.
	ScenarioIDS2SV6AErrorAuthHealthyControl = ScenarioIDS2SV6AErrorAuth + "-healthy-control"
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
			id:          ScenarioIDS2SV6AErrorAuthInvalidCredentials,
			name:        "v6a auth failure: invalid credentials",
			description: "Session attempt with invalid credentials must terminate with an auth-classified error",
			text:        "probe input",
			expectation: ExpectedBehavior{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "error:authentication"},
		},
		{
			id:          ScenarioIDS2SV6AErrorAuthHealthyControl,
			name:        "v6a auth failure: healthy control",
			description: "Healthy session must terminate cleanly without firing the error or deadguard paths",
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
