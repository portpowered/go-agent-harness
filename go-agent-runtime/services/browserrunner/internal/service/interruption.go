package service

import (
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
)

type interruptionController struct {
	mu        sync.Mutex
	active    bool
	steps     []browserrunner.StepBoundary
	run       browserrunner.RunRecorder
	errorSink browserrunner.ErrorSink
	audio     map[string]browserrunner.AudioInput
	channel   chan browserrunner.AudioInput
	triggered map[string]bool
	closed    bool
}

func newInterruptionController(config browserrunner.InterruptionControllerConfig) *interruptionController {
	controlCount := 0
	for _, step := range config.Steps {
		if step.Interrupt != nil || step.Cancel != nil {
			controlCount++
		}
	}
	if controlCount == 0 {
		return &interruptionController{}
	}
	capacity := config.QueueCapacity
	if capacity <= 0 {
		capacity = len(config.Steps)
	}
	if capacity > len(config.Steps) {
		capacity = len(config.Steps)
	}
	if capacity < 1 {
		capacity = 1
	}
	return &interruptionController{
		active:    true,
		steps:     cloneSteps(config.Steps),
		run:       config.Run,
		errorSink: config.ErrorSink,
		audio:     cloneAudio(config.Audio),
		channel:   make(chan browserrunner.AudioInput, capacity),
		triggered: make(map[string]bool),
	}
}

func (c *interruptionController) Active() bool {
	return c != nil && c.active
}

func (c *interruptionController) AudioInterruptions() <-chan browserrunner.AudioInput {
	if c == nil {
		return nil
	}
	return c.channel
}

func (c *interruptionController) ObserveInFlight(stepID, invocationID, toolName string) {
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
		// The explicit cancel input follows the overlap input on the shared
		// loop, preserving the customer's utterance order.
		cancelSent = c.enqueueAudio(c.audio[cancelStep.ID])
	}
	if !triggerSent || !cancelSent {
		return
	}
	evidence := browserrunner.CancellationObservation{
		Interrupted:             trigger.Interrupt != nil,
		InvocationID:            invocationID,
		OverlappingAudioSent:    trigger.Interrupt != nil,
		ExplicitCancelAudioSent: trigger.Cancel != nil || cancelStep != nil,
	}
	if trigger.Interrupt != nil {
		evidence.InterruptedStepID = stepID
	}
	if c.run != nil {
		if err := c.run.RecordCancellation(evidence); err != nil {
			c.setError(err)
		}
	}
}

func (c *interruptionController) nextEligibleInterruption(stepID, toolName string) (*browserrunner.StepBoundary, *browserrunner.StepBoundary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	currentIndex := stepIndex(c.steps, stepID)
	if trigger, cancel := c.findInterruptionLocked(currentIndex, toolName); trigger != nil {
		return trigger, cancel, true
	}
	if cancel := c.findStandaloneCancelLocked(currentIndex); cancel != nil {
		return cancel, nil, true
	}
	return nil, nil, false
}

func (c *interruptionController) findInterruptionLocked(currentIndex int, toolName string) (*browserrunner.StepBoundary, *browserrunner.StepBoundary) {
	for index := range c.steps {
		step := &c.steps[index]
		if !c.isEligibleInterruption(*step, index, currentIndex, toolName) {
			continue
		}
		if !c.hasAudio(step.ID) {
			c.setErrorLocked(errors.New("declared interruption has no audio payload"))
			return nil, nil
		}
		cancelStep := c.followingCancelLocked(index)
		if cancelStep != nil && !c.hasAudio(cancelStep.ID) {
			c.setErrorLocked(errors.New("declared cancellation has no audio payload"))
			return nil, nil
		}
		return step, cancelStep
	}
	return nil, nil
}

func (c *interruptionController) findStandaloneCancelLocked(currentIndex int) *browserrunner.StepBoundary {
	for index := range c.steps {
		step := &c.steps[index]
		if !c.isEligibleCancel(*step, index, currentIndex) {
			continue
		}
		if !c.hasAudio(step.ID) {
			c.setErrorLocked(errors.New("declared cancellation has no audio payload"))
			return nil
		}
		return step
	}
	return nil
}

func (c *interruptionController) isEligibleInterruption(step browserrunner.StepBoundary, index, currentIndex int, toolName string) bool {
	if step.Interrupt == nil || c.triggered[step.ID] {
		return false
	}
	if currentIndex >= 0 && currentIndex != index && currentIndex+1 != index {
		return false
	}
	return step.Interrupt.ToolName == "" || step.Interrupt.ToolName == toolName
}

func (c *interruptionController) isEligibleCancel(step browserrunner.StepBoundary, index, currentIndex int) bool {
	if step.Cancel == nil || c.triggered[step.ID] || currentIndex < 0 || currentIndex+1 != index {
		return false
	}
	return index == 0 || c.steps[index-1].Interrupt == nil
}

func (c *interruptionController) followingCancelLocked(index int) *browserrunner.StepBoundary {
	for later := index + 1; later < len(c.steps); later++ {
		if c.steps[later].Cancel != nil {
			return &c.steps[later]
		}
		if c.steps[later].Interrupt != nil {
			return nil
		}
	}
	return nil
}

func (c *interruptionController) hasAudio(stepID string) bool {
	input, ok := c.audio[stepID]
	return ok && len(input.PCM) > 0
}

func (c *interruptionController) enqueueAudio(input browserrunner.AudioInput) bool {
	if c == nil || !c.active || len(input.PCM) == 0 {
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
		c.setErrorLocked(browserrunner.ErrInterruptionQueueFull)
		return false
	}
}

func (c *interruptionController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.active && !c.closed {
		c.closed = true
		close(c.channel)
	}
	c.mu.Unlock()
}

func (c *interruptionController) setError(err error) {
	if c == nil || c.errorSink == nil || err == nil {
		return
	}
	c.errorSink.SetError(err)
}

func (c *interruptionController) setErrorLocked(err error) {
	if c == nil || c.errorSink == nil || err == nil {
		return
	}
	c.errorSink.SetError(err)
}

func stepIndex(steps []browserrunner.StepBoundary, stepID string) int {
	for index, step := range steps {
		if step.ID == stepID {
			return index
		}
	}
	return -1
}

func cloneAudio(audio map[string]browserrunner.AudioInput) map[string]browserrunner.AudioInput {
	if audio == nil {
		return nil
	}
	clone := make(map[string]browserrunner.AudioInput, len(audio))
	for stepID, input := range audio {
		input.PCM = append([]byte(nil), input.PCM...)
		clone[stepID] = input
	}
	return clone
}

var _ browserrunner.InterruptionController = (*interruptionController)(nil)
