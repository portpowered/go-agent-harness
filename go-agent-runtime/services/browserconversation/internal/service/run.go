package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

// runBrowserConversation coordinates the service-owned vertical. The detailed
// phases live beside this entrypoint so each boundary remains independently
// auditable and within the repository's complexity budgets.
func runBrowserConversation(ctx context.Context, request browserconversation.RunRequest) (browserconversation.BrowserConversationResult, error) {
	execution, err := newBrowserConversationExecution(ctx, request)
	if err != nil {
		return browserconversation.BrowserConversationResult{}, err
	}
	defer execution.close()
	execution.start(request)
	execution.cleanup(ctx, request)
	return execution.finish(request)
}

type evidenceBroker struct {
	inner       browserconversation.Broker
	run         *browserconversation.BrowserConversationRun
	tracker     *evidenceTracker
	scenario    browserconversation.BrowserConversationScenario
	oracle      browserconversation.OracleReader
	fixture     browserconversation.Fixture
	interrupter *interruptionController
	mu          sync.Mutex
	catalog     map[string]browserconversation.BrowserToolDescriptor
}

func newEvidenceBroker(inner browserconversation.Broker, run *browserconversation.BrowserConversationRun, tracker *evidenceTracker, scenario browserconversation.BrowserConversationScenario, oracle browserconversation.OracleReader, fixture browserconversation.Fixture, interrupter *interruptionController) *evidenceBroker {
	return &evidenceBroker{inner: inner, run: run, tracker: tracker, scenario: scenario, oracle: oracle, fixture: fixture, interrupter: interrupter, catalog: make(map[string]browserconversation.BrowserToolDescriptor)}
}

func (b *evidenceBroker) Discover(ctx context.Context, options browserconversation.BrowserDiscoveryOptions) ([]browserconversation.BrowserCandidate, error) {
	return b.inner.Discover(ctx, options)
}

func (b *evidenceBroker) ListTargets(ctx context.Context, selector browserconversation.BrowserSelector) ([]browserconversation.BrowserTarget, error) {
	return b.inner.ListTargets(ctx, selector)
}

func (b *evidenceBroker) Select(ctx context.Context, selector browserconversation.BrowserTargetSelector) (browserconversation.BrowserPageContext, error) {
	result, err := b.inner.Select(ctx, selector)
	b.record(browserconversation.BrowserConversationBrokerCall{StepID: b.step(), Operation: browserconversation.BrowserConversationSelectPage, InputJSON: encodeJSON(selector), Generation: result.Generation, ErrorCode: browserErrorCode(err)})
	return result, err
}

func (b *evidenceBroker) Selected(ctx context.Context) (browserconversation.BrowserPageContext, error) {
	result, err := b.inner.Selected(ctx)
	b.record(browserconversation.BrowserConversationBrokerCall{StepID: b.step(), Operation: browserconversation.BrowserConversationWaitReady, Generation: result.Generation, ErrorCode: browserErrorCode(err)})
	return result, err
}

func (b *evidenceBroker) ListTools(ctx context.Context, options browserconversation.BrowserListToolsOptions) (browserconversation.BrowserToolCatalog, error) {
	result, err := b.inner.ListTools(ctx, options)
	if err == nil {
		b.mu.Lock()
		for _, descriptor := range result.Tools {
			b.catalog[descriptor.Ref] = cloneTool(descriptor)
		}
		b.mu.Unlock()
	}
	refs := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		refs = append(refs, tool.Ref)
	}
	b.record(browserconversation.BrowserConversationBrokerCall{StepID: b.step(), Operation: browserconversation.BrowserConversationListTools, InputJSON: encodeJSON(options), Generation: result.Generation, ToolRefs: refs, Output: encodeRaw(result), ErrorCode: browserErrorCode(err)})
	return result, err
}

