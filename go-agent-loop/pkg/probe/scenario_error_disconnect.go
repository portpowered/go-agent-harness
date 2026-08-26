package probe

// Registered scenarios for the v6b provider-disconnect error-path vertical.
// The disconnect case replays partial provider output and then a transport
// EOF; the healthy control replays the same kind of response through an
// explicit response.done boundary. Both cases assert the complete terminal
// triple so a reason-only disconnect result cannot pass.

const (
	// ScenarioIDS2SV6BErrorDisconnect selects the whole disconnect vertical;
	// every registered case whose ID extends this prefix runs when it is
	// selected.
	ScenarioIDS2SV6BErrorDisconnect = "s2s-v6b-error-disconnect"
	// ScenarioIDS2SV6BDisconnectMidSession is the mid-session-drop case backed
	// by s2s-v6b-error-disconnect-mid-session.session.json.
	ScenarioIDS2SV6BDisconnectMidSession = ScenarioIDS2SV6BErrorDisconnect + "-mid-session"
	// ScenarioIDS2SV6BHealthyControl is the explicit-completion control case
	// backed by s2s-v6b-error-disconnect-healthy-control.session.json.
	ScenarioIDS2SV6BHealthyControl = ScenarioIDS2SV6BErrorDisconnect + "-healthy-control"
)

const scenarioS2SV6BInput = "tell me about transport failures"

func init() {
	for _, registration := range []struct {
		id           string
		description  string
		transcript   string
		expectations []ExpectedBehavior
	}{
		{
			id:          ScenarioIDS2SV6BDisconnectMidSession,
			description: "A provider transport disconnect after partial output must report disconnect, provider provenance, and partial output state",
			transcript:  "v6b midstream answer cut off when the transport dropped",
			expectations: []ExpectedBehavior{
				{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "disconnect"},
				{Type: ExpectTerminalProvenance, Kind: ExpectTerminalProvenance, Value: "provider"},
				{Type: ExpectOutputState, Kind: ExpectOutputState, Value: "partial"},
				{Type: ExpectTranscriptContains, Kind: ExpectTranscriptContains, Text: "v6b midstream answer cut off when the transport dropped"},
			},
		},
		{
			id:          ScenarioIDS2SV6BHealthyControl,
			description: "An explicit provider response completion must remain distinct from a transport disconnect and report complete output",
			transcript:  "v6b healthy response completed normally",
			expectations: []ExpectedBehavior{
				{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "complete"},
				{Type: ExpectTerminalProvenance, Kind: ExpectTerminalProvenance, Value: "provider"},
				{Type: ExpectOutputState, Kind: ExpectOutputState, Value: "complete"},
				{Type: ExpectTranscriptContains, Kind: ExpectTranscriptContains, Text: "v6b healthy response completed normally"},
			},
		},
	} {
		expectations := append([]ExpectedBehavior(nil), registration.expectations...)
		expectations[3].Text = registration.transcript
		scenario := Scenario{
			ID:               registration.id,
			Name:             registration.id,
			Description:      registration.description,
			Steps:            []Step{{Type: StepSendText, Text: scenarioS2SV6BInput}, {Type: StepClose}},
			Expectations:     expectations,
			Expected:         expectations,
			ExpectedBehavior: expectations,
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}
