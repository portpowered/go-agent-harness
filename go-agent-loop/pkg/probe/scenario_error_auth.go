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

func registerErrorAuthScenarios(register func(Scenario, ...DeadSessionControl) error) error {
	return registerTerminalReasonScenarios(register,
		terminalReasonCase{
			id:          ScenarioIDS2SV6AErrorAuthInvalidCredentials,
			description: "Session attempt with invalid credentials must terminate with an auth-classified error",
			reason:      "error:authentication",
		},
		terminalReasonCase{
			id:          ScenarioIDS2SV6AErrorAuthHealthyControl,
			description: "Healthy session must terminate cleanly without firing the error or deadguard paths",
			reason:      "disconnect",
		},
	)
}

// terminalReasonInput is the text every terminal-reason error-path scenario
// sends before closing.
const terminalReasonInput = "probe input"

// terminalReasonCase is an error-path scenario that sends one text input,
// closes the session, and is judged only by its terminal reason.
type terminalReasonCase struct {
	id          string
	description string
	reason      string
}

// terminalReasonScenario builds the scenario for one terminal-reason case.
func terminalReasonScenario(c terminalReasonCase) Scenario {
	expectation := ExpectedBehavior{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: c.reason}
	return Scenario{
		ID:          c.id,
		Name:        c.id,
		Description: c.description,
		Steps: []Step{
			{Type: StepSendText, Text: terminalReasonInput},
			{Type: StepClose},
		},
		Expectations:     []ExpectedBehavior{expectation},
		Expected:         []ExpectedBehavior{expectation},
		ExpectedBehavior: []ExpectedBehavior{expectation},
	}
}

// registerTerminalReasonScenarios registers each case in order.
func registerTerminalReasonScenarios(register func(Scenario, ...DeadSessionControl) error, cases ...terminalReasonCase) error {
	for _, c := range cases {
		if err := register(terminalReasonScenario(c)); err != nil {
			return err
		}
	}
	return nil
}
