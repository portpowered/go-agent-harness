package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestRunBrowserConversationProvesSpokenCorrectionWithIndependentEvidence(t *testing.T) {
	var validatorSaw BrowserConversationCorrectionEvidence
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:       browserConversationCorrectionScenario(),
		AudioByStep:    map[string][]byte{"apply": {1, 2}, "correct": {3, 4}},
		FixtureScript:  browserConversationCorrectionScript(),
		FixtureOptions: browserConversationCorrectionFixtureOptions(),
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"label":"unset"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"unset"}`),
			json.RawMessage(`{"label":"unset"}`),
		}},
		PostSessionProbe: browserConversationCorrectionProbe,
		SessionRunner:    browserConversationCorrectionSession,
		Validator: BrowserConversationValidatorFunc(func(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
			if len(result.Corrections) != 1 {
				return BrowserConversationValidatorVerdict{}, fmt.Errorf("corrections = %d, want one", len(result.Corrections))
			}
			validatorSaw = result.Corrections[0]
			return BrowserConversationValidatorVerdict{
				Version: BrowserConversationValidatorVersion,
				Status:  BrowserConversationValidatorPass,
				Passed:  true,
				Checks: []BrowserConversationValidatorCheck{
					{Name: "original and corrected intents retained", Passed: true},
					{Name: "both independent transitions retained", Passed: true},
				},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("RunBrowserConversation: %v", err)
	}
	if !result.Mechanical.Passed || !result.Validator.Passed {
		t.Fatalf("result = %+v, want mechanical and validator pass", result)
	}
	if !validatorSaw.Passed || validatorSaw.TargetStepID != "apply" || validatorSaw.StepID != "correct" {
		t.Fatalf("validator correction evidence = %+v, want passed apply -> correct evidence", validatorSaw)
	}
	if validatorSaw.TargetUtterance != "Set the label to blue." || validatorSaw.CorrectionUtterance != "Actually, undo that and set the label back to unset." {
		t.Fatalf("validator intents = %+v, want both exact utterances", validatorSaw)
	}
	if string(validatorSaw.OriginalBefore) != `{"label":"unset"}` || string(validatorSaw.OriginalAfter) != `{"label":"blue"}` ||
		string(validatorSaw.CorrectionBefore) != `{"label":"blue"}` || string(validatorSaw.CorrectionAfter) != `{"label":"unset"}` {
		t.Fatalf("validator transitions = %+v, want original and corrected oracle pairs", validatorSaw)
	}
	if validatorSaw.OriginalInvocationID != "inv-000001" || validatorSaw.CorrectionInvocationID != "inv-000002" ||
		validatorSaw.OriginalToolName != "write_label" || validatorSaw.CorrectionToolName != "write_label" {
		t.Fatalf("validator invocations = %+v, want both write_label completions", validatorSaw)
	}
	if validatorSaw.OriginalAssistantText != "The label is blue." || validatorSaw.CorrectionAssistantText != "I changed the label back to unset." {
		t.Fatalf("validator confirmations = %+v, want grounded confirmations", validatorSaw)
	}
	var completedInputs []string
	for _, call := range result.BrokerCalls {
		if call.Operation == BrowserConversationInvoke && call.Terminal && call.State == webmcp.InvocationCompleted {
			completedInputs = append(completedInputs, call.InputJSON)
		}
	}
	if len(completedInputs) != 2 || completedInputs[0] != `{"label":"blue"}` || completedInputs[1] != `{"label":"unset"}` {
		t.Fatalf("completed inputs = %v, want original and correction inputs", completedInputs)
	}
	if result.Lifecycle.Outcome != BrowserConversationLifecycleCompleted || result.Lifecycle.DetachCount != 1 || result.Lifecycle.TargetClosed || result.Lifecycle.BrowserClosed {
		t.Fatalf("lifecycle = %+v, want one external detach", result.Lifecycle)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate: %v", err)
	}
}

func TestRunBrowserConversationRejectsCorrectionAcknowledgementWithoutMutation(t *testing.T) {
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:       browserConversationCorrectionScenario(),
		AudioByStep:    map[string][]byte{"apply": {1}, "correct": {2}},
		FixtureScript:  browserConversationCorrectionInitialOnlyScript(),
		FixtureOptions: browserConversationCorrectionFixtureOptions(),
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"label":"unset"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
		}},
		PostSessionProbe: browserConversationCorrectionProbe,
		SessionRunner:    browserConversationCorrectionInitialOnlySession,
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want mechanical evidence failure", err)
	}
	if result.Mechanical.Passed || len(result.Corrections) != 1 || result.Corrections[0].CorrectionInvocationCompleted {
		t.Fatalf("result = %+v, want failed correction without terminal invocation", result)
	}
	if !containsBrowserConversationFailure(result.Mechanical.Failures, "correction lacks a completed terminal invocation") {
		t.Fatalf("mechanical failures = %v, want missing correction terminal result", result.Mechanical.Failures)
	}
}

func TestRunBrowserConversationRejectsCorrectionThatLeavesSupersededOracleState(t *testing.T) {
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:       browserConversationCorrectionScenario(),
		AudioByStep:    map[string][]byte{"apply": {1}, "correct": {2}},
		FixtureScript:  browserConversationCorrectionScript(),
		FixtureOptions: browserConversationCorrectionFixtureOptions(),
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"label":"unset"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"unset"}`),
		}},
		PostSessionProbe: browserConversationCorrectionProbe,
		SessionRunner:    browserConversationCorrectionSession,
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want oracle evidence failure", err)
	}
	if result.Mechanical.Passed || result.Corrections[0].Passed {
		t.Fatalf("result = %+v, want failed correction evidence", result)
	}
	if !containsBrowserConversationFailure(result.Mechanical.Failures, "correction left the superseded state in place") {
		t.Fatalf("mechanical failures = %v, want superseded-state failure", result.Mechanical.Failures)
	}
}

