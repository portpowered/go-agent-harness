package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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
	done := contextDone(ctx)
	for {
		stepID, ready, run, changed, err := t.invocationState()
		if err != nil {
			return "", err
		}
		if ready || (stepID != "" && (run == nil || !run.HasInvocation(stepID))) {
			return stepID, nil
		}
		if err := waitForStepChange(changed, done, ctx); err != nil {
			return "", err
		}
	}
}

func (t *evidenceTracker) invocationState() (string, bool, browserrunner.RunRecorder, <-chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstErr != nil {
		return "", false, nil, nil, t.firstErr
	}
	if t.cancellationPending || t.cancellationFinalized {
		return "", false, nil, nil, context.Canceled
	}
	if t.currentStepIDValue == "" {
		return "", false, nil, t.stepChanged, nil
	}
	if t.awaitingAssistant {
		return t.currentStepIDValue, true, nil, nil, nil
	}
	return t.currentStepIDValue, false, t.run, t.stepChanged, nil
}

func waitForStepChange(changed, done <-chan struct{}, ctx context.Context) error {
	select {
	case <-changed:
		return nil
	case <-done:
		return ctx.Err()
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
		navigationContext := t.context
		if navigationContext == nil {
			navigationContext = context.Background()
		}
		navigationErr = t.navigation.Navigate(navigationContext, *step.Navigation)
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

func contextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
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
