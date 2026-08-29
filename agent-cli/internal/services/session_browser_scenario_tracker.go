package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type browserConversationEvidenceTracker struct {
	mu                    sync.Mutex
	run                   *BrowserConversationRun
	scenario              BrowserConversationScenario
	customerAt            int
	currentStepIDValue    string
	assistant             strings.Builder
	firstErr              error
	awaitingAssistant     bool
	context               context.Context
	cancel                context.CancelFunc
	fixture               *BrowserConversationFixtureRun
	navigate              BrowserConversationCustomerNavigateFunc
	cancelInvocation      func(context.Context, webmcp.InvocationID, string) error
	inFlightInvocation    webmcp.InvocationID
	inFlightStepID        string
	cancellationPending   bool
	cancellationFinalized bool
	suppressedLateEvents  int
	deadlineTimer         *time.Timer
	deadlineStop          chan struct{}
	deadlineToken         uint64
	stepChanged           chan struct{}
}

func newBrowserConversationEvidenceTracker(run *BrowserConversationRun, scenario BrowserConversationScenario) *browserConversationEvidenceTracker {
	return &browserConversationEvidenceTracker{
		run:         run,
		scenario:    scenario,
		stepChanged: make(chan struct{}),
	}
}

func (t *browserConversationEvidenceTracker) configure(
	ctx context.Context,
	cancel context.CancelFunc,
	fixture *BrowserConversationFixtureRun,
	navigate BrowserConversationCustomerNavigateFunc,
) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.context = ctx
	t.cancel = cancel
	t.fixture = fixture
	t.navigate = navigate
}

// setCancelInvocation binds the tracker to the already wrapped broker. It is
// installed after broker construction so a spoken cancel can use the exact
// broker/session ownership boundary that recorded the in-flight invocation.
func (t *browserConversationEvidenceTracker) setCancelInvocation(cancel func(context.Context, webmcp.InvocationID, string) error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.cancelInvocation = cancel
	t.mu.Unlock()
}

// noteInFlight records the invocation that the browser broker admitted before
// its terminal wait. The next interrupt/cancel transcript can therefore cancel
// a concrete browser identity rather than guessing from timing.
func (t *browserConversationEvidenceTracker) noteInFlight(stepID string, invocationID webmcp.InvocationID) {
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

// invocationStep waits until the stream observer has recorded the customer
// boundary that owns a provider tool call. Tool execution runs concurrently
// with delta consumption, so reading currentStep immediately at Invoke time
// can otherwise attach a call to the previous turn. The signal is replaced
// for each transcript instead of polling or sleeping.
func (t *browserConversationEvidenceTracker) invocationStep(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("browser conversation evidence tracker is nil")
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
			// the assistant boundary; preserve that existing evidence path when
			// this step has not issued a browser invocation yet. Once a browser
			// call belongs to the completed step, a later call is necessarily
			// racing the next customer transcript and must wait for its boundary.
			if !browserConversationStepHasInvocation(run, stepID) {
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

func browserConversationStepHasInvocation(run *BrowserConversationRun, stepID string) bool {
	if run == nil || stepID == "" {
		return false
	}
	for _, call := range run.Snapshot().BrokerCalls {
		if call.StepID == stepID && call.Operation == BrowserConversationInvoke {
			return true
		}
	}
	return false
}

func (t *browserConversationEvidenceTracker) stopDeadline() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopDeadlineLocked()
}

func (t *browserConversationEvidenceTracker) stopDeadlineLocked() {
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

func (t *browserConversationEvidenceTracker) startDeadlineLocked(step BrowserConversationStep) {
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

func (t *browserConversationEvidenceTracker) expireDeadline(token uint64, stepID string, deadline time.Duration) {
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
		ErrBrowserConversationTimeout,
		fmt.Errorf("step %q exceeded deadline %s", stepID, deadline),
	))
	cancel = t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *browserConversationEvidenceTracker) observe(message messages.StreamMessage) {
	if t == nil {
		return
	}
	var cancelInvocation func(context.Context, webmcp.InvocationID, string) error
	var cancelContext context.Context
	var cancelID webmcp.InvocationID
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
			// A customer interruption is intentionally allowed to arrive while
			// the preceding browser invocation is still awaiting its assistant
			// boundary. The explicit cancel step may likewise follow the
			// interruption without manufacturing an assistant success turn.
			nextStep := t.nextStepLocked()
			if nextStep == nil || (nextStep.Interrupt == nil && nextStep.Cancel == nil) {
				t.setErrorLocked(fmt.Errorf("customer transcript arrived before assistant turn for step %q", t.currentStepIDValue))
				t.mu.Unlock()
				return
			}
			t.awaitingAssistant = false
		}
		if t.customerAt >= len(t.scenario.Steps) {
			t.setErrorLocked(errors.New("received more customer transcripts than scenario steps"))
			t.mu.Unlock()
			return
		}
		step := t.scenario.Steps[t.customerAt]
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
			cancelReason = safeBrowserConversationText(step.Cancel.Reason)
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
	var stopSession context.CancelFunc
	if cancelErr != nil {
		t.mu.Lock()
		t.cancellationPending = false
		t.setErrorLocked(cancelErr)
		t.mu.Unlock()
		return
	}
	if err := t.run.RecordCancellation(BrowserConversationCancellationEvidence{
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

func (t *browserConversationEvidenceTracker) nextStepLocked() *BrowserConversationStep {
	if t.customerAt >= len(t.scenario.Steps) {
		return nil
	}
	return &t.scenario.Steps[t.customerAt]
}

func (t *browserConversationEvidenceTracker) noteLateEventLocked(message messages.StreamMessage) {
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

func (t *browserConversationEvidenceTracker) lateEventCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.suppressedLateEvents
}

func (t *browserConversationEvidenceTracker) navigateStepLocked(step BrowserConversationStep) error {
	if step.Navigation == nil {
		return nil
	}
	navigation := *step.Navigation
	previousGeneration := browserConversationFixtureGeneration(t.fixture)
	var navigationErr error
	if t.navigate == nil {
		navigationErr = errors.New("customer navigation callback is unavailable")
	} else {
		navigationErr = t.navigate(t.context, t.fixture, navigation)
	}
	currentGeneration := browserConversationFixtureGeneration(t.fixture)
	recordErr := t.run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID:             step.ID,
		Operation:          BrowserConversationCustomerNavigate,
		InputJSON:          browserConversationJSON(navigation),
		PreviousGeneration: previousGeneration,
		Generation:         currentGeneration,
		ErrorCode:          browserConversationErrorCode(navigationErr),
	})
	if navigationErr != nil {
		return navigationErr
	}
	return recordErr
}

func (t *browserConversationEvidenceTracker) currentStepID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentStepIDValue
}

func (t *browserConversationEvidenceTracker) currentStep() string {
	return t.currentStepID()
}

func (t *browserConversationEvidenceTracker) setErrorLocked(err error) {
	if t.firstErr == nil && err != nil {
		t.firstErr = errors.Join(ErrBrowserConversationEvidence, err)
		t.signalStepChangedLocked()
	}
}

func (t *browserConversationEvidenceTracker) signalStepChangedLocked() {
	if t.stepChanged == nil {
		t.stepChanged = make(chan struct{})
	}
	close(t.stepChanged)
	t.stepChanged = make(chan struct{})
}

func (t *browserConversationEvidenceTracker) setError(err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setErrorLocked(err)
}

func (t *browserConversationEvidenceTracker) err() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstErr
}
