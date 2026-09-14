package service

import (
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

// interruptionController releases declared customer audio only after the
// broker has admitted an in-flight invocation. It is deliberately stateful
// only for one run and owns no browser or provider resource.
type interruptionController struct {
	mu        sync.Mutex
	run       *browserconversation.BrowserConversationRun
	tracker   *evidenceTracker
	scenario  browserconversation.BrowserConversationScenario
	audio     map[string]browserconversation.ScheduledAudioInput
	channel   chan browserconversation.ScheduledAudioInput
	triggered map[string]bool
	closed    bool
}

func newInterruptionController(run *browserconversation.BrowserConversationRun, tracker *evidenceTracker, scenario browserconversation.BrowserConversationScenario, audio map[string]browserconversation.ScheduledAudioInput) *interruptionController {
	count := 0
	for _, step := range scenario.Steps {
		if step.Interrupt != nil || step.Cancel != nil {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	return &interruptionController{run: run, tracker: tracker, scenario: scenario, audio: cloneAudioMap(audio), channel: make(chan browserconversation.ScheduledAudioInput, len(scenario.Steps)), triggered: make(map[string]bool)}
}

func (c *interruptionController) Inputs() <-chan browserconversation.ScheduledAudioInput {
	if c == nil {
		return nil
	}
	return c.channel
}

func (c *interruptionController) observeInFlight(stepID, invocationID, toolName string) {
	if c == nil || invocationID == "" {
		return
	}
	trigger, cancelStep, ok := c.next(stepID, toolName)
	if !ok {
		return
	}
	c.mu.Lock()
	if c.closed || c.triggered[trigger.ID] {
		c.mu.Unlock()
		return
	}
	c.triggered[trigger.ID] = true
	c.mu.Unlock()
	if !c.enqueue(c.audio[trigger.ID]) {
		return
	}
	cancelSent := true
	if cancelStep != nil {
		cancelSent = c.enqueue(c.audio[cancelStep.ID])
	}
	if !cancelSent || c.run == nil {
		return
	}
	evidence := browserconversation.BrowserConversationCancellationEvidence{
		Interrupted: true, InvocationID: invocationID, OverlappingAudioSent: true,
		ExplicitCancelAudioSent: cancelStep != nil, InterruptedStepID: stepID,
	}
	if err := c.run.RecordCancellation(evidence); err != nil && c.tracker != nil {
		c.tracker.setError(err)
	}
}

func (c *interruptionController) next(stepID, toolName string) (*browserconversation.BrowserConversationStep, *browserconversation.BrowserConversationStep, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := stepIndex(c.scenario, stepID)
	if step, cancel, ok := c.findInterruptLocked(current, toolName); ok {
		return step, cancel, true
	}
	return c.findCancelLocked(current)
}

func (c *interruptionController) findInterruptLocked(current int, toolName string) (*browserconversation.BrowserConversationStep, *browserconversation.BrowserConversationStep, bool) {
	for index := range c.scenario.Steps {
		step := &c.scenario.Steps[index]
		if step.Interrupt == nil || c.triggered[step.ID] || (current >= 0 && current != index && current+1 != index) {
			continue
		}
		if step.Interrupt.ToolName != "" && step.Interrupt.ToolName != toolName {
			continue
		}
		if !c.hasAudioLocked(step.ID, "declared interruption has no audio payload") {
			return nil, nil, false
		}
		cancel := c.cancelAfterLocked(index)
		if cancel != nil && !c.hasAudioLocked(cancel.ID, "declared cancellation has no audio payload") {
			return nil, nil, false
		}
		return step, cancel, true
	}
	return nil, nil, false
}

func (c *interruptionController) findCancelLocked(current int) (*browserconversation.BrowserConversationStep, *browserconversation.BrowserConversationStep, bool) {
	for index := range c.scenario.Steps {
		step := &c.scenario.Steps[index]
		if step.Cancel == nil || c.triggered[step.ID] || (index > 0 && c.scenario.Steps[index-1].Interrupt != nil) || current < 0 || current+1 != index {
			continue
		}
		if !c.hasAudioLocked(step.ID, "declared cancellation has no audio payload") {
			return nil, nil, false
		}
		return step, nil, true
	}
	return nil, nil, false
}

func (c *interruptionController) cancelAfterLocked(index int) *browserconversation.BrowserConversationStep {
	for later := index + 1; later < len(c.scenario.Steps); later++ {
		if c.scenario.Steps[later].Cancel != nil {
			return &c.scenario.Steps[later]
		}
		if c.scenario.Steps[later].Interrupt != nil {
			break
		}
	}
	return nil
}

func (c *interruptionController) hasAudioLocked(stepID, message string) bool {
	input, ok := c.audio[stepID]
	if ok && len(input.PCM) > 0 {
		return true
	}
	if c.tracker != nil {
		c.tracker.setError(errors.New(message))
	}
	return false
}

func (c *interruptionController) enqueue(input browserconversation.ScheduledAudioInput) bool {
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

func (c *interruptionController) close() {
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

func stepIndex(scenario browserconversation.BrowserConversationScenario, id string) int {
	for index, step := range scenario.Steps {
		if step.ID == id {
			return index
		}
	}
	return -1
}

func cloneAudioMap(audio map[string]browserconversation.ScheduledAudioInput) map[string]browserconversation.ScheduledAudioInput {
	if audio == nil {
		return nil
	}
	clone := make(map[string]browserconversation.ScheduledAudioInput, len(audio))
	for id, input := range audio {
		input.PCM = append([]byte(nil), input.PCM...)
		clone[id] = input
	}
	return clone
}

func partitionAudio(scenario browserconversation.BrowserConversationScenario, inputs []browserconversation.ScheduledAudioInput) ([]browserconversation.ScheduledAudioInput, map[string]browserconversation.ScheduledAudioInput) {
	special := interruptionStepIDs(scenario)
	normal := make([]browserconversation.ScheduledAudioInput, 0, len(inputs))
	held := make(map[string]browserconversation.ScheduledAudioInput, len(special))
	completed := 0
	for index, input := range inputs {
		if index < len(scenario.Steps) {
			stepID := scenario.Steps[index].ID
			if _, ok := special[stepID]; ok {
				held[stepID] = input
				continue
			}
		}
		input.AfterCompletedTurns = completed
		normal = append(normal, input)
		completed++
	}
	return normal, held
}

func interruptionStepIDs(scenario browserconversation.BrowserConversationScenario) map[string]struct{} {
	special := make(map[string]struct{})
	for index, step := range scenario.Steps {
		if step.Interrupt != nil {
			special[step.ID] = struct{}{}
			addFollowingCancellationID(scenario, index, special)
		}
		if step.Cancel != nil {
			special[step.ID] = struct{}{}
		}
	}
	return special
}

func addFollowingCancellationID(scenario browserconversation.BrowserConversationScenario, index int, special map[string]struct{}) {
	for later := index + 1; later < len(scenario.Steps); later++ {
		if scenario.Steps[later].Cancel != nil {
			special[scenario.Steps[later].ID] = struct{}{}
			return
		}
		if scenario.Steps[later].Interrupt != nil {
			return
		}
	}
}
