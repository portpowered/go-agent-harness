package agentruntime

func cloneScheduledAudioInputs(inputs []ScheduledAudioInput) []ScheduledAudioInput {
	clone := make([]ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		clone[index] = input
		clone[index].PCM = append([]byte(nil), input.PCM...)
	}
	return clone
}
