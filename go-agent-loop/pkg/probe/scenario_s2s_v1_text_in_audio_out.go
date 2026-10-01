package probe

func registerS2SV1TextInAudioOutScenario(register func(Scenario, ...DeadSessionControl) error) error {
	if err := register(Scenario{
		ID:          "s2s-v1-text-in-audio-out",
		Name:        "s2s_v1_text_in_audio_out",
		Description: "Vertical probe v1 baseline: a text prompt enters over the session path and an audio (or audio-transcript) response arrives before the session closes.",
		Steps: []Step{
			{Type: StepSendText, Text: "What is the weather today?"},
			{Type: StepClose},
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectFrameCount, Kind: ExpectFrameCount, Count: s2sV1ExpectedFrameCount},
			{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"},
		},
	}); err != nil {
		return err
	}
	return nil
}

// s2sV1ExpectedFrameCount is the frame count the v1 text-in/audio-out
// fixture produces.
const s2sV1ExpectedFrameCount = 9
