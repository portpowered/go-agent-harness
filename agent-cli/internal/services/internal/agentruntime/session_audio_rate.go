package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	audioratewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate/wire"
)

// Deprecated: use audiorate.ErrPCM16Truncated at the public boundary.
var ErrSessionAudioPCM16Truncated = audiorate.ErrPCM16Truncated

// Deprecated: retained as a source-compatible adapter for the session caller.
func convertSessionAudioPCM(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	return audioratewire.NewService().ConvertPCM(context.Background(), pcm, sourceRate, providerRate)
}

// Deprecated: retained as a source-compatible scheduled-input adapter.
func convertScheduledAudioInputs(inputs []ScheduledAudioInput, providerRate int) ([]ScheduledAudioInput, error) {
	if inputs == nil {
		return nil, nil
	}
	request := make([]audiorate.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		request[index] = audiorate.ScheduledAudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 input.PCM,
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	converted, err := audioratewire.NewService().ConvertScheduledAudioInputs(context.Background(), request, providerRate)
	if err != nil {
		return nil, err
	}
	result := make([]ScheduledAudioInput, len(converted))
	for index, input := range converted {
		result[index] = ScheduledAudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 input.PCM,
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	return result, nil
}