func (b *evidenceBroker) Invoke(ctx context.Context, request browserconversation.BrowserInvokeRequest) (browserconversation.BrowserInvokeResult, error) {
	stepID, err := b.invocationStep(ctx)
	if err != nil {
		return browserconversation.BrowserInvokeResult{State: "error"}, err
	}
	step := scenarioStep(b.scenario, stepID)
	if step != nil && expectedState(step) != nil {
		b.observeOracle(ctx, step, browserconversation.BrowserConversationOracleBefore)
	}
	descriptor := b.tool(request.ToolRef)
	result, err := b.inner.Invoke(ctx, request)
	if err != nil {
		b.recordInvocationError(stepID, request, descriptor, err)
		return result, err
	}
	b.record(invokeCall(stepID, request, result, descriptor))
	if isTerminal(result.State) || result.InvocationID == "" {
		b.observeImmediateInvocationOracle(ctx, step, result)
		return result, nil
	}
	if b.tracker != nil {
		b.tracker.noteInFlight(result.InvocationID)
	}
	if b.interrupter != nil {
		b.interrupter.observeInFlight(stepID, result.InvocationID, descriptor.Name)
	}
	result, err = b.waitInvocation(ctx, stepID, request, result, descriptor)
	if err != nil {
		return result, err
	}
	if step != nil && result.State == browserConversationInvocationCompleted && isTerminal(result.State) && expectedState(step) != nil {
		b.observeOracle(ctx, step, browserconversation.BrowserConversationOracleAfter)
	}
	return result, nil
}

func (b *evidenceBroker) observeImmediateInvocationOracle(ctx context.Context, step *browserconversation.BrowserConversationStep, result browserconversation.BrowserInvokeResult) {
	if step != nil && result.State == browserConversationInvocationCompleted && expectedState(step) != nil {
		b.observeOracle(ctx, step, browserconversation.BrowserConversationOracleAfter)
	}
}

func (b *evidenceBroker) invocationStep(ctx context.Context) (string, error) {
	if b == nil || b.tracker == nil {
		return b.step(), nil
	}
	return b.tracker.invocationStep(ctx)
}

func (b *evidenceBroker) tool(ref string) browserconversation.BrowserToolDescriptor {
	if b == nil {
		return browserconversation.BrowserToolDescriptor{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneTool(b.catalog[ref])
}

func (b *evidenceBroker) recordInvocationError(stepID string, request browserconversation.BrowserInvokeRequest, descriptor browserconversation.BrowserToolDescriptor, err error) {
	b.record(browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationInvoke, ToolRef: request.ToolRef, ToolName: descriptor.Name, InputJSON: string(request.Input), State: "error", Terminal: true, Generation: descriptor.Generation, ErrorCode: browserErrorCode(err)})
}

func (b *evidenceBroker) waitInvocation(ctx context.Context, stepID string, request browserconversation.BrowserInvokeRequest, result browserconversation.BrowserInvokeResult, descriptor browserconversation.BrowserToolDescriptor) (browserconversation.BrowserInvokeResult, error) {
	waiter, ok := b.inner.(browserconversation.InvocationWaiter)
	if !ok {
		return result, nil
	}
	terminal, err := waiter.WaitInvocation(ctx, result.InvocationID)
	if err != nil {
		if b.tracker != nil {
			b.tracker.setError(err)
		}
		return result, err
	}
	b.record(invokeCall(stepID, request, terminal, descriptor))
	return terminal, nil
}

func (b *evidenceBroker) Cancel(ctx context.Context, request browserconversation.BrowserCancelRequest) error {
	err := b.inner.Cancel(ctx, request)
	b.record(browserconversation.BrowserConversationBrokerCall{StepID: b.step(), Operation: browserconversation.BrowserConversationCancel, InputJSON: encodeJSON(request), InvocationID: request.InvocationID, State: "canceled", ErrorCode: browserErrorCode(err)})
	if err == nil {
		if recordErr := b.run.RecordCancellation(browserconversation.BrowserConversationCancellationEvidence{Requested: true, InvocationID: request.InvocationID, Reason: safeText(request.Reason)}); recordErr != nil && b.tracker != nil {
			b.tracker.setError(recordErr)
		}
	}
	return err
}

func (b *evidenceBroker) Watch(ctx context.Context) <-chan browserconversation.BrowserEvent {
	return b.inner.Watch(ctx)
}
func (b *evidenceBroker) Close() error { return b.inner.Close() }

func (b *evidenceBroker) observeOracle(ctx context.Context, step *browserconversation.BrowserConversationStep, phase browserconversation.BrowserConversationOraclePhase) {
	if b == nil || b.run == nil || step == nil || expectedState(step) == nil {
		return
	}
	reader := b.oracle
	if reader == nil {
		reader = b.fixture
	}
	if reader == nil {
		if b.tracker != nil {
			b.tracker.setError(errors.New("expected-state oracle is unavailable"))
		}
		return
	}
	transition := expectedState(step)
	state, err := reader.ReadState(ctx, transition.PageID)
	if err != nil {
		if b.tracker != nil {
			b.tracker.setError(err)
		}
		return
	}
	if err := b.run.ObserveOracleSnapshot(browserconversation.BrowserConversationOracleSnapshot{StepID: step.ID, PageID: transition.PageID, Phase: phase, State: state, Generation: b.generation(ctx)}); err != nil && b.tracker != nil {
		b.tracker.setError(err)
	}
}

func (b *evidenceBroker) generation(ctx context.Context) uint64 {
	if b == nil {
		return 0
	}
	selected, err := b.inner.Selected(ctx)
	if err != nil {
		return 0
	}
	return selected.Generation
}

func (b *evidenceBroker) step() string {
	if b == nil || b.tracker == nil {
		return ""
	}
	return b.tracker.currentStepID()
}

func (b *evidenceBroker) record(call browserconversation.BrowserConversationBrokerCall) {
	if b == nil || b.run == nil {
		return
	}
	if err := b.run.ObserveBrokerCall(call); err != nil && b.tracker != nil {
		b.tracker.setError(err)
	}
}

func invokeCall(stepID string, request browserconversation.BrowserInvokeRequest, result browserconversation.BrowserInvokeResult, descriptor browserconversation.BrowserToolDescriptor) browserconversation.BrowserConversationBrokerCall {
	return browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationInvoke, ToolRef: request.ToolRef, ToolName: descriptor.Name, InvocationID: result.InvocationID, InputJSON: string(request.Input), State: result.State, Terminal: isTerminal(result.State), Output: append([]byte(nil), result.Output...), ErrorCode: result.ErrorCode, Generation: descriptor.Generation}
}

