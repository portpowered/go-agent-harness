package services

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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunBrowserConversationRecoversStaleReferenceAfterCustomerNavigation(t *testing.T) {
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:       browserConversationRecoveryScenario(),
		AudioByStep:    map[string][]byte{"apply": {1, 2}, "change": {3, 4}},
		FixtureScript:  browserConversationRecoveryScript(),
		FixtureOptions: []BrowserConversationFixtureOption{WithBrowserConversationFixtureRuntimeOption(testkit.WithFixtureState(map[string]any{"value": false}))},
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"value":false}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":false}`),
			json.RawMessage(`{"value":false}`),
		}},
		SessionRunner: browserConversationRecoverySession,
	})
	if err != nil {
		t.Fatalf("RunBrowserConversation: %v", err)
	}
	if !result.Mechanical.Passed || result.Lifecycle.Outcome != BrowserConversationLifecycleCompleted {
		t.Fatalf("result = %+v, want mechanically completed recovery", result)
	}
	if len(result.Recovery) != 1 {
		t.Fatalf("recovery = %+v, want one navigation recovery", result.Recovery)
	}
	recovery := result.Recovery[0]
	if !recovery.Passed || !recovery.NavigationObserved || !recovery.StaleRejected || !recovery.ToolsRelisted || !recovery.FreshInvocationCompleted {
		t.Fatalf("recovery = %+v, want every recovery fact", recovery)
	}
	if recovery.StaleErrorCode != string(webmcp.ErrorStaleToolRef) || recovery.StaleToolRef == "" || recovery.FreshToolRef == "" || recovery.StaleToolRef == recovery.FreshToolRef {
		t.Fatalf("recovery = %+v, want distinct stale/fresh refs and stale code", recovery)
	}
	if recovery.PreviousGeneration != 1 || recovery.CurrentGeneration != 2 || recovery.FreshGeneration != 2 {
		t.Fatalf("recovery generations = %+v, want 1 -> 2", recovery)
	}
	if len(result.Turns) != 4 {
		t.Fatalf("turns = %+v, want two customer/assistant pairs", result.Turns)
	}
	if len(result.Oracles) != 6 {
		t.Fatalf("oracles = %+v, want stale-before plus successful before/after/post facts", result.Oracles)
	}
	var sawNavigation, sawStale, sawFresh bool
	lastSequence := uint64(0)
	for _, call := range result.BrokerCalls {
		if call.Sequence <= lastSequence {
			t.Fatalf("broker calls are not ordered: %+v", result.BrokerCalls)
		}
		lastSequence = call.Sequence
		switch {
		case call.Operation == BrowserConversationCustomerNavigate:
			sawNavigation = call.PreviousGeneration == 1 && call.Generation == 2
		case call.Operation == BrowserConversationInvoke && call.ErrorCode == string(webmcp.ErrorStaleToolRef):
			sawStale = call.Terminal && call.ToolRef == recovery.StaleToolRef
		case call.Operation == BrowserConversationInvoke && call.State == webmcp.InvocationCompleted:
			if call.ToolRef == recovery.FreshToolRef && call.InputJSON == `{"value":false}` {
				sawFresh = true
			}
		}
	}
	if !sawNavigation || !sawStale || !sawFresh {
		t.Fatalf("broker calls = %+v, want navigation, stale rejection, and fresh completion", result.BrokerCalls)
	}
	if validateErr := result.Validate(); validateErr != nil {
		t.Fatalf("result.Validate: %v", validateErr)
	}
}

