package probe

// Registered scenarios for the s2s-v7a metrics-modality vertical. The positive
// case requires one hermetic replayed session to emit per-direction audio,
// text, and tool metric series that reconcile exactly with the observed delta
// stream. The overcount case is the negative control: its execution seam
// deliberately reports output/tool as the observed delta sum plus one, so the
// metrics reconciliation expectation must fail with expected-vs-actual detail.
//
// Both cases run offline through the CLI probe entrypoints over the shared
// committed fixture s2s-v7a-metrics-modality.session.json.

const (
	// ScenarioIDS2SV7AMetricsModality is the passing positive case: text in,
	// audio out, and one tool call whose emitted per-modality totals equal
	// the summed observed deltas.
	ScenarioIDS2SV7AMetricsModality = "s2s-v7a-metrics-modality"
	// ScenarioIDS2SV7AMetricsModalityOvercount is the deliberately mismatched
	// negative control; running it must fail on the metrics-reconcile kind.
	ScenarioIDS2SV7AMetricsModalityOvercount = ScenarioIDS2SV7AMetricsModality + "-overcount"
)

// ScenarioTextS2SV7A is the user turn exercised by both v7a cases. It must
// match the conversation.item.create record captured in the committed fixture.
const ScenarioTextS2SV7A = "What is the weather in Paris?"

func init() {
	expectations := []ExpectedBehavior{
		{Type: ExpectTranscriptContains, Kind: ExpectTranscriptContains, Text: "It is sunny in Paris."},
		{Type: ExpectMetricsReconcile, Kind: ExpectMetricsReconcile},
	}
	for _, registration := range []struct {
		id          string
		description string
	}{
		{
			id:          ScenarioIDS2SV7AMetricsModality,
			description: "Positive case: emitted audio/text/tool per-modality totals reconcile exactly with the observed delta stream",
		},
		{
			id:          ScenarioIDS2SV7AMetricsModalityOvercount,
			description: "Negative control: an injected off-by-one overcount on output/tool must fail the metrics reconciliation",
		},
	} {
		scenario := Scenario{
			ID:          registration.id,
			Name:        registration.id,
			Description: registration.description,
			Steps: []Step{
				{Type: StepSendText, Text: ScenarioTextS2SV7A},
				{Type: StepClose},
			},
			Expectations:     expectations,
			Expected:         expectations,
			ExpectedBehavior: expectations,
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}
