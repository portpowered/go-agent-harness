package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

type evidenceTracker struct {
	mu                    sync.Mutex
	run                   *browserconversation.BrowserConversationRun
	scenario              browserconversation.BrowserConversationScenario
	customerAt            int
	currentStep           string
	assistant             strings.Builder
	firstErr              error
	awaitingAssistant     bool
	ctx                   context.Context
	cancel                context.CancelFunc
	fixture               browserconversation.Fixture
	navigate              browserconversation.CustomerNavigateFunc
	cancelInvocation      func(context.Context, string, string) error
	inFlightInvocation    string
	cancellationPending   bool
	cancellationFinalized bool
	suppressedLateEvents  int
	deadlineTimer         *time.Timer
	deadlineToken         uint64
	stepChanged           chan struct{}
}

func newEvidenceTracker(run *browserconversation.BrowserConversationRun, scenario browserconversation.BrowserConversationScenario) *evidenceTracker {
	return &evidenceTracker{run: run, scenario: scenario, stepChanged: make(chan struct{})}
}

func (t *evidenceTracker) configure(ctx context.Context, cancel context.CancelFunc, fixture browserconversation.Fixture, navigate browserconversation.CustomerNavigateFunc) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ctx, t.cancel, t.fixture, t.navigate = ctx, cancel, fixture, navigate
	t.mu.Unlock()
}

func (t *evidenceTracker) setCancelInvocation(cancel func(context.Context, string, string) error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.cancelInvocation = cancel
	t.mu.Unlock()
}

func (t *evidenceTracker) noteInFlight(invocationID string) {
	if t == nil || invocationID == "" {
		return
	}
	t.mu.Lock()
	if !t.cancellationPending && !t.cancellationFinalized {
		t.inFlightInvocation = invocationID
	}
	t.mu.Unlock()
}