func TestRunBrowserConversationUsesFreshReferencesForDistinctTools(t *testing.T) {
	result, err := RunBrowserConversation(context.Background(), io.Discard, BrowserConversationRunOptions{
		Scenario:      browserConversationDirectFreshScenario(),
		AudioByStep:   map[string][]byte{"inspect": {1}, "clear": {2}},
		FixtureScript: browserConversationDirectFreshScript(),
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"value":false}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":false}`),
			json.RawMessage(`{"value":false}`),
		}},
		SessionRunner: browserConversationDirectFreshSession,
	})
	if err != nil {
		t.Fatalf("RunBrowserConversation: %v", err)
	}
	if !result.Mechanical.Passed || len(result.Recovery) != 0 {
		t.Fatalf("result = %+v, want direct fresh-reference success without recovery", result)
	}
	var refs []webmcp.ToolRef
	for _, call := range result.BrokerCalls {
		if call.Operation == BrowserConversationInvoke && call.State == webmcp.InvocationCompleted {
			refs = append(refs, call.ToolRef)
		}
	}
	if len(refs) != 2 || refs[0] == "" || refs[0] == refs[1] {
		t.Fatalf("completed invoke refs = %v, want two distinct fresh catalog refs", refs)
	}
	if validateErr := result.Validate(); validateErr != nil {
		t.Fatalf("result.Validate: %v", validateErr)
	}
}

func TestRunBrowserConversationRejectsCustomerTurnsOutOfOrder(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(true)
	result, err := RunBrowserConversation(context.Background(), io.Discard, browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{json.RawMessage(`{"value":false}`)}},
		func(_ context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptEnd,
				Role:  messages.RoleUser,
				Value: messages.NewTranscriptEndValue("apply"),
			})
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptEnd,
				Role:  messages.RoleUser,
				Value: messages.NewTranscriptEndValue("apply again"),
			})
			return nil
		},
	))
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) || !strings.Contains(err.Error(), "before assistant turn") {
		t.Fatalf("error = %v, want out-of-order evidence failure", err)
	}
	if result.Mechanical.Passed || !result.Finalized || fixture.CloseCount() != 1 || broker.closeCount != 1 {
		t.Fatalf("result=%+v fixture_closes=%d broker_closes=%d, want finalized failure and one cleanup", result, fixture.CloseCount(), broker.closeCount)
	}
}

