package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func browserConversationStepByID(scenario BrowserConversationScenario, stepID string) *BrowserConversationStep {
	for index := range scenario.Steps {
		if scenario.Steps[index].ID == stepID {
			return &scenario.Steps[index]
		}
	}
	return nil
}

func browserConversationExpectedState(step *BrowserConversationStep) *BrowserStateTransition {
	if step == nil {
		return nil
	}
	if step.ExpectedState != nil {
		return step.ExpectedState
	}
	if step.Correction != nil {
		return &step.Correction.ExpectedState
	}
	return nil
}

func browserConversationTurnsForStep(turns []BrowserConversationTurn, stepID string) (*BrowserConversationTurn, *BrowserConversationTurn) {
	var customer, assistant *BrowserConversationTurn
	for index := range turns {
		if turns[index].StepID != stepID {
			continue
		}
		switch turns[index].Direction {
		case BrowserConversationCustomerTurn:
			if customer == nil {
				customer = &turns[index]
			}
		case BrowserConversationAssistantTurn:
			if assistant == nil {
				assistant = &turns[index]
			}
		}
	}
	return customer, assistant
}

func browserConversationOracleForStep(oracles []BrowserConversationOracleSnapshot, stepID string, phase BrowserConversationOraclePhase) *BrowserConversationOracleSnapshot {
	var match *BrowserConversationOracleSnapshot
	for index := range oracles {
		if oracles[index].StepID == stepID && oracles[index].Phase == phase {
			match = &oracles[index]
		}
	}
	return match
}

func browserConversationTerminalInvokeForStep(calls []BrowserConversationBrokerCall, stepID string) *BrowserConversationBrokerCall {
	for _, call := range calls {
		if call.StepID == stepID &&
			call.Operation == BrowserConversationInvoke &&
			call.Terminal &&
			browserConversationOpaqueString(call.State) == browserConversationInvocationCompleted &&
			call.ErrorCode == "" {
			candidate := call
			return &candidate
		}
	}
	return nil
}

func browserConversationJSONEqual(left, right json.RawMessage) bool {
	leftValue, leftOK := decodeBrowserConversationJSON(left)
	rightValue, rightOK := decodeBrowserConversationJSON(right)
	return leftOK && rightOK && reflect.DeepEqual(leftValue, rightValue)
}

func decodeBrowserConversationJSON(raw json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return value, true
}

func safeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return safeBrowserConversationText(err.Error())
}

func (t *evidenceTracker) readyInvocationStepLocked() string {
	if t.currentStep != "" && t.awaitingAssistant {
		return t.currentStep
	}
	return ""
}

type browserConversationExecution struct {
	scenario    browserconversation.BrowserConversationScenario
	normalAudio []browserconversation.ScheduledAudioInput
	run         browserconversation.Run
	runContext  context.Context
	cancel      context.CancelFunc
	tracker     *evidenceTracker
	interrupter *interruptionController
	fixture     browserconversation.Fixture
	lifecycle   browserconversation.BrowserConversationLifecycleEvidence
	rootErr     error
}

func newBrowserConversationExecution(ctx context.Context, request browserconversation.RunRequest) (*browserConversationExecution, error) {
	scenario, err := admitScenario(request.Scenario)
	if err != nil {
		return nil, err
	}
	audio, err := scheduleAudioInputs(scenario, request.AudioByStep)
	if err != nil {
		return nil, err
	}
	normalAudio, heldAudio := partitionAudio(scenario, audio)
	run := newBrowserConversationRun(scenario)
	runContext, cancel := context.WithTimeout(ctx, scenario.RunTimeout)
	tracker := newEvidenceTracker(run, scenario)
	return &browserConversationExecution{
		scenario: scenario, normalAudio: normalAudio, run: run,
		runContext: runContext, cancel: cancel, tracker: tracker,
		interrupter: newInterruptionController(run, tracker, scenario, heldAudio),
		lifecycle:   browserconversation.BrowserConversationLifecycleEvidence{Outcome: browserconversation.BrowserConversationLifecycleNotStarted},
	}, nil
}

func (e *browserConversationExecution) add(err error) {
	if e != nil && err != nil {
		e.rootErr = errors.Join(e.rootErr, err)
	}
}

func (e *browserConversationExecution) start(request browserconversation.RunRequest) {
	if e == nil {
		return
	}
	if request.Fixture != nil {
		e.fixture = request.Fixture
	} else if request.FixtureFactory == nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationFixtureStartup, errors.New("fixture factory is required")))
	} else {
		fixture, err := request.FixtureFactory(e.runContext, e.scenario)
		e.fixture = fixture
		if err != nil {
			e.add(errors.Join(browserconversation.ErrBrowserConversationFixtureStartup, err))
		}
	}
	if e.fixture != nil && request.Broker == nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationFixtureStartup, errors.New("browser broker is required")))
	}
	if e.fixture != nil && request.Broker != nil && e.rootErr == nil {
		e.startSession(request)
	}
}

