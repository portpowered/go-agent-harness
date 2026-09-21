package service

import "fmt"

func admitScenario(scenario BrowserConversationScenario) (BrowserConversationScenario, error) {
	if err := validateScenario(scenario); err != nil {
		return BrowserConversationScenario{}, err
	}
	return scenario.Clone(), nil
}

func scheduleAudioInputs(scenario BrowserConversationScenario, audioByStep map[string][]byte) ([]ScheduledAudioInput, error) {
	if err := validateScenario(scenario); err != nil {
		return nil, err
	}
	if audioByStep == nil {
		return nil, browserScenarioError("audio", "one PCM payload is required for every step")
	}
	inputs := make([]ScheduledAudioInput, len(scenario.Steps))
	for index, step := range scenario.Steps {
		pcm, ok := audioByStep[step.ID]
		if !ok || len(pcm) == 0 {
			return nil, browserScenarioError(fmt.Sprintf("steps[%d].audio", index), "one non-empty PCM payload is required")
		}
		inputs[index] = ScheduledAudioInput{AfterCompletedTurns: index, PCM: append([]byte(nil), pcm...), EndOfTurn: true}
	}
	return inputs, nil
}