func TestBrowserConversationTrackerDeadlineCancelsRun(t *testing.T) {
	run, err := NewBrowserConversationRun(browserConversationRunnerScenario())
	if err != nil {
		t.Fatalf("NewBrowserConversationRun: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker := newBrowserConversationEvidenceTracker(run, browserConversationRunnerScenario())
	tracker.configure(ctx, cancel, nil, nil)
	tracker.mu.Lock()
	tracker.currentStepIDValue = "apply"
	tracker.awaitingAssistant = true
	tracker.deadlineToken = 7
	tracker.mu.Unlock()
	tracker.expireDeadline(7, "apply", time.Second)
	if !errors.Is(tracker.err(), ErrBrowserConversationTimeout) {
		t.Fatalf("tracker error = %v, want timeout", tracker.err())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("deadline did not cancel the run context")
	}
}

func browserConversationRecoveryScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion,
		ID:      "iterative-recovery",
		Name:    "Iterative recovery",
		Fixture: BrowserConversationFixture{
			ID: "shop",
			Pages: []BrowserConversationPage{
				{ID: "checkout", URL: "https://fixture.test/checkout"},
				{ID: "next", URL: "https://fixture.test/next"},
			},
			InitialPage: "checkout",
		},
		Steps: []BrowserConversationStep{
			{
				ID: "apply", Utterance: "apply", PageID: "checkout", Deadline: 2 * time.Second,
				ExpectedState: &BrowserStateTransition{
					PageID: "checkout", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`),
				},
			},
			{
				ID: "change", Utterance: "go to the next page and change it", PageID: "next", Deadline: 2 * time.Second,
				Navigation: &BrowserCustomerNavigation{FromPageID: "checkout", ToPageID: "next", URL: "https://fixture.test/next"},
				ExpectedState: &BrowserStateTransition{
					PageID: "next", Before: json.RawMessage(`{"value":true}`), After: json.RawMessage(`{"value":false}`),
				},
			},
		},
		RunTimeout: 5 * time.Second,
		PostSession: BrowserConversationTabStateRequired{
			PageID: "next", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func browserConversationRecoveryScript() testkit.BrowserScript {
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
		Operations: []testkit.BrowserScriptOperation{
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Emit: []testkit.EmittedEvent{{Type: testkit.EmittedToolsAdded, Tools: []testkit.ToolDescriptor{browserConversationFixtureTool("write_state")}}}},
			{
				Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: json.RawMessage(`{"value":true}`)},
				Result: json.RawMessage(`{"invocation_id":"browser-first"}`),
				Emit:   []testkit.EmittedEvent{{Type: testkit.EmittedToolResponded, InvocationID: "browser-first", Status: "Completed", Output: json.RawMessage(`{"value":true}`)}},
			},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationNavigate, URL: "https://fixture.test/next"}},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Emit: []testkit.EmittedEvent{{Type: testkit.EmittedToolsAdded, Tools: []testkit.ToolDescriptor{browserConversationFixtureTool("write_state")}}}},
			{
				Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: json.RawMessage(`{"value":false}`)},
				Result: json.RawMessage(`{"invocation_id":"browser-second"}`),
				Emit:   []testkit.EmittedEvent{{Type: testkit.EmittedToolResponded, InvocationID: "browser-second", Status: "Completed", Output: json.RawMessage(`{"value":false}`)}},
			},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationDetachTarget}},
		},
	}
}

func browserConversationRecoverySession(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
	if request.StreamObserver == nil || request.ToolExecutor == nil || request.Broker == nil {
		return errors.New("recovery session seams were not composed")
	}
	initial, err := request.Broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		return err
	}
	oldRef, err := browserConversationToolRefByName(initial, "write_state")
	if err != nil {
		return err
	}
	emitBrowserConversationCustomer(request.StreamObserver, "apply")
	if envelope, invokeErr := executeBrowserConversationTool(ctx, request.ToolExecutor, "call-first", oldRef, `{"value":true}`, "apply"); invokeErr != nil || !envelope.OK {
		return fmt.Errorf("initial invocation: envelope=%+v error=%v", envelope, invokeErr)
	}
	emitBrowserConversationAssistant(request.StreamObserver, "applied")

	emitBrowserConversationCustomer(request.StreamObserver, "go to the next page and change it")
	stale, invokeErr := executeBrowserConversationTool(ctx, request.ToolExecutor, "call-stale", oldRef, `{"value":false}`, "change")
	if invokeErr != nil {
		return invokeErr
	}
	if stale.OK || stale.Error == nil || stale.Error.Code != string(webmcp.ErrorStaleToolRef) {
		return fmt.Errorf("stale invocation envelope = %+v, want stale_tool_ref failure", stale)
	}
	freshCatalog, err := request.Broker.ListTools(ctx, webmcp.ListToolsOptions{Refresh: true, IncludeSchemas: true})
	if err != nil {
		return err
	}
	freshRef, err := browserConversationToolRefByName(freshCatalog, "write_state")
	if err != nil {
		return err
	}
	if freshRef == oldRef {
		return errors.New("refresh returned the stale tool reference")
	}
	fresh, invokeErr := executeBrowserConversationTool(ctx, request.ToolExecutor, "call-fresh", freshRef, `{"value":false}`, "change")
	if invokeErr != nil || !fresh.OK {
		return fmt.Errorf("fresh invocation: envelope=%+v error=%v", fresh, invokeErr)
	}
	emitBrowserConversationAssistant(request.StreamObserver, "changed")
	return nil
}

func browserConversationDirectFreshScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion, ID: "direct-fresh", Name: "Direct fresh tools",
		Fixture: BrowserConversationFixture{ID: "shop", Pages: []BrowserConversationPage{{ID: "checkout", URL: "https://fixture.test/checkout"}}, InitialPage: "checkout"},
		Steps: []BrowserConversationStep{
			{ID: "inspect", Utterance: "inspect", PageID: "checkout", ExpectedState: &BrowserStateTransition{PageID: "checkout", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`)}, Deadline: 2 * time.Second},
			{ID: "clear", Utterance: "clear", PageID: "checkout", ExpectedState: &BrowserStateTransition{PageID: "checkout", Before: json.RawMessage(`{"value":true}`), After: json.RawMessage(`{"value":false}`)}, Deadline: 2 * time.Second},
		},
		RunTimeout:  5 * time.Second,
		PostSession: BrowserConversationTabStateRequired{PageID: "checkout", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func browserConversationDirectFreshScript() testkit.BrowserScript {
	script := browserConversationRecoveryScript()
	script.Operations = []testkit.BrowserScriptOperation{
		script.Operations[0],
		{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Emit: []testkit.EmittedEvent{{Type: testkit.EmittedToolsAdded, Tools: []testkit.ToolDescriptor{browserConversationFixtureTool("inspect_state"), browserConversationFixtureTool("clear_state")}}}},
		{Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "inspect_state", Input: json.RawMessage(`{"value":true}`)}, Result: json.RawMessage(`{"invocation_id":"browser-inspect"}`), Emit: []testkit.EmittedEvent{{Type: testkit.EmittedToolResponded, InvocationID: "browser-inspect", Status: "Completed", Output: json.RawMessage(`{"value":true}`)}}},
		{Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "clear_state", Input: json.RawMessage(`{"value":false}`)}, Result: json.RawMessage(`{"invocation_id":"browser-clear"}`), Emit: []testkit.EmittedEvent{{Type: testkit.EmittedToolResponded, InvocationID: "browser-clear", Status: "Completed", Output: json.RawMessage(`{"value":false}`)}}},
		{Expect: testkit.OperationExpectation{Type: testkit.OperationDetachTarget}},
	}
	return script
}