func (e *browserConversationExecution) startSession(request browserconversation.RunRequest) {
	navigate := request.CustomerNavigate
	if navigate == nil {
		navigate = defaultCustomerNavigate
	}
	e.tracker.configure(e.runContext, e.cancel, e.fixture, navigate)
	observed := newEvidenceBroker(request.Broker, e.run, e.tracker, e.scenario, request.Oracle, e.fixture, e.interrupter)
	e.tracker.setCancelInvocation(func(ctx context.Context, invocationID, reason string) error {
		return observed.Cancel(ctx, browserconversation.BrowserCancelRequest{InvocationID: invocationID, Reason: reason})
	})
	if err := prepareFixture(e.runContext, e.scenario, observed); err != nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationFixtureStartup, err))
		return
	}
	if request.SessionRunner == nil {
		e.add(browserconversation.ErrBrowserConversationSessionBoundaryRequired)
		return
	}
	e.lifecycle.SessionStarted = true
	sessionErr := request.SessionRunner(e.runContext, sessionOutput(request), browserconversation.SessionRequest{
		Scenario: e.scenario, Fixture: e.fixture, Broker: observed,
		ToolExecutor: request.ToolExecutor, ToolDefinitions: append([]messages.ToolDefinition(nil), request.ToolDefinitions...),
		AudioInputs: cloneAudioInputs(e.normalAudio), AudioInterruptions: interruptionInputs(e.interrupter),
		StreamObserver: e.tracker.observe, CustomerNavigate: navigate,
	})
	e.lifecycle.SessionTerminated = true
	if sessionErr != nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationSession, sessionErr))
	}
}

func sessionOutput(request browserconversation.RunRequest) io.Writer {
	if request.Output != nil {
		return request.Output
	}
	return io.Discard
}

func interruptionInputs(interrupter *interruptionController) <-chan browserconversation.ScheduledAudioInput {
	if interrupter == nil {
		return nil
	}
	return interrupter.Inputs()
}

func defaultCustomerNavigate(ctx context.Context, fixture browserconversation.Fixture, navigation browserconversation.BrowserCustomerNavigation) error {
	return fixture.Navigate(ctx, navigation)
}

func (e *browserConversationExecution) cleanup(ctx context.Context, request browserconversation.RunRequest) {
	if e == nil {
		return
	}
	e.add(e.tracker.err())
	if count := e.tracker.lateEventCount(); count > 0 {
		if err := e.run.RecordCancellation(browserconversation.BrowserConversationCancellationEvidence{LateEventsSuppressed: count}); err != nil {
			e.add(err)
		}
	}
	if e.fixture != nil {
		e.cleanupFixture(ctx, request)
	}
	e.add(e.tracker.err())
	e.lifecycle.Outcome = lifecycleOutcome(e.rootErr, e.runContext.Err())
	e.add(e.run.RecordLifecycle(e.lifecycle))
}

func (e *browserConversationExecution) cleanupFixture(ctx context.Context, request browserconversation.RunRequest) {
	if closeErr := e.fixture.Close(); closeErr != nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationCleanup, closeErr))
	}
	pageID := e.scenario.PostSession.PageID
	probe := request.PostSessionProbe
	if probe == nil {
		probe = func(ctx context.Context, fixture browserconversation.Fixture, pageID string) (browserconversation.BrowserConversationTabStateProbeResult, error) {
			return fixture.ProbeTab(ctx, pageID)
		}
	}
	cleanupContext := context.WithoutCancel(ctx)
	if health, err := probe(cleanupContext, e.fixture, pageID); err != nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationCleanup, err))
	} else {
		e.lifecycle.ExternalBrowserID, e.lifecycle.ExternalTargetID = health.BrowserID, health.TargetID
		e.lifecycle.ExternalTabAlive, e.lifecycle.ExternalTabResponsive = health.Alive, health.Responsive
		e.lifecycle.ExternalTabAllowsMutation, e.lifecycle.ExternalTabRead = health.AllowsMutation, health.ReadSucceeded
		e.lifecycle.ExternalTabMutation = health.MutationSucceeded
	}
	if request.Oracle != nil {
		state, err := request.Oracle.ReadState(cleanupContext, pageID)
		if err != nil {
			e.add(errors.Join(browserconversation.ErrBrowserConversationCleanup, err))
		} else {
			e.add(e.run.ObserveOracleSnapshot(browserconversation.BrowserConversationOracleSnapshot{PageID: pageID, Phase: browserconversation.BrowserConversationOraclePostSession, State: state}))
		}
	}
	e.lifecycle.Detached, e.lifecycle.DetachRequired, e.lifecycle.DetachCount = true, true, 1
}

func (e *browserConversationExecution) finish(request browserconversation.RunRequest) (browserconversation.BrowserConversationResult, error) {
	e.recordDerivedEvidence()
	e.recordEvaluation()
	e.recordValidator(request.Validator)
	result, err := e.run.Finalize()
	e.add(err)
	e.close()
	return result, e.rootErr
}

func (e *browserConversationExecution) recordDerivedEvidence() {
	snapshot := e.run.Snapshot()
	e.add(e.run.RecordCorrections(deriveBrowserConversationCorrections(e.scenario, snapshot)))
	e.add(e.run.RecordRecovery(deriveBrowserConversationRecovery(e.scenario, snapshot)))
}

func (e *browserConversationExecution) recordEvaluation() {
	evaluation, err := EvaluateBrowserConversation(e.scenario, e.run.Snapshot(), e.rootErr)
	if err != nil {
		e.add(err)
		return
	}
	e.add(e.run.RecordMechanicalEvaluation(evaluation))
}

func (e *browserConversationExecution) recordValidator(validator browserconversation.BrowserConversationValidator) {
	if validator == nil {
		return
	}
	verdict, err := validator.ValidateBrowserConversation(e.run.Snapshot())
	if err != nil {
		e.add(errors.Join(browserconversation.ErrBrowserConversationValidator, err))
		return
	}
	e.add(e.run.RecordValidator(verdict))
}

func (e *browserConversationExecution) close() {
	if e == nil {
		return
	}
	if e.interrupter != nil {
		e.interrupter.close()
	}
	if e.cancel != nil {
		e.cancel()
	}
}