func TestRunBrowserConversationRejectsCorrectionWithoutTerminalResult(t *testing.T) {
	broker := &browserConversationCorrectionNoTerminalBroker{}
	fixture := &BrowserConversationFixtureRun{Broker: broker}
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:    browserConversationCorrectionScenario(),
		AudioByStep: map[string][]byte{"apply": {1}, "correct": {2}},
		FixtureFactory: func(context.Context, BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
			return fixture, nil
		},
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"label":"unset"}`),
			json.RawMessage(`{"label":"blue"}`),
			json.RawMessage(`{"label":"blue"}`),
		}},
		PostSessionProbe: browserConversationCorrectionProbe,
		SessionRunner:    browserConversationCorrectionSession,
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want missing-terminal evidence failure", err)
	}
	if result.Mechanical.Passed || len(result.Corrections) != 1 || result.Corrections[0].OriginalInvocationCompleted || result.Corrections[0].CorrectionInvocationCompleted {
		t.Fatalf("result = %+v, want both correction invocations non-terminal", result)
	}
	if !containsBrowserConversationFailure(result.Mechanical.Failures, "original browser action lacks a completed terminal invocation") ||
		!containsBrowserConversationFailure(result.Mechanical.Failures, "correction lacks a completed terminal invocation") {
		t.Fatalf("mechanical failures = %v, want both missing terminal results", result.Mechanical.Failures)
	}
	if broker.closeCount != 1 {
		t.Fatalf("broker close count = %d, want one", broker.closeCount)
	}
}

func TestBrowserConversationScenarioRequiresCorrectionToStartFromSupersededState(t *testing.T) {
	scenario := browserConversationCorrectionScenario()
	scenario.Steps[1].Correction.ExpectedState.Before = json.RawMessage(`{"label":"unset"}`)
	if _, err := NewBrowserConversationScenario(scenario); err == nil || !strings.Contains(err.Error(), "steps[1].correction.expected_state.before") {
		t.Fatalf("scenario error = %v, want correction before-state path", err)
	}

	scenario = browserConversationCorrectionScenario()
	scenario.Steps[1].ExpectedState = &BrowserStateTransition{
		PageID: "checkout", Before: json.RawMessage(`{"label":"blue"}`), After: json.RawMessage(`{"label":"unset"}`),
	}
	if _, err := NewBrowserConversationScenario(scenario); err == nil || !strings.Contains(err.Error(), "steps[1].correction") {
		t.Fatalf("ambiguous correction error = %v, want correction path", err)
	}
}

func browserConversationCorrectionScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion,
		ID:      "spoken-correction",
		Name:    "Spoken correction",
		Fixture: BrowserConversationFixture{
			ID:          "shop",
			Pages:       []BrowserConversationPage{{ID: "checkout", URL: "https://fixture.test/checkout"}},
			InitialPage: "checkout",
		},
		Steps: []BrowserConversationStep{
			{
				ID: "apply", Utterance: "Set the label to blue.", PageID: "checkout", Deadline: 2 * time.Second,
				ExpectedState: &BrowserStateTransition{
					PageID: "checkout", Before: json.RawMessage(`{"label":"unset"}`), After: json.RawMessage(`{"label":"blue"}`),
				},
			},
			{
				ID: "correct", Utterance: "Actually, undo that and set the label back to unset.", PageID: "checkout", Deadline: 2 * time.Second,
				Correction: &BrowserConversationCorrection{
					TargetStepID: "apply",
					ExpectedState: BrowserStateTransition{
						PageID: "checkout", Before: json.RawMessage(`{"label":"blue"}`), After: json.RawMessage(`{"label":"unset"}`),
					},
				},
			},
		},
		RunTimeout: 5 * time.Second,
		PostSession: BrowserConversationTabStateRequired{
			PageID: "checkout", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func browserConversationCorrectionScript() testkit.BrowserScript {
	return browserConversationCorrectionScriptWithOperations(
		testkit.BrowserScriptOperation{
			Expect: testkit.OperationExpectation{
				Type:     testkit.OperationInvokeTool,
				FrameID:  "frame-1",
				ToolName: "write_label",
				Input:    json.RawMessage(`{"label":"blue"}`),
			},
			Result: json.RawMessage(`{"invocation_id":"correction-initial"}`),
			Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolResponded, InvocationID: "correction-initial", Status: "Completed", Output: json.RawMessage(`{"label":"blue"}`),
			}},
		},
		testkit.BrowserScriptOperation{
			Expect: testkit.OperationExpectation{
				Type:     testkit.OperationInvokeTool,
				FrameID:  "frame-1",
				ToolName: "write_label",
				Input:    json.RawMessage(`{"label":"unset"}`),
			},
			Result: json.RawMessage(`{"invocation_id":"correction-undo"}`),
			Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolResponded, InvocationID: "correction-undo", Status: "Completed", Output: json.RawMessage(`{"label":"unset"}`),
			}},
		},
	)
}

func browserConversationCorrectionInitialOnlyScript() testkit.BrowserScript {
	script := browserConversationCorrectionScript()
	script.Operations = []testkit.BrowserScriptOperation{
		script.Operations[0], script.Operations[1], script.Operations[2], script.Operations[4],
	}
	return script
}

func browserConversationCorrectionScriptWithOperations(invocations ...testkit.BrowserScriptOperation) testkit.BrowserScript {
	return testkit.BrowserScript{
		Version: testkit.BrowserScriptVersion,
		Endpoint: testkit.BrowserEndpoint{
			Version: testkit.EndpointVersionInfo{
				Browser: "Chrome/Fixture", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://fixture/browser",
			},
			Targets: []testkit.BrowserTarget{{
				ID: "tab-1", Type: "page", Title: "Checkout", URL: "https://fixture.test/checkout", WebSocketDebuggerURL: "ws://fixture/page/tab-1",
			}},
		},
		Operations: append([]testkit.BrowserScriptOperation{
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolsAdded,
				Tools: []testkit.ToolDescriptor{{
					Name: "write_label", Description: "Write fixture label", FrameID: "frame-1",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false}`),
				}},
			}}},
		}, append(invocations, testkit.BrowserScriptOperation{Expect: testkit.OperationExpectation{Type: testkit.OperationDetachTarget}})...),
	}
}