func browserConversationDirectFreshSession(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
	catalog, err := request.Broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		return err
	}
	for index, step := range []struct {
		utterance string
		name      string
		input     string
	}{
		{utterance: "inspect", name: "inspect_state", input: `{"value":true}`},
		{utterance: "clear", name: "clear_state", input: `{"value":false}`},
	} {
		ref, refErr := browserConversationToolRefByName(catalog, step.name)
		if refErr != nil {
			return refErr
		}
		emitBrowserConversationCustomer(request.StreamObserver, step.utterance)
		envelope, invokeErr := executeBrowserConversationTool(ctx, request.ToolExecutor, fmt.Sprintf("call-%d", index), ref, step.input, step.utterance)
		if invokeErr != nil || !envelope.OK {
			return fmt.Errorf("direct invocation %s: envelope=%+v error=%v", step.name, envelope, invokeErr)
		}
		emitBrowserConversationAssistant(request.StreamObserver, step.name+" completed")
	}
	return nil
}

func browserConversationFixtureTool(name string) testkit.ToolDescriptor {
	return testkit.ToolDescriptor{
		Name: name, Description: "Mutate fixture state", FrameID: "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"boolean"}},"required":["value"],"additionalProperties":false}`),
	}
}

func browserConversationToolRefByName(catalog webmcp.ToolCatalogSnapshot, name string) (webmcp.ToolRef, error) {
	for _, tool := range catalog.Tools {
		if tool.Name == name {
			if tool.Ref == "" {
				return "", fmt.Errorf("tool %q has an empty reference", name)
			}
			return tool.Ref, nil
		}
	}
	return "", fmt.Errorf("catalog did not contain tool %q", name)
}

func executeBrowserConversationTool(ctx context.Context, executor messages.ToolExecutor, callID string, ref webmcp.ToolRef, input, reason string) (webmcp.ToolResultEnvelope, error) {
	arguments, err := json.Marshal(map[string]string{
		"tool_ref": string(ref), "input_json": input, "reason": reason,
	})
	if err != nil {
		return webmcp.ToolResultEnvelope{}, err
	}
	response, err := executor.Execute(ctx, messages.ToolCall{ID: callID, Name: webmcp.InvokeToolName, Arguments: string(arguments)})
	if err != nil {
		return webmcp.ToolResultEnvelope{}, err
	}
	return webmcp.UnmarshalToolResult([]byte(response.Content))
}

func emitBrowserConversationCustomer(observer SessionStreamObserver, text string) {
	observer(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue(text)})
}

func emitBrowserConversationAssistant(observer SessionStreamObserver, text string) {
	observer(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	observer(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)})
	observer(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}
