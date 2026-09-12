package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
)

type evidenceTracker struct {
	mu                    sync.Mutex
	steps                 []browserrunner.StepBoundary
	run                   browserrunner.RunRecorder
	customerAt            int
	currentStepIDValue    string
	assistant             strings.Builder
	firstErr              error
	awaitingAssistant     bool
	context               context.Context
	cancel                context.CancelFunc
	navigation            browserrunner.NavigationPort
	cancelInvocation      browserrunner.CancelInvocationFunc
	inFlightInvocation    string
	inFlightStepID        string
	cancellationPending   bool
	cancellationFinalized bool
	suppressedLateEvents  int
	deadlineTimer         *time.Timer
	deadlineStop          chan struct{}
	deadlineToken         uint64
	stepChanged           chan struct{}
}

func newEvidenceTracker(config browserrunner.EvidenceTrackerConfig) *evidenceTracker {
	return &evidenceTracker{
		steps:       cloneSteps(config.Steps),
		run:         config.Run,
		stepChanged: make(chan struct{}),
	}
}

func (t *evidenceTracker) Configure(ctx context.Context, cancel context.CancelFunc, navigation browserrunner.NavigationPort) {
	if t == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	t.context = ctx
	t.cancel = cancel
	t.navigation = navigation
	t.mu.Unlock()
}

func (t *evidenceTracker) SetCancelInvocation(cancel browserrunner.CancelInvocationFunc) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.cancelInvocation = cancel
	t.mu.Unlock()
}

func (t *evidenceTracker) NoteInFlight(stepID, invocationID string) {
	if t == nil || invocationID == "" {
		return
	}
	t.mu.Lock()
	if !t.cancellationPending && !t.cancellationFinalized {
		t.inFlightStepID = stepID
		t.inFlightInvocation = invocationID
	}
	t.mu.Unlock()
}