func prepareFixture(ctx context.Context, scenario browserconversation.BrowserConversationScenario, broker browserconversation.Broker) error {
	candidates, err := broker.Discover(ctx, browserconversation.BrowserDiscoveryOptions{ExplicitOnly: true})
	if err != nil {
		return err
	}
	if len(candidates) != 1 {
		return fmt.Errorf("fixture discovery returned %d candidates, want one", len(candidates))
	}
	targets, err := broker.ListTargets(ctx, browserconversation.BrowserSelector{BrowserID: candidates[0].ID})
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return errors.New("fixture discovery returned no target")
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	targetID := targets[0].ID
	for _, target := range targets {
		if target.ID == scenario.Fixture.InitialPage {
			targetID = target.ID
			break
		}
	}
	if _, err := broker.Select(ctx, browserconversation.BrowserTargetSelector{BrowserID: candidates[0].ID, TargetID: targetID}); err != nil {
		return err
	}
	_, err = broker.ListTools(ctx, browserconversation.BrowserListToolsOptions{IncludeSchemas: true})
	return err
}

func cloneAudioInputs(inputs []browserconversation.ScheduledAudioInput) []browserconversation.ScheduledAudioInput {
	clone := make([]browserconversation.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		clone[index] = input
		clone[index].PCM = append([]byte(nil), input.PCM...)
	}
	return clone
}

func expectedState(step *browserconversation.BrowserConversationStep) *browserconversation.BrowserStateTransition {
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

func scenarioStep(scenario browserconversation.BrowserConversationScenario, id string) *browserconversation.BrowserConversationStep {
	for index := range scenario.Steps {
		if scenario.Steps[index].ID == id {
			return &scenario.Steps[index]
		}
	}
	return nil
}

func isTerminal(state string) bool {
	switch state {
	case "completed", "error", "canceled", "timed_out", "orphaned", "policy_denied":
		return true
	default:
		return false
	}
}

func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func encodeRaw(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func cloneTool(tool browserconversation.BrowserToolDescriptor) browserconversation.BrowserToolDescriptor {
	tool.InputSchema = append([]byte(nil), tool.InputSchema...)
	return tool
}

func browserErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return safeText(err.Error())
}

func safeText(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			builder.WriteByte(' ')
		} else {
			builder.WriteRune(char)
		}
		if builder.Len() >= browserConversationMaxSafeTextBytes {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func lifecycleOutcome(rootErr, contextErr error) browserconversation.BrowserConversationLifecycleOutcome {
	if errors.Is(rootErr, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return browserconversation.BrowserConversationLifecycleTimedOut
	}
	if errors.Is(rootErr, context.Canceled) || errors.Is(contextErr, context.Canceled) {
		return browserconversation.BrowserConversationLifecycleCanceled
	}
	if rootErr != nil {
		return browserconversation.BrowserConversationLifecycleFailed
	}
	return browserconversation.BrowserConversationLifecycleCompleted
}
