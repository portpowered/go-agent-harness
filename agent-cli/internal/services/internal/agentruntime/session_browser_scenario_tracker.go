package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
	browserrunnerwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner/wire"
)

// browserConversationEvidenceTracker is the CLI adapter for the runtime
// tracker. Scenario and durable-result types remain host-owned; the runtime
// receives only the narrow behavior projection and recorder port.
type browserConversationEvidenceTracker struct {
	// These fields are retained as a compatibility seam for the recovery test's
	// direct deadline probe. Normal tracking state lives in delegate.
	mu                 sync.Mutex
	currentStepIDValue string
	awaitingAssistant  bool
	deadlineToken      uint64
	context            context.Context
	cancel             context.CancelFunc
	delegate           browserrunner.EvidenceTracker
}

func newBrowserConversationEvidenceTracker(run *BrowserConversationRun, scenario BrowserConversationScenario) *browserConversationEvidenceTracker {
	return &browserConversationEvidenceTracker{
		delegate: browserrunnerwire.NewEvidenceTracker(browserrunner.EvidenceTrackerConfig{
			Steps: browserConversationTrackingSteps(scenario),
			Run:   &browserConversationRunRecorder{run: run},
		}),
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
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	t.context = ctx
	t.cancel = cancel
	t.mu.Unlock()
	if t.delegate != nil {
		t.delegate.Configure(ctx, cancel, &browserConversationNavigationAdapter{
			fixture:  fixture,
			navigate: navigate,
		})
	}
}

// setCancelInvocation binds the tracker to the already wrapped broker. It is
// installed after broker construction so a spoken cancel uses the exact
// broker/session ownership boundary that recorded the in-flight invocation.
func (t *browserConversationEvidenceTracker) setCancelInvocation(cancel func(context.Context, webmcp.InvocationID, string) error) {
	if t == nil || t.delegate == nil {
		return
	}
	if cancel == nil {
		t.delegate.SetCancelInvocation(nil)
		return
	}
	t.delegate.SetCancelInvocation(func(ctx context.Context, invocationID, reason string) error {
		return cancel(ctx, webmcp.InvocationID(invocationID), reason)
	})
}

// noteInFlight records the invocation admitted before its terminal wait.
func (t *browserConversationEvidenceTracker) noteInFlight(stepID string, invocationID webmcp.InvocationID) {
	if t == nil || t.delegate == nil || invocationID == "" {
		return
	}
	t.delegate.NoteInFlight(stepID, string(invocationID))
}

// invocationStep waits for the stream observer to record the customer
// boundary that owns a provider tool call.
func (t *browserConversationEvidenceTracker) invocationStep(ctx context.Context) (string, error) {
	if t == nil || t.delegate == nil {
		return "", errors.New("browser conversation evidence tracker is nil")
	}
	return t.delegate.InvocationStep(ctx)
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
	t.deadlineToken++
	t.mu.Unlock()
	if t.delegate != nil {
		t.delegate.StopDeadline()
	}
}

// expireDeadline remains a small compatibility hook for the recovery test.
// Production deadline ownership is in the runtime tracker.
func (t *browserConversationEvidenceTracker) expireDeadline(token uint64, stepID string, deadline time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if token != t.deadlineToken || !t.awaitingAssistant {
		t.mu.Unlock()
		return
	}
	cancel := t.cancel
	t.mu.Unlock()
	if t.delegate != nil {
		t.delegate.SetError(errors.Join(
			ErrBrowserConversationTimeout,
			fmt.Errorf("step %q exceeded deadline %s", stepID, deadline),
		))
	}
	if cancel != nil {
		cancel()
	}
}

func (t *browserConversationEvidenceTracker) observe(message messages.StreamMessage) {
	if t == nil || t.delegate == nil {
		return
	}
	t.delegate.Observe(message)
}

func (t *browserConversationEvidenceTracker) lateEventCount() int {
	if t == nil || t.delegate == nil {
		return 0
	}
	return t.delegate.LateEventCount()
}

func (t *browserConversationEvidenceTracker) currentStepID() string {
	if t == nil {
		return ""
	}
	if t.delegate != nil {
		if stepID := t.delegate.CurrentStep(); stepID != "" {
			return stepID
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentStepIDValue
}

func (t *browserConversationEvidenceTracker) currentStep() string {
	return t.currentStepID()
}

func (t *browserConversationEvidenceTracker) setError(err error) {
	if t == nil || t.delegate == nil {
		return
	}
	t.delegate.SetError(err)
}

// SetError exposes the narrow runtime error sink to the interruption adapter.
func (t *browserConversationEvidenceTracker) SetError(err error) {
	t.setError(err)
}

func (t *browserConversationEvidenceTracker) err() error {
	if t == nil || t.delegate == nil {
		return nil
	}
	err := t.delegate.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, browserrunner.ErrTimeout) {
		return errors.Join(ErrBrowserConversationTimeout, err)
	}
	if errors.Is(err, browserrunner.ErrEvidence) {
		return errors.Join(ErrBrowserConversationEvidence, err)
	}
	return err
}

type browserConversationRunRecorder struct {
	run *BrowserConversationRun
}

func (r *browserConversationRunRecorder) ObserveCustomerTurn(stepID, observed string) error {
	if r == nil || r.run == nil {
		return errors.New("browser conversation run is nil")
	}
	return r.run.ObserveCustomerTurn(stepID, observed)
}

func (r *browserConversationRunRecorder) ObserveAssistantTurn(stepID, observed string) error {
	if r == nil || r.run == nil {
		return errors.New("browser conversation run is nil")
	}
	return r.run.ObserveAssistantTurn(stepID, observed)
}

func (r *browserConversationRunRecorder) RecordNavigation(observation browserrunner.NavigationObservation) error {
	if r == nil || r.run == nil {
		return errors.New("browser conversation run is nil")
	}
	navigation := BrowserCustomerNavigation{
		FromPageID: observation.Navigation.FromPageID,
		ToPageID:   observation.Navigation.ToPageID,
		URL:        observation.Navigation.URL,
	}
	return r.run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID:             observation.StepID,
		Operation:          BrowserConversationCustomerNavigate,
		InputJSON:          browserConversationJSON(navigation),
		PreviousGeneration: observation.PreviousGeneration,
		Generation:         observation.Generation,
		ErrorCode:          browserConversationErrorCode(observation.Error),
	})
}

