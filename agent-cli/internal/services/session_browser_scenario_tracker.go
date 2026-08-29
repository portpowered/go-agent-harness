package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type browserConversationEvidenceTracker struct {
	mu                 sync.Mutex
	run                *BrowserConversationRun
	scenario           BrowserConversationScenario
	customerAt         int
	currentStepIDValue string
	assistant          strings.Builder
	firstErr           error
	awaitingAssistant  bool
	context            context.Context
	cancel             context.CancelFunc
	fixture            *BrowserConversationFixtureRun
	navigate           BrowserConversationCustomerNavigateFunc
	deadlineTimer      *time.Timer
	deadlineStop       chan struct{}
	deadlineToken      uint64
}

func newBrowserConversationEvidenceTracker(run *BrowserConversationRun, scenario BrowserConversationScenario) *browserConversationEvidenceTracker {
	return &browserConversationEvidenceTracker{run: run, scenario: scenario}
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
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.firstErr != nil {
		return
	}
	switch message.Type {
	case messages.StreamTypeTranscriptEnd:
		value, ok := message.Value.(*messages.TranscriptEndValue)
		if !ok || value == nil {
			t.setErrorLocked(errors.New("transcript end has malformed typed evidence"))
			return
		}
		if strings.TrimSpace(value.FullText) == "" {
			t.setErrorLocked(errors.New("transcript end has empty customer text"))
			return
		}
		if t.awaitingAssistant {
			t.setErrorLocked(fmt.Errorf("customer transcript arrived before assistant turn for step %q", t.currentStepIDValue))
			return
		}
		if t.customerAt >= len(t.scenario.Steps) {
			t.setErrorLocked(errors.New("received more customer transcripts than scenario steps"))
			return
		}
		step := t.scenario.Steps[t.customerAt]
		if err := t.run.ObserveCustomerTurn(step.ID, value.FullText); err != nil {
			t.setErrorLocked(err)
			return
		}
		t.currentStepIDValue = step.ID
		t.customerAt++
		t.awaitingAssistant = true
		if step.Navigation != nil {
			if err := t.navigateStepLocked(step); err != nil {
				t.setErrorLocked(err)
				return
			}
		}
		t.startDeadlineLocked(step)
	case messages.StreamTypeMessageStart:
		t.assistant.Reset()
	case messages.StreamTypeTextDelta:
		value, ok := message.Value.(*messages.TextDeltaValue)
		if !ok || value == nil {
			t.setErrorLocked(errors.New("text delta has malformed typed evidence"))
			return
		}
		if message.Role != messages.RoleTool && message.Role != messages.RoleUser {
			t.assistant.WriteString(value.Content)
		}
	case messages.StreamTypeMessageEnd:
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
		step := browserConversationStepByID(t.scenario, t.currentStepIDValue)
		if step != nil && browserConversationExpectedState(step) != nil && !browserConversationCompletedInvokeForStep(t.run.Snapshot().BrokerCalls, step.ID) {
			t.setErrorLocked(fmt.Errorf("assistant turn arrived before completed browser invocation for step %q", step.ID))
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
	}
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
