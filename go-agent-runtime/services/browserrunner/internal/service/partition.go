package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"

// partitionAudioInputs keeps ordinary turns on a completed-turn scheduler and
// holds interruption/cancel turns until their semantic trigger. Both returned
// collections own defensive PCM copies.
func partitionAudioInputs(steps []browserrunner.StepBoundary, inputs []browserrunner.AudioInput) ([]browserrunner.AudioInput, map[string]browserrunner.AudioInput) {
	specialIDs := specialStepIDs(steps)
	normal := make([]browserrunner.AudioInput, 0, len(inputs))
	special := make(map[string]browserrunner.AudioInput, len(specialIDs))
	for index, input := range inputs {
		stepID := stepIDAt(steps, index)
		input.PCM = append([]byte(nil), input.PCM...)
		if _, held := specialIDs[stepID]; held {
			special[stepID] = input
			continue
		}
		input.AfterCompletedTurns = len(normal)
		normal = append(normal, input)
	}
	return normal, special
}

func specialStepIDs(steps []browserrunner.StepBoundary) map[string]struct{} {
	ids := make(map[string]struct{})
	for index, step := range steps {
		if step.Interrupt == nil && step.Cancel == nil {
			continue
		}
		if step.Interrupt != nil || step.Cancel != nil {
			ids[step.ID] = struct{}{}
		}
		if step.Interrupt == nil {
			continue
		}
		ids = addFollowingCancelID(ids, steps, index)
	}
	return ids
}

func addFollowingCancelID(ids map[string]struct{}, steps []browserrunner.StepBoundary, index int) map[string]struct{} {
	for later := index + 1; later < len(steps); later++ {
		if steps[later].Cancel != nil {
			ids[steps[later].ID] = struct{}{}
			break
		}
		if steps[later].Interrupt != nil {
			break
		}
	}
	return ids
}

func stepIDAt(steps []browserrunner.StepBoundary, index int) string {
	if index >= len(steps) {
		return ""
	}
	return steps[index].ID
}
