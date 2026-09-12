package agentruntime

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
	browserrunnerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner/wire"
)

// browserConversationInterruptionController is the CLI adapter for the
// provider-neutral runtime controller. It translates the runtime's audio
// value at the existing scheduled-audio seam without changing the runner.
type browserConversationInterruptionController struct {
	delegate browserrunner.InterruptionController
	channel  <-chan ScheduledAudioInput
}

func newBrowserConversationInterruptionController(
	run *BrowserConversationRun,
	tracker *browserConversationEvidenceTracker,
	scenario BrowserConversationScenario,
	audio map[string]ScheduledAudioInput,
) *browserConversationInterruptionController {
	delegate := browserrunnerwire.NewInterruptionController(browserrunner.InterruptionControllerConfig{
		Steps:     browserConversationTrackingSteps(scenario),
		Audio:     browserConversationAudioInputs(audio),
		Run:       &browserConversationRunRecorder{run: run},
		ErrorSink: tracker,
	})
	if delegate == nil {
		return nil
	}
	channel := make(chan ScheduledAudioInput, len(scenario.Steps))
	go func() {
		defer close(channel)
		for input := range delegate.AudioInterruptions() {
			channel <- ScheduledAudioInput{
				AfterCompletedTurns: input.AfterCompletedTurns,
				PCM:                 append([]byte(nil), input.PCM...),
				SourceSampleRate:    input.SourceSampleRate,
				EndOfTurn:           input.EndOfTurn,
			}
		}
	}()
	return &browserConversationInterruptionController{delegate: delegate, channel: channel}
}

func (c *browserConversationInterruptionController) AudioInterruptions() <-chan ScheduledAudioInput {
	if c == nil {
		return nil
	}
	return c.channel
}

// observeInFlight arms the next declared interruption only for the turn that
// can semantically own it: the declared interruption step itself or the
// immediately preceding step whose browser work it interrupts.
func (c *browserConversationInterruptionController) observeInFlight(stepID string, invocationID webmcp.InvocationID, toolName string) {
	if c == nil || c.delegate == nil {
		return
	}
	c.delegate.ObserveInFlight(stepID, string(invocationID), toolName)
}

func (c *browserConversationInterruptionController) Close() {
	if c == nil || c.delegate == nil {
		return
	}
	c.delegate.Close()
}

func browserConversationAudioInputs(audio map[string]ScheduledAudioInput) map[string]browserrunner.AudioInput {
	if audio == nil {
		return nil
	}
	inputs := make(map[string]browserrunner.AudioInput, len(audio))
	for stepID, input := range audio {
		inputs[stepID] = browserrunner.AudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 append([]byte(nil), input.PCM...),
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	return inputs
}

func cloneBrowserConversationAudioMap(audio map[string]ScheduledAudioInput) map[string]ScheduledAudioInput {
	if audio == nil {
		return nil
	}
	clone := make(map[string]ScheduledAudioInput, len(audio))
	for stepID, input := range audio {
		input.PCM = append([]byte(nil), input.PCM...)
		clone[stepID] = input
	}
	return clone
}

// partitionBrowserConversationAudio keeps ordinary turns on the existing
// completed-turn scheduler and holds interruption/cancel turns until their
// semantic trigger. The runtime owns the classification rules.
func partitionBrowserConversationAudio(
	scenario BrowserConversationScenario,
	inputs []ScheduledAudioInput,
) ([]ScheduledAudioInput, map[string]ScheduledAudioInput) {
	publicInputs := make([]browserrunner.AudioInput, len(inputs))
	for index, input := range inputs {
		publicInputs[index] = browserrunner.AudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 append([]byte(nil), input.PCM...),
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	normal, special := browserrunner.PartitionAudioInputs(browserConversationTrackingSteps(scenario), publicInputs)
	normalInputs := make([]ScheduledAudioInput, len(normal))
	for index, input := range normal {
		normalInputs[index] = ScheduledAudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 append([]byte(nil), input.PCM...),
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	specialInputs := make(map[string]ScheduledAudioInput, len(special))
	for stepID, input := range special {
		specialInputs[stepID] = ScheduledAudioInput{
			AfterCompletedTurns: input.AfterCompletedTurns,
			PCM:                 append([]byte(nil), input.PCM...),
			SourceSampleRate:    input.SourceSampleRate,
			EndOfTurn:           input.EndOfTurn,
		}
	}
	return normalInputs, specialInputs
}
