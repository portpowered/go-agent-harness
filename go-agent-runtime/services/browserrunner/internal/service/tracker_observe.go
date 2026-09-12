package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
)

type cancellationRequest struct {
	invoke  browserrunner.CancelInvocationFunc
	context context.Context
	id      string
	reason  string
	stepID  string
}

func (t *evidenceTracker) Observe(message messages.StreamMessage) {
	if t == nil {
		return
	}
	request := t.observeLocked(message)
	if request != nil {
		t.completeCancellation(request)
	}
}

func (t *evidenceTracker) observeLocked(message messages.StreamMessage) *cancellationRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstErr != nil {
		return nil
	}
	if t.cancellationPending || t.cancellationFinalized {
		t.noteLateEventLocked(message)
		return nil
	}
	switch message.Type { //nolint:exhaustive // unrelated provider events are intentionally ignored
	case messages.StreamTypeTranscriptEnd:
		return t.observeTranscriptEndLocked(message)
	case messages.StreamTypeMessageStart:
		t.assistant.Reset()
	case messages.StreamTypeTextDelta:
		t.observeTextDeltaLocked(message)
	case messages.StreamTypeMessageEnd:
		t.observeMessageEndLocked(message)
	default:
		// Other stream messages do not change the browser turn boundary.
	}
	return nil
}

func (t *evidenceTracker) observeTranscriptEndLocked(message messages.StreamMessage) *cancellationRequest {
	value, ok := message.Value.(*messages.TranscriptEndValue)
	if !ok || value == nil {
		t.setErrorLocked(errors.New("transcript end has malformed typed evidence"))
		return nil
	}
	if strings.TrimSpace(value.FullText) == "" {
		t.setErrorLocked(errors.New("transcript end has empty customer text"))
		return nil
	}
	if t.awaitingAssistant {
		nextStep := t.nextStepLocked()
		if nextStep == nil || (nextStep.Interrupt == nil && nextStep.Cancel == nil) {
			t.setErrorLocked(fmt.Errorf("customer transcript arrived before assistant turn for step %q", t.currentStepIDValue))
			return nil
		}
		t.awaitingAssistant = false
	}
	if t.customerAt >= len(t.steps) {
		t.setErrorLocked(errors.New("received more customer transcripts than scenario steps"))
		return nil
	}
	step := t.steps[t.customerAt]
	if t.run == nil {
		t.setErrorLocked(errors.New("browser runner evidence recorder is unavailable"))
		return nil
	}
	if err := t.run.ObserveCustomerTurn(step.ID, value.FullText); err != nil {
		t.setErrorLocked(err)
		return nil
	}
	t.currentStepIDValue = step.ID
	t.customerAt++
	t.awaitingAssistant = true
	t.signalStepChangedLocked()
	if err := t.navigateStepLocked(step); err != nil {
		t.setErrorLocked(err)
		return nil
	}
	t.startDeadlineLocked(step)
	return t.prepareCancellationLocked(step)
}

func (t *evidenceTracker) prepareCancellationLocked(step browserrunner.StepBoundary) *cancellationRequest {
	if step.Cancel == nil {
		return nil
	}
	if t.cancelInvocation == nil {
		t.setErrorLocked(errors.New("explicit cancellation callback is unavailable"))
		return nil
	}
	if t.inFlightInvocation == "" {
		t.setErrorLocked(errors.New("explicit cancellation has no in-flight invocation"))
		return nil
	}
	t.cancellationPending = true
	return &cancellationRequest{
		invoke:  t.cancelInvocation,
		context: t.context,
		id:      t.inFlightInvocation,
		reason:  step.Cancel.Reason,
		stepID:  step.ID,
	}
}

func (t *evidenceTracker) observeTextDeltaLocked(message messages.StreamMessage) {
	value, ok := message.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		t.setErrorLocked(errors.New("text delta has malformed typed evidence"))
		return
	}
	if message.Role != messages.RoleTool && message.Role != messages.RoleUser {
		t.assistant.WriteString(value.Content)
	}
}

func (t *evidenceTracker) observeMessageEndLocked(message messages.StreamMessage) {
	if message.Role == messages.RoleTool || message.Role == messages.RoleUser {
		return
	}
	text := strings.TrimSpace(t.assistant.String())
	if text == "" {
		return
	}
	if t.currentStepIDValue == "" {
		t.setErrorLocked(errors.New("assistant turn arrived before customer transcript"))
		return
	}
	if !t.awaitingAssistant {
		t.setErrorLocked(fmt.Errorf("assistant turn arrived out of order for step %q", t.currentStepIDValue))
		return
	}
	if t.run == nil {
		t.setErrorLocked(errors.New("browser runner evidence recorder is unavailable"))
		return
	}
	if err := t.run.ObserveAssistantTurn(t.currentStepIDValue, text); err != nil {
		t.setErrorLocked(err)
		return
	}
	t.awaitingAssistant = false
	t.stopDeadlineLocked()
	t.assistant.Reset()
}

func (t *evidenceTracker) completeCancellation(request *cancellationRequest) {
	cancelContext := request.context
	if cancelContext == nil {
		cancelContext = context.Background()
	}
	if err := request.invoke(cancelContext, request.id, request.reason); err != nil {
		t.failCancellation(err)
		return
	}
	if t.run == nil {
		t.failCancellation(errors.New("browser runner evidence recorder is unavailable"))
		return
	}
	if err := t.run.RecordCancellation(browserrunner.CancellationObservation{
		Requested:    true,
		InvocationID: request.id,
		CancelStepID: request.stepID,
		Reason:       request.reason,
	}); err != nil {
		t.failCancellation(err)
		return
	}
	t.mu.Lock()
	t.cancellationPending = false
	t.cancellationFinalized = true
	t.stopDeadlineLocked()
	stopSession := t.cancel
	t.mu.Unlock()
	if stopSession != nil {
		stopSession()
	}
}

func (t *evidenceTracker) failCancellation(err error) {
	t.mu.Lock()
	t.cancellationPending = false
	t.setErrorLocked(err)
	t.mu.Unlock()
}

func (t *evidenceTracker) nextStepLocked() *browserrunner.StepBoundary {
	if t.customerAt >= len(t.steps) {
		return nil
	}
	return &t.steps[t.customerAt]
}

func (t *evidenceTracker) noteLateEventLocked(message messages.StreamMessage) {
	switch message.Type { //nolint:exhaustive // unrelated late events are intentionally ignored
	case messages.StreamTypeTranscriptEnd,
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeToolCallStart:
		t.suppressedLateEvents++
	default:
		// Unrelated late events do not represent a browser turn boundary.
	}
}