func (r *browserConversationRunRecorder) RecordCancellation(observation browserrunner.CancellationObservation) error {
	if r == nil || r.run == nil {
		return errors.New("browser conversation run is nil")
	}
	return r.run.RecordCancellation(BrowserConversationCancellationEvidence{
		Interrupted:             observation.Interrupted,
		Requested:               observation.Requested,
		InvocationID:            webmcp.InvocationID(observation.InvocationID),
		FinalState:              webmcp.InvocationState(observation.FinalState),
		Reason:                  observation.Reason,
		InterruptedStepID:       observation.InterruptedStepID,
		CancelStepID:            observation.CancelStepID,
		OverlappingAudioSent:    observation.OverlappingAudioSent,
		ExplicitCancelAudioSent: observation.ExplicitCancelAudioSent,
		LateEventsSuppressed:    observation.LateEventsSuppressed,
	})
}

func (r *browserConversationRunRecorder) HasInvocation(stepID string) bool {
	if r == nil {
		return false
	}
	return browserConversationStepHasInvocation(r.run, stepID)
}

type browserConversationNavigationAdapter struct {
	fixture  *BrowserConversationFixtureRun
	navigate BrowserConversationCustomerNavigateFunc
}

func (a *browserConversationNavigationAdapter) Navigate(ctx context.Context, navigation browserrunner.Navigation) error {
	if a == nil || a.navigate == nil {
		return errors.New("customer navigation callback is unavailable")
	}
	return a.navigate(ctx, a.fixture, BrowserCustomerNavigation{
		FromPageID: navigation.FromPageID,
		ToPageID:   navigation.ToPageID,
		URL:        navigation.URL,
	})
}

func (a *browserConversationNavigationAdapter) Generation() uint64 {
	if a == nil {
		return 0
	}
	return browserConversationFixtureGeneration(a.fixture)
}

func browserConversationTrackingSteps(scenario BrowserConversationScenario) []browserrunner.StepBoundary {
	steps := make([]browserrunner.StepBoundary, len(scenario.Steps))
	for index, step := range scenario.Steps {
		steps[index] = browserrunner.StepBoundary{ID: step.ID, Deadline: step.Deadline}
		if step.Navigation != nil {
			navigation := browserrunner.Navigation{
				FromPageID: step.Navigation.FromPageID,
				ToPageID:   step.Navigation.ToPageID,
				URL:        step.Navigation.URL,
			}
			steps[index].Navigation = &navigation
		}
		if step.Interrupt != nil {
			interrupt := browserrunner.InterruptionBoundary{
				Trigger:  string(step.Interrupt.Trigger),
				ToolName: step.Interrupt.ToolName,
			}
			steps[index].Interrupt = &interrupt
		}
		if step.Cancel != nil {
			cancel := browserrunner.CancellationBoundary{Reason: safeBrowserConversationText(step.Cancel.Reason)}
			steps[index].Cancel = &cancel
		}
	}
	return steps
}
