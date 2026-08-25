package probe

// Registered scenarios for the s2s v3b barge-in-during-tool-call vertical.
// Each case replays a recorded session fixture in which a user barge-in lands
// while a tool call is in flight. The standing invariant: every issued tool
// call must end either delivered to the provider or explicitly discarded via
// an observable discard event. The orphaned case encodes the invariant's
// violation and therefore must fail when driven through the CLI.

const (
	// ScenarioIDS2SV3BBargeInToolResult selects the whole v3b suite; every
	// registered case whose ID extends this prefix runs when it is selected.
	ScenarioIDS2SV3BBargeInToolResult = "s2s-v3b-barge-in-tool-result"
	// ScenarioIDS2SV3BBargeInToolResultDelivered is the case where the tool
	// result still reaches the provider after the barge-in.
	ScenarioIDS2SV3BBargeInToolResultDelivered = ScenarioIDS2SV3BBargeInToolResult + "-delivered"
	// ScenarioIDS2SV3BBargeInToolResultDiscarded is the case where the loop
	// explicitly discards the tool result through an observable event.
	ScenarioIDS2SV3BBargeInToolResultDiscarded = ScenarioIDS2SV3BBargeInToolResult + "-discarded"
	// ScenarioIDS2SV3BBargeInToolResultOrphaned is the negative control: the
	// tool result silently vanishes after the barge-in and the run must fail.
	ScenarioIDS2SV3BBargeInToolResultOrphaned = ScenarioIDS2SV3BBargeInToolResult + "-orphaned"
)

// v3bToolCallID is the provider-issued call id shared by every v3b fixture.
const v3bToolCallID = "call_v3b_weather"

func init() {
	for _, registration := range []struct {
		id           string
		name         string
		description  string
		expectations []ExpectedBehavior
	}{
		{
			id:          ScenarioIDS2SV3BBargeInToolResultDelivered,
			name:        "v3b barge-in during tool call: result delivered",
			description: "A barge-in mid tool-call must not prevent the tool result from reaching the provider",
			expectations: []ExpectedBehavior{
				{Type: ExpectToolResultDelivered, Kind: ExpectToolResultDelivered, ToolCallID: v3bToolCallID},
				{Type: ExpectNoOrphanedToolResult, Kind: ExpectNoOrphanedToolResult},
				{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"},
			},
		},
		{
			id:          ScenarioIDS2SV3BBargeInToolResultDiscarded,
			name:        "v3b barge-in during tool call: result explicitly discarded",
			description: "An explicit discard of the tool result after barge-in must surface an observable discard event and exit cleanly",
			expectations: []ExpectedBehavior{
				{Type: ExpectToolResultDiscarded, Kind: ExpectToolResultDiscarded, ToolCallID: v3bToolCallID},
				{Type: ExpectNoOrphanedToolResult, Kind: ExpectNoOrphanedToolResult},
				{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic_failure"},
			},
		},
		{
			id:          ScenarioIDS2SV3BBargeInToolResultOrphaned,
			name:        "v3b barge-in during tool call: negative control (orphaned result)",
			description: "A vanished tool result with no delivery and no discard event must fail the run as an orphaned tool result",
			expectations: []ExpectedBehavior{
				{Type: ExpectToolResultDelivered, Kind: ExpectToolResultDelivered, ToolCallID: v3bToolCallID},
				{Type: ExpectNoOrphanedToolResult, Kind: ExpectNoOrphanedToolResult},
			},
		},
	} {
		scenario := Scenario{
			ID:               registration.id,
			Name:             registration.id,
			Description:      registration.description,
			Steps:            []Step{{Type: StepSendText, Text: "probe input"}, {Type: StepClose}},
			Expectations:     registration.expectations,
			Expected:         registration.expectations,
			ExpectedBehavior: registration.expectations,
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}