func browserConversationCorrectionFixtureOptions() []BrowserConversationFixtureOption {
	return []BrowserConversationFixtureOption{
		WithBrowserConversationFixtureRuntimeOption(testkit.WithFixtureState(map[string]any{"label": "unset"})),
		WithBrowserConversationFixtureBrokerOption(webmcp.BrokerOptions{
			ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
				return browserConversationRunnerToolRef(), nil
			},
		}),
	}
}

func browserConversationCorrectionProbe(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error) {
	return BrowserConversationTabStateProbeResult{PageID: "checkout", Alive: true, Responsive: true, AllowsMutation: true}, nil
}

func browserConversationCorrectionSession(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
	catalog, err := request.Broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		return err
	}
	ref, err := browserConversationToolRefByName(catalog, "write_label")
	if err != nil {
		return err
	}
	emitBrowserConversationCustomer(request.StreamObserver, "Set the label to blue.")
	initial, err := executeBrowserConversationTool(ctx, request.ToolExecutor, "correction-call-initial", ref, `{"label":"blue"}`, "initial label request")
	if err != nil || !initial.OK {
		return fmt.Errorf("initial correction fixture call: envelope=%+v error=%v", initial, err)
	}
	emitBrowserConversationAssistant(request.StreamObserver, "The label is blue.")
	emitBrowserConversationCustomer(request.StreamObserver, "Actually, undo that and set the label back to unset.")
	corrected, err := executeBrowserConversationTool(ctx, request.ToolExecutor, "correction-call-undo", ref, `{"label":"unset"}`, "spoken correction")
	if err != nil || !corrected.OK {
		return fmt.Errorf("correcting fixture call: envelope=%+v error=%v", corrected, err)
	}
	emitBrowserConversationAssistant(request.StreamObserver, "I changed the label back to unset.")
	return nil
}