func (t *evidenceTracker) InvocationStep(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("browser runner evidence tracker is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		t.mu.Lock()
		if t.firstErr != nil {
			err := t.firstErr
			t.mu.Unlock()
			return "", err
		}
		if t.cancellationPending || t.cancellationFinalized {
			t.mu.Unlock()
			return "", context.Canceled
		}
		if t.currentStepIDValue != "" && t.awaitingAssistant {
			stepID := t.currentStepIDValue
			t.mu.Unlock()
			return stepID, nil
		}
		if t.currentStepIDValue != "" {
			stepID := t.currentStepIDValue
			run := t.run
			changed := t.stepChanged
			t.mu.Unlock()
			// A direct caller may intentionally issue an ungrounded tool after
			// the assistant boundary. Once the completed step already owns a
			// browser invocation, wait for the next customer boundary instead.
			if run == nil || !run.HasInvocation(stepID) {
				return stepID, nil
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			continue
		}
		changed := t.stepChanged
		t.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (t *evidenceTracker) StopDeadline() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.stopDeadlineLocked()
	t.mu.Unlock()
}

func (t *evidenceTracker) stopDeadlineLocked() {
	t.deadlineToken++
	if t.deadlineTimer != nil {
		t.deadlineTimer.Stop()
		t.deadlineTimer = nil
	}
	if t.deadlineStop != nil {
		close(t.deadlineStop)
		t.deadlineStop = nil
	}
}

func (t *evidenceTracker) startDeadlineLocked(step browserrunner.StepBoundary) {
	t.stopDeadlineLocked()
	if step.Deadline <= 0 || t.cancel == nil {
		return
	}
	t.deadlineToken++
	token := t.deadlineToken
	timer := time.NewTimer(step.Deadline)
	stop := make(chan struct{})
	t.deadlineTimer = timer
	t.deadlineStop = stop
	go func() {
		select {
		case <-timer.C:
			t.expireDeadline(token, step.ID, step.Deadline)
		case <-stop:
		}
	}()
}

func (t *evidenceTracker) expireDeadline(token uint64, stepID string, deadline time.Duration) {
	if t == nil {
		return
	}
	var cancel context.CancelFunc
	t.mu.Lock()
	if token != t.deadlineToken || !t.awaitingAssistant || t.firstErr != nil {
		t.mu.Unlock()
		return
	}
	t.setErrorLocked(errors.Join(
		browserrunner.ErrTimeout,
		fmt.Errorf("step %q exceeded deadline %s", stepID, deadline),
	))
	cancel = t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *evidenceTracker) Observe(message messages.StreamMessage) {
	if t == nil {
		return
	}
	var cancelInvocation browserrunner.CancelInvocationFunc
	var cancelContext context.Context
	var cancelID string
	var cancelReason string
	var cancelStepID string
	t.mu.Lock()
	if t.firstErr != nil {
		t.mu.Unlock()
		return
	}
	if t.cancellationPending || t.cancellationFinalized {
		t.noteLateEventLocked(message)
		t.mu.Unlock()
		return
	}
	switch message.Type {
	case messages.StreamTypeTranscriptEnd:
		value, ok := message.Value.(*messages.TranscriptEndValue)
		if !ok || value == nil {
			t.setErrorLocked(errors.New("transcript end has malformed typed evidence"))
			t.mu.Unlock()
			return
		}
		if strings.TrimSpace(value.FullText) == "" {
			t.setErrorLocked(errors.New("transcript end has empty customer text"))
			t.mu.Unlock()
			return
		}
		if t.awaitingAssistant {
			// A customer interruption may arrive while the preceding browser
			// invocation is still waiting for its assistant boundary.
			nextStep := t.nextStepLocked()
			if nextStep == nil || (nextStep.Interrupt == nil && nextStep.Cancel == nil) {
				t.setErrorLocked(fmt.Errorf("customer transcript arrived before assistant turn for step %q", t.currentStepIDValue))
				t.mu.Unlock()
				return
			}
			t.awaitingAssistant = false
		}
		if t.customerAt >= len(t.steps) {
			t.setErrorLocked(errors.New("received more customer transcripts than scenario steps"))
			t.mu.Unlock()
			return
		}
		step := t.steps[t.customerAt]
		if t.run == nil {
			t.setErrorLocked(errors.New("browser runner evidence recorder is unavailable"))
			t.mu.Unlock()
			return
		}
		if err := t.run.ObserveCustomerTurn(step.ID, value.FullText); err != nil {
			t.setErrorLocked(err)
			t.mu.Unlock()
			return
		}
		t.currentStepIDValue = step.ID
		t.customerAt++
		t.awaitingAssistant = true
		t.signalStepChangedLocked()
		if step.Navigation != nil {
			if err := t.navigateStepLocked(step); err != nil {
				t.setErrorLocked(err)
				t.mu.Unlock()
				return
			}
		}
		t.startDeadlineLocked(step)
		if step.Cancel != nil {
			cancelInvocation = t.cancelInvocation
			cancelContext = t.context
			cancelID = t.inFlightInvocation
			cancelReason = step.Cancel.Reason
			cancelStepID = step.ID
			if cancelInvocation == nil {
				t.setErrorLocked(errors.New("explicit cancellation callback is unavailable"))
			} else if cancelID == "" {
				t.setErrorLocked(errors.New("explicit cancellation has no in-flight invocation"))
			} else {
				t.cancellationPending = true
			}
		}
	case messages.StreamTypeMessageStart:
		t.assistant.Reset()
	case messages.StreamTypeTextDelta:
		value, ok := message.Value.(*messages.TextDeltaValue)
		if !ok || value == nil {
			t.setErrorLocked(errors.New("text delta has malformed typed evidence"))
			t.mu.Unlock()
			return
		}
		if message.Role != messages.RoleTool && message.Role != messages.RoleUser {
			t.assistant.WriteString(value.Content)
		}
	case messages.StreamTypeMessageEnd:
		if message.Role == messages.RoleTool || message.Role == messages.RoleUser {
			t.mu.Unlock()
			return
		}
		text := strings.TrimSpace(t.assistant.String())
		if text == "" {
			t.mu.Unlock()
			return
		}
		if t.currentStepIDValue == "" {
			t.setErrorLocked(errors.New("assistant turn arrived before customer transcript"))
			t.mu.Unlock()
			return
		}
		if !t.awaitingAssistant {
			t.setErrorLocked(fmt.Errorf("assistant turn arrived out of order for step %q", t.currentStepIDValue))
			t.mu.Unlock()
			return
		}
		if t.run == nil {
			t.setErrorLocked(errors.New("browser runner evidence recorder is unavailable"))
			t.mu.Unlock()
			return
		}
		if err := t.run.ObserveAssistantTurn(t.currentStepIDValue, text); err != nil {
			t.setErrorLocked(err)
			t.mu.Unlock()
			return
		}
		t.awaitingAssistant = false
		t.stopDeadlineLocked()
		t.assistant.Reset()
	}
	t.mu.Unlock()

	if cancelInvocation == nil || cancelID == "" {
		return
	}
	if cancelContext == nil {
		cancelContext = context.Background()
	}
	cancelErr := cancelInvocation(cancelContext, cancelID, cancelReason)
	if cancelErr != nil {
		t.mu.Lock()
		t.cancellationPending = false
		t.setErrorLocked(cancelErr)
		t.mu.Unlock()
		return
	}
	if t.run == nil {
		t.mu.Lock()
		t.cancellationPending = false
		t.setErrorLocked(errors.New("browser runner evidence recorder is unavailable"))
		t.mu.Unlock()
		return
	}
	if err := t.run.RecordCancellation(browserrunner.CancellationObservation{
		Requested:    true,
		InvocationID: cancelID,
		CancelStepID: cancelStepID,
		Reason:       cancelReason,
	}); err != nil {
		t.mu.Lock()
		t.cancellationPending = false
		t.setErrorLocked(err)
		t.mu.Unlock()
		return
	}
	var stopSession context.CancelFunc
	t.mu.Lock()
	t.cancellationPending = false
	t.cancellationFinalized = true
	t.stopDeadlineLocked()
	stopSession = t.cancel
	t.mu.Unlock()
	if stopSession != nil {
		stopSession()
	}
}

func (t *evidenceTracker) nextStepLocked() *browserrunner.StepBoundary {
	if t.customerAt >= len(t.steps) {
		return nil
	}
	return &t.steps[t.customerAt]
}

func (t *evidenceTracker) noteLateEventLocked(message messages.StreamMessage) {
	switch message.Type {
	case messages.StreamTypeTranscriptEnd,
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeToolCallStart:
		t.suppressedLateEvents++
	}
}

func (t *evidenceTracker) LateEventCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.suppressedLateEvents
}

func (t *evidenceTracker) navigateStepLocked(step browserrunner.StepBoundary) error {
	if step.Navigation == nil {
		return nil
	}
	previousGeneration := uint64(0)
	currentGeneration := uint64(0)
	var navigationErr error
	if t.navigation == nil {
		navigationErr = errors.New("customer navigation callback is unavailable")
	} else {
		previousGeneration = t.navigation.Generation()
		navigationErr = t.navigation.Navigate(t.context, *step.Navigation)
		currentGeneration = t.navigation.Generation()
	}
	if t.run == nil {
		return errors.New("browser runner evidence recorder is unavailable")
	}
	recordErr := t.run.RecordNavigation(browserrunner.NavigationObservation{
		StepID:             step.ID,
		Navigation:         *step.Navigation,
		PreviousGeneration: previousGeneration,
		Generation:         currentGeneration,
		Error:              navigationErr,
	})
	if navigationErr != nil {
		return navigationErr
	}
	return recordErr
}

func (t *evidenceTracker) CurrentStep() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentStepIDValue
}