func (t *evidenceTracker) invocationStep(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("browser conversation evidence tracker is nil")
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
		if step := t.readyInvocationStepLocked(); step != "" {
			t.mu.Unlock()
			return step, nil
		}
		step, changed := t.currentStep, t.stepChanged
		allowWithoutBoundary := step != "" && !t.stepHasInvocationLocked(step)
		t.mu.Unlock()
		if allowWithoutBoundary {
			return step, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (t *evidenceTracker) stepHasInvocationLocked(stepID string) bool {
	if t.run == nil {
		return false
	}
	for _, call := range t.run.Snapshot().BrokerCalls {
		if call.StepID == stepID && call.Operation == browserconversation.BrowserConversationInvoke {
			return true
		}
	}
	return false
}

func (t *evidenceTracker) suppress(message messages.StreamMessage) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstErr != nil {
		return true
	}
	if t.cancellationPending || t.cancellationFinalized {
		t.noteLateEventLocked(message)
		return true
	}
	return false
}

func (t *evidenceTracker) observeCustomerTurn(message messages.StreamMessage) {
	value, ok := message.Value.(*messages.TranscriptEndValue)
	if !ok || value == nil || strings.TrimSpace(value.FullText) == "" {
		t.setError(errors.New("transcript end has malformed or empty customer text"))
		return
	}
	step, err := t.acceptCustomerTurn(value.FullText)
	if err != nil {
		t.setError(err)
		return
	}
	if err := t.navigateCustomerTurn(step); err != nil {
		t.setError(err)
		return
	}
	t.mu.Lock()
	t.startDeadlineLocked(step)
	t.mu.Unlock()
	t.executeCancellation(t.cancellationForStep(step))
}

func (t *evidenceTracker) acceptCustomerTurn(observed string) (browserconversation.BrowserConversationStep, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.awaitingAssistant {
		next := t.nextStepLocked()
		if next == nil || (next.Interrupt == nil && next.Cancel == nil) {
			return browserconversation.BrowserConversationStep{}, fmt.Errorf("customer transcript arrived before assistant turn for step %q", t.currentStep)
		}
		t.awaitingAssistant = false
	}
	if t.customerAt >= len(t.scenario.Steps) {
		return browserconversation.BrowserConversationStep{}, errors.New("received more customer transcripts than scenario steps")
	}
	step := t.scenario.Steps[t.customerAt]
	if err := t.run.ObserveCustomerTurn(step.ID, observed); err != nil {
		return browserconversation.BrowserConversationStep{}, err
	}
	t.currentStep, t.customerAt, t.awaitingAssistant = step.ID, t.customerAt+1, true
	t.signalStepChangedLocked()
	return step, nil
}

func (t *evidenceTracker) navigateCustomerTurn(step browserconversation.BrowserConversationStep) error {
	if step.Navigation == nil {
		return nil
	}
	t.mu.Lock()
	navigate, ctx, fixture := t.navigate, t.ctx, t.fixture
	t.mu.Unlock()
	if navigate == nil {
		return errors.New("customer navigation callback is unavailable")
	}
	navigationErr := navigate(ctx, fixture, *step.Navigation)
	if err := t.run.ObserveBrokerCall(browserconversation.BrowserConversationBrokerCall{StepID: step.ID, Operation: browserconversation.BrowserConversationCustomerNavigate, InputJSON: fmt.Sprintf(`{"to_page_id":%q,"url":%q}`, step.Navigation.ToPageID, step.Navigation.URL), ErrorCode: safeErrorCode(navigationErr)}); err != nil {
		return err
	}
	return navigationErr
}

type browserConversationCancelAction struct {
	cancel func(context.Context, string, string) error
	ctx    context.Context
	id     string
	reason string
	step   string
}

func (t *evidenceTracker) cancellationForStep(step browserconversation.BrowserConversationStep) browserConversationCancelAction {
	if step.Cancel == nil {
		return browserConversationCancelAction{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	action := browserConversationCancelAction{cancel: t.cancelInvocation, ctx: t.ctx, id: t.inFlightInvocation, reason: safeBrowserConversationText(step.Cancel.Reason), step: step.ID}
	if action.cancel == nil {
		t.setErrorLocked(errors.New("explicit cancellation callback is unavailable"))
	} else if action.id == "" {
		t.setErrorLocked(errors.New("explicit cancellation has no in-flight invocation"))
	} else {
		t.cancellationPending = true
	}
	return action
}

func (t *evidenceTracker) executeCancellation(action browserConversationCancelAction) {
	if action.cancel == nil || action.id == "" {
		return
	}
	if action.ctx == nil {
		action.ctx = context.Background()
	}
	if err := action.cancel(action.ctx, action.id, action.reason); err != nil {
		t.setError(err)
		return
	}
	if err := t.run.ObserveInvocationPublication("cancel", action.id, browserConversationInvocationCanceled, true); err != nil {
		t.setError(err)
		return
	}
	if err := t.run.RecordCancellation(browserconversation.BrowserConversationCancellationEvidence{Requested: true, InvocationID: action.id, CancelStepID: action.step, Reason: action.reason}); err != nil {
		t.setError(err)
		return
	}
	t.mu.Lock()
	t.cancellationPending, t.cancellationFinalized = false, true
	t.stopDeadlineLocked()
	stop := t.cancel
	t.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (t *evidenceTracker) beginAssistantMessage() {
	t.mu.Lock()
	t.assistant.Reset()
	t.mu.Unlock()
}

func (t *evidenceTracker) observeTextDelta(message messages.StreamMessage) {
	value, ok := message.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		t.setError(errors.New("text delta has malformed typed evidence"))
		return
	}
	if message.Role == messages.RoleTool || message.Role == messages.RoleUser {
		return
	}
	t.mu.Lock()
	t.assistant.WriteString(value.Content)
	t.mu.Unlock()
}

func (t *evidenceTracker) observeAssistantTurn(message messages.StreamMessage) {
	if message.Role == messages.RoleTool || message.Role == messages.RoleUser {
		return
	}
	t.mu.Lock()
	text := strings.TrimSpace(t.assistant.String())
	if text == "" {
		t.mu.Unlock()
		return
	}
	if t.currentStep == "" || !t.awaitingAssistant {
		t.setErrorLocked(errors.New("assistant turn arrived out of order"))
		t.mu.Unlock()
		return
	}
	if err := t.run.ObserveAssistantTurn(t.currentStep, text); err != nil {
		t.setErrorLocked(err)
		t.mu.Unlock()
		return
	}
	t.awaitingAssistant = false
	t.stopDeadlineLocked()
	t.assistant.Reset()
	t.mu.Unlock()
}

func (t *evidenceTracker) nextStepLocked() *browserconversation.BrowserConversationStep {
	if t.customerAt >= len(t.scenario.Steps) {
		return nil
	}
	return &t.scenario.Steps[t.customerAt]
}

func (t *evidenceTracker) lateEventCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.suppressedLateEvents
}
func (t *evidenceTracker) currentStepID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentStep
}
func (t *evidenceTracker) setError(err error) {
	if t == nil || err == nil {
		return
	}
	t.mu.Lock()
	t.setErrorLocked(err)
	t.mu.Unlock()
}
func (t *evidenceTracker) err() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstErr
}

func (t *evidenceTracker) startDeadlineLocked(step browserconversation.BrowserConversationStep) {
	t.stopDeadlineLocked()
	if step.Deadline <= 0 || t.cancel == nil {
		return
	}
	t.deadlineToken++
	token := t.deadlineToken
	timer := time.NewTimer(step.Deadline)
	t.deadlineTimer = timer
	go func() { <-timer.C; t.expireDeadline(token, step.ID, step.Deadline) }()
}

func (t *evidenceTracker) expireDeadline(token uint64, stepID string, deadline time.Duration) {
	t.mu.Lock()
	if token != t.deadlineToken || !t.awaitingAssistant || t.firstErr != nil {
		t.mu.Unlock()
		return
	}
	t.setErrorLocked(fmt.Errorf("step %q exceeded deadline %s", stepID, deadline))
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (t *evidenceTracker) stopDeadlineLocked() {
	t.deadlineToken++
	if t.deadlineTimer != nil {
		t.deadlineTimer.Stop()
		t.deadlineTimer = nil
	}
}
func (t *evidenceTracker) signalStepChangedLocked() {
	close(t.stepChanged)
	t.stepChanged = make(chan struct{})
}
func (t *evidenceTracker) setErrorLocked(err error) {
	if t.firstErr == nil && err != nil {
		t.firstErr = errors.Join(browserconversation.ErrBrowserConversationEvidence, err)
		t.signalStepChangedLocked()
	}
}
