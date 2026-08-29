package services

import (
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// browserConversationInterruptionController converts a semantic broker event
// into customer audio on the shared duplex-session seam. It deliberately has
// no timer: the trigger is the broker's nonterminal invocation observation.
type browserConversationInterruptionController struct {
	mu        sync.Mutex
	run       *BrowserConversationRun
	tracker   *browserConversationEvidenceTracker
	scenario  BrowserConversationScenario
	audio     map[string]ScheduledAudioInput
	channel   chan ScheduledAudioInput
	triggered map[string]bool
	closed    bool
}

func newBrowserConversationInterruptionController(
	run *BrowserConversationRun,
	tracker *browserConversationEvidenceTracker,
	scenario BrowserConversationScenario,
	audio map[string]ScheduledAudioInput,
) *browserConversationInterruptionController {
	controlCount := 0
	for _, step := range scenario.Steps {
		if step.Interrupt != nil || step.Cancel != nil {
			controlCount++
		}
	}
	if controlCount == 0 {
		return nil
	}
	return &browserConversationInterruptionController{
		run:       run,
		tracker:   tracker,
		scenario:  scenario,
		audio:     cloneBrowserConversationAudioMap(audio),
		channel:   make(chan ScheduledAudioInput, len(scenario.Steps)),
		triggered: make(map[string]bool),
	}
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
	if c == nil || invocationID == "" {
		return
	}
	trigger, cancelStep, ok := c.nextEligibleInterruption(stepID, toolName)
	if !ok {
		return
	}
	input := c.audio[trigger.ID]

	c.mu.Lock()
	if c.closed || c.triggered[trigger.ID] {
		c.mu.Unlock()
		return
	}
	c.triggered[trigger.ID] = true
	c.mu.Unlock()

	triggerSent := c.enqueueAudio(input)
	cancelSent := true
	if cancelStep != nil {
		// The explicit cancel input is queued immediately after the overlap
		// input, preserving the customer's utterance order on the shared loop.
		cancelSent = c.enqueueAudio(c.audio[cancelStep.ID])
	}
	if !triggerSent || !cancelSent {
		return
	}
	evidence := BrowserConversationCancellationEvidence{
		Interrupted:             trigger.Interrupt != nil,
		InvocationID:            invocationID,
		OverlappingAudioSent:    trigger.Interrupt != nil,
		ExplicitCancelAudioSent: trigger.Cancel != nil || cancelStep != nil,
	}
	if trigger.Interrupt != nil {
		evidence.InterruptedStepID = stepID
	}
	if c.run != nil {
		if err := c.run.RecordCancellation(evidence); err != nil && c.tracker != nil {
			c.tracker.setError(err)
		}
	}
}

func (c *browserConversationInterruptionController) nextEligibleInterruption(stepID, toolName string) (*BrowserConversationStep, *BrowserConversationStep, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	currentIndex := browserConversationScenarioStepIndex(c.scenario, stepID)
	for index := range c.scenario.Steps {
		step := c.scenario.Steps[index]
		if step.Interrupt == nil || c.triggered[step.ID] {
			continue
		}
		if currentIndex >= 0 && currentIndex != index && currentIndex+1 != index {
			continue
		}
		if step.Interrupt.ToolName != "" && step.Interrupt.ToolName != toolName {
			continue
		}
		input, ok := c.audio[step.ID]
		if !ok || len(input.PCM) == 0 {
			if c.tracker != nil {
				c.tracker.setError(errors.New("declared interruption has no audio payload"))
			}
			return nil, nil, false
		}
		var cancelStep *BrowserConversationStep
		for later := index + 1; later < len(c.scenario.Steps); later++ {
			if c.scenario.Steps[later].Cancel != nil {
				cancelStep = &c.scenario.Steps[later]
				break
			}
			if c.scenario.Steps[later].Interrupt != nil {
				break
			}
		}
		if cancelStep != nil {
			cancelInput, cancelOK := c.audio[cancelStep.ID]
			if !cancelOK || len(cancelInput.PCM) == 0 {
				if c.tracker != nil {
					c.tracker.setError(errors.New("declared cancellation has no audio payload"))
				}
				return nil, nil, false
			}
		}
		return &c.scenario.Steps[index], cancelStep, true
	}
	for index := range c.scenario.Steps {
		step := c.scenario.Steps[index]
		if step.Cancel == nil || c.triggered[step.ID] {
			continue
		}
		// A cancel directly following an interruption is released by the
		// interruption branch above. Standalone cancel steps are released by
		// the in-flight invocation immediately before them.
		if index > 0 && c.scenario.Steps[index-1].Interrupt != nil {
			continue
		}
		if currentIndex < 0 || currentIndex+1 != index {
			continue
		}
		input, ok := c.audio[step.ID]
		if !ok || len(input.PCM) == 0 {
			if c.tracker != nil {
				c.tracker.setError(errors.New("declared cancellation has no audio payload"))
			}
			return nil, nil, false
		}
		return &c.scenario.Steps[index], nil, true
	}
	return nil, nil, false
}

func (c *browserConversationInterruptionController) enqueueAudio(input ScheduledAudioInput) bool {
	if c == nil || len(input.PCM) == 0 {
		return false
	}
	input.PCM = append([]byte(nil), input.PCM...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.channel <- input:
		return true
	default:
		if c.tracker != nil {
			c.tracker.setError(errors.New("event-driven audio interruption queue is full"))
		}
		return false
	}
}

func (c *browserConversationInterruptionController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.channel)
	}
	c.mu.Unlock()
}

func browserConversationScenarioStepIndex(scenario BrowserConversationScenario, stepID string) int {
	for index, step := range scenario.Steps {
		if step.ID == stepID {
			return index
		}
	}
	return -1
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

// partitionBrowserConversationAudio keeps the ordinary turns on the existing
// completed-turn scheduler and holds interruption/cancel turns until their
// semantic trigger. Their relative order is retained and the ordinary
// scheduler's turn indexes are rebased after the held inputs are removed.
func partitionBrowserConversationAudio(
	scenario BrowserConversationScenario,
	inputs []ScheduledAudioInput,
) ([]ScheduledAudioInput, map[string]ScheduledAudioInput) {
	specialIDs := make(map[string]struct{})
	for index, step := range scenario.Steps {
		if step.Interrupt == nil && step.Cancel == nil {
			continue
		}
		if step.Interrupt != nil {
			specialIDs[step.ID] = struct{}{}
		}
		if step.Cancel != nil {
			specialIDs[step.ID] = struct{}{}
		}
		if step.Interrupt == nil {
			continue
		}
		for later := index + 1; later < len(scenario.Steps); later++ {
			if scenario.Steps[later].Cancel != nil {
				specialIDs[scenario.Steps[later].ID] = struct{}{}
				break
			}
			if scenario.Steps[later].Interrupt != nil {
				break
			}
		}
	}

	normal := make([]ScheduledAudioInput, 0, len(inputs))
	special := make(map[string]ScheduledAudioInput, len(specialIDs))
	for index, input := range inputs {
		stepID := ""
		if index < len(scenario.Steps) {
			stepID = scenario.Steps[index].ID
		}
		if _, held := specialIDs[stepID]; held {
			input.PCM = append([]byte(nil), input.PCM...)
			special[stepID] = input
			continue
		}
		input.AfterCompletedTurns = len(normal)
		input.PCM = append([]byte(nil), input.PCM...)
		normal = append(normal, input)
	}
	return normal, special
}