func (t *evidenceTracker) setErrorLocked(err error) {
	if t.firstErr == nil && err != nil {
		t.firstErr = errors.Join(browserrunner.ErrEvidence, err)
		t.signalStepChangedLocked()
	}
}

func (t *evidenceTracker) signalStepChangedLocked() {
	if t.stepChanged == nil {
		t.stepChanged = make(chan struct{})
	}
	close(t.stepChanged)
	t.stepChanged = make(chan struct{})
}

func (t *evidenceTracker) SetError(err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.setErrorLocked(err)
	t.mu.Unlock()
}

func (t *evidenceTracker) Err() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstErr
}

func cloneSteps(steps []browserrunner.StepBoundary) []browserrunner.StepBoundary {
	if steps == nil {
		return nil
	}
	clone := make([]browserrunner.StepBoundary, len(steps))
	for index, step := range steps {
		clone[index] = step
		if step.Navigation != nil {
			navigation := *step.Navigation
			clone[index].Navigation = &navigation
		}
		if step.Interrupt != nil {
			interrupt := *step.Interrupt
			clone[index].Interrupt = &interrupt
		}
		if step.Cancel != nil {
			cancel := *step.Cancel
			clone[index].Cancel = &cancel
		}
	}
	return clone
}

var _ browserrunner.EvidenceTracker = (*evidenceTracker)(nil)