func browserConversationCorrectionInitialOnlySession(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
	catalog, err := request.Broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		return err
	}
	ref, err := browserConversationToolRefByName(catalog, "write_label")
	if err != nil {
		return err
	}
	emitBrowserConversationCustomer(request.StreamObserver, "Set the label to blue.")
	initial, err := executeBrowserConversationTool(ctx, request.ToolExecutor, "correction-call-initial", ref, `{"label":"blue"}`, "initial label request")
	if err != nil || !initial.OK {
		return fmt.Errorf("initial correction fixture call: envelope=%+v error=%v", initial, err)
	}
	emitBrowserConversationAssistant(request.StreamObserver, "The label is blue.")
	emitBrowserConversationCustomer(request.StreamObserver, "Actually, undo that and set the label back to unset.")
	emitBrowserConversationAssistant(request.StreamObserver, "Done.")
	return nil
}

// browserConversationCorrectionNoTerminalBroker deliberately has no
// WaitInvocation method. It models a broker that admits work but never
// publishes a terminal result, so the coordinator must not accept speech as
// proof of either mutation.
type browserConversationCorrectionNoTerminalBroker struct {
	closeCount int
}

func (b *browserConversationCorrectionNoTerminalBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return []webmcp.BrowserCandidate{{ID: "correction-browser", Explicit: true}}, nil
}

func (b *browserConversationCorrectionNoTerminalBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return []webmcp.Target{{BrowserID: "correction-browser", ID: "correction-tab", Generation: 1, Eligible: true}}, nil
}

func (b *browserConversationCorrectionNoTerminalBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	return webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: "correction-browser", TargetID: "correction-tab"},
		Generation: 1,
		Connected:  true,
		Ready:      true,
	}, nil
}

func (b *browserConversationCorrectionNoTerminalBroker) Selected(ctx context.Context) (webmcp.PageContext, error) {
	return b.Select(ctx, webmcp.TargetSelector{})
}

func (b *browserConversationCorrectionNoTerminalBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return webmcp.ToolCatalogSnapshot{
		Context: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "correction-browser", TargetID: "correction-tab"},
			Generation: 1,
			Connected:  true,
			Ready:      true,
		},
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{{
			Ref: browserConversationRunnerToolRef(), Name: "write_label", FrameID: "frame-1",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`),
		}},
	}, nil
}

func (*browserConversationCorrectionNoTerminalBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{InvocationID: "never-terminal", State: webmcp.InvocationDispatched}, nil
}

func (*browserConversationCorrectionNoTerminalBroker) Cancel(context.Context, webmcp.CancelRequest) error {
	return nil
}

func (*browserConversationCorrectionNoTerminalBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	events := make(chan webmcp.BrokerEvent)
	close(events)
	return events
}

func (b *browserConversationCorrectionNoTerminalBroker) Close() error {
	b.closeCount++
	return nil
}
