package probe

func init() {
	if err := RegisterScenario(Scenario{
		ID:          "s2s-v1-text-in-audio-out",
		Name:        "s2s_v1_text_in_audio_out",
		Description: "Vertical probe v1 baseline: a text prompt enters over the session path and an audio (or audio-transcript) response arrives before the session closes.",
		Steps: []Step{
			{Type: StepSendText, Text: "What is the weather today?"},
			{Type: StepClose},
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectFrameCount, Kind: ExpectFrameCount, Count: 9},
			{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"},
		},
	}); err != nil {
		panic(err)
	}
}
