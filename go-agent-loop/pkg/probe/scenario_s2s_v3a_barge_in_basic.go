package probe

import "fmt"

// Registered scenarios for the s2s v3a barge-in vertical. Each case replays a
// recorded session fixture in which an assistant audio response is streaming
// and the recorded overlap_* user utterance arrives mid-response. The standing
// contract from the lane manifest: the interrupting input cancels the in-flight
// response at the provider — a RESPONSE.CANCEL must cross the client-to-provider
// path within a bounded tick window of the interrupting audio's outbound tick.
//
// The no-interruption case replays an uninterrupted response and asserts the
// opposite invariant: no RESPONSE.CANCEL is emitted when no user audio arrives.

const (
	// ScenarioIDS2SV3ABargeInBasic selects the whole v3a suite; every
	// registered case whose ID extends this prefix runs when it is selected.
	ScenarioIDS2SV3ABargeInBasic = "s2s-v3a-barge-in-basic"
	// ScenarioIDS2SV3ABargeInBasicCancelled16k drives the overlap_16k corpus
	// fixture: user audio lands mid-response and RESPONSE.CANCEL follows
	// within the bounded tick window.
	ScenarioIDS2SV3ABargeInBasicCancelled16k = ScenarioIDS2SV3ABargeInBasic + "-cancelled-16k"
	// ScenarioIDS2SV3ABargeInBasicCancelled24k drives the overlap_24k corpus
	// fixture through the same barge-in contract at the 24 kHz sample rate.
	ScenarioIDS2SV3ABargeInBasicCancelled24k = ScenarioIDS2SV3ABargeInBasic + "-cancelled-24k"
	// ScenarioIDS2SV3ABargeInBasicNoInterruption is the negative control: the
	// response streams to completion without user audio and must not be
	// cancelled.
	ScenarioIDS2SV3ABargeInBasicNoInterruption = ScenarioIDS2SV3ABargeInBasic + "-no-interruption"
)

const (
	// v3a interrupting-audio corpus IDs from go-agent-loop/testdata/audio.
	v3aCorpus16k = "overlap_16k"
	v3aCorpus24k = "overlap_24k"

	// v3aCancelBoundTicks bounds how many ticks may pass between the
	// interrupting audio and the observed RESPONSE.CANCEL.
	v3aCancelBoundTicks = 2

	// v3aPrompt opens every v3a case so the assistant response starts before
	// the interrupting input arrives.
	v3aPrompt = "Tell me about today's schedule."
)

func init() {
	cancelledExpectations := func() []ExpectedBehavior {
		return []ExpectedBehavior{
			{Type: ExpectResponseCancel, Kind: ExpectResponseCancel},
			// The latency expectation intentionally omits At. The replay
			// observation supplies the actual first append tick, and the
			// evaluator measures through the observed response cancel tick.
			{Type: ExpectLatencyWithinTicks, Kind: ExpectLatencyWithinTicks, Count: v3aCancelBoundTicks},
			{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"},
		}
	}
	cancelledDescription := fmt.Sprintf(
		"User speech arrives while the assistant audio response streams; "+
			"RESPONSE.CANCEL must reach the provider within %d ticks of the interrupting audio",
		v3aCancelBoundTicks)
	for _, registration := range []struct {
		id           string
		description  string
		steps        []Step
		expectations []ExpectedBehavior
	}{
		{
			id:           ScenarioIDS2SV3ABargeInBasicCancelled16k,
			description:  cancelledDescription + " (overlap_16k)",
			steps:        []Step{{Type: StepSendText, Text: v3aPrompt}, {Type: StepSendAudio, CorpusID: v3aCorpus16k}, {Type: StepClose}},
			expectations: cancelledExpectations(),
		},
		{
			id:           ScenarioIDS2SV3ABargeInBasicCancelled24k,
			description:  cancelledDescription + " (overlap_24k)",
			steps:        []Step{{Type: StepSendText, Text: v3aPrompt}, {Type: StepSendAudio, CorpusID: v3aCorpus24k}, {Type: StepClose}},
			expectations: cancelledExpectations(),
		},
		{
			id: ScenarioIDS2SV3ABargeInBasicNoInterruption,
			description: "Negative control: the response streams to completion with no user audio, so " +
				"no RESPONSE.CANCEL may cross the client-to-provider path",
			steps: []Step{{Type: StepSendText, Text: v3aPrompt}, {Type: StepClose}},
			expectations: []ExpectedBehavior{
				{Type: ExpectResponseCancel, Kind: ExpectResponseCancel, Value: ResponseCancelNone},
				{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"},
			},
		},
	} {
		scenario := Scenario{
			ID:               registration.id,
			Name:             registration.id,
			Description:      registration.description,
			Steps:            registration.steps,
			Expectations:     registration.expectations,
			Expected:         registration.expectations,
			ExpectedBehavior: registration.expectations,
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}
