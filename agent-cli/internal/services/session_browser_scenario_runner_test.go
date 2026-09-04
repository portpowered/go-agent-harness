package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunBrowserConversationJoinsHermeticFixtureSessionAndOracleEvidence(t *testing.T) {
	scenario := browserConversationRunnerScenario()
	script := browserConversationRunnerScript()
	oracle := &browserConversationSequenceOracle{
		states: []json.RawMessage{
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":true}"),
			json.RawMessage("{\"value\":true}"),
		},
	}
	var fixture *BrowserConversationFixtureRun
	var sawAudio []byte
	var sawDefinitions int

	result, err := RunBrowserConversation(context.Background(), &bytes.Buffer{}, BrowserConversationRunOptions{
		Scenario:    scenario,
		AudioByStep: map[string][]byte{"apply": {1, 2, 3, 4}},
		FixtureFactory: func(ctx context.Context, _ BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
			created, createErr := NewBrowserConversationFixtureRun(
				script,
				WithBrowserConversationFixtureBrokerOptions(webmcp.BrokerOptions{
					ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
						return browserConversationRunnerToolRef(), nil
					},
				}),
			)
			fixture = created
			return created, createErr
		},
		Oracle: oracle,
		PostSessionProbe: func(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error) {
			return BrowserConversationTabStateProbeResult{
				PageID: "checkout", Alive: true, Responsive: true, AllowsMutation: true,
			}, nil
		},
		Validator: BrowserConversationValidatorFunc(func(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
			if !result.Finalized {
				return BrowserConversationValidatorVerdict{}, errors.New("validator received a non-final result")
			}
			return BrowserConversationValidatorVerdict{
				Version: BrowserConversationValidatorVersion,
				Status:  BrowserConversationValidatorPass,
				Passed:  true,
				Summary: "language is supplementary evidence",
			}, nil
		}),
		SessionRunner: func(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
			if len(request.AudioInputs) != 1 {
				return fmt.Errorf("audio input count = %d, want one", len(request.AudioInputs))
			}
			sawAudio = append([]byte(nil), request.AudioInputs[0].PCM...)
			sawDefinitions = len(request.ToolDefinitions)
			if request.StreamObserver == nil || request.ToolExecutor == nil {
				return errors.New("shared session seams were not composed")
			}
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptEnd,
				Role:  messages.RoleUser,
				Value: messages.NewTranscriptEndValue("apply"),
			})
			arguments := "{\"tool_ref\":\"" + string(browserConversationRunnerToolRef()) + "\",\"input_json\":\"{\\\"value\\\":true}\",\"reason\":\"apply\"}"
			response, executeErr := request.ToolExecutor.Execute(ctx, messages.ToolCall{
				ID:        "call-1",
				Name:      webmcp.InvokeToolName,
				Arguments: arguments,
			})
			if executeErr != nil {
				return executeErr
			}
			if !strings.Contains(response.Content, "completed") {
				return fmt.Errorf("tool response = %s, want completed", response.Content)
			}
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeMessageStart,
				Role:  messages.RoleAssistant,
				Value: messages.NewMessageStartValue(),
			})
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTextDeltaValue("discount applied"),
			})
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeMessageEnd,
				Role:  messages.RoleAssistant,
				Value: messages.NewMessageEndValue(messages.TokenUsage{}),
			})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunBrowserConversation: %v", err)
	}
	if !bytes.Equal(sawAudio, []byte{1, 2, 3, 4}) || sawDefinitions != 9 {
		t.Fatalf("session composition audio=%v definitions=%d", sawAudio, sawDefinitions)
	}
	if !result.Finalized || !result.Mechanical.Passed {
		t.Fatalf("result = %+v, want finalized mechanical pass", result)
	}
	if result.Validator.Status != BrowserConversationValidatorPass || !result.Validator.Passed {
		t.Fatalf("validator = %+v, want pass", result.Validator)
	}
	if len(result.Turns) != 2 || result.Turns[0].Direction != BrowserConversationCustomerTurn || result.Turns[1].Direction != BrowserConversationAssistantTurn {
		t.Fatalf("turns = %+v, want customer then assistant", result.Turns)
	}
	if len(result.BrokerCalls) < 3 {
		t.Fatalf("broker calls = %+v, want setup and invoke evidence", result.BrokerCalls)
	}
	var terminalInvoke bool
	for _, call := range result.BrokerCalls {
		if call.Operation == BrowserConversationInvoke && call.Terminal {
			terminalInvoke = call.State == webmcp.InvocationCompleted && call.InputJSON == "{\"value\":true}"
		}
	}
	if !terminalInvoke {
		t.Fatalf("broker calls = %+v, want terminal invoke with exact input JSON", result.BrokerCalls)
	}
	if len(result.Oracles) != 3 || result.Oracles[0].Phase != BrowserConversationOracleBefore || result.Oracles[1].Phase != BrowserConversationOracleAfter || result.Oracles[2].Phase != BrowserConversationOraclePostSession {
		t.Fatalf("oracle evidence = %+v, want before/after/post-session order", result.Oracles)
	}
	if result.Lifecycle.Outcome != BrowserConversationLifecycleCompleted || !result.Lifecycle.Detached || result.Lifecycle.DetachCount != 1 || result.Lifecycle.TargetClosed || result.Lifecycle.BrowserClosed {
		t.Fatalf("lifecycle = %+v, want completed external-detach lifecycle", result.Lifecycle)
	}
	if fixture == nil || fixture.CloseCount() != 1 {
		t.Fatalf("fixture close count = %v, want one", fixture)
	}
}

func TestRunBrowserConversationDefaultRunnerUsesSharedDuplexAudioPath(t *testing.T) {
	oracle := &browserConversationSequenceOracle{
		states: []json.RawMessage{
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":true}"),
			json.RawMessage("{\"value\":true}"),
		},
	}
	sessionInferencer := &browserConversationSessionInferencer{
		toolRef: browserConversationRunnerToolRef(),
	}
	result, err := RunBrowserConversation(context.Background(), nil, BrowserConversationRunOptions{
		Scenario:      browserConversationRunnerScenario(),
		AudioByStep:   map[string][]byte{"apply": {9, 8, 7}},
		FixtureScript: browserConversationRunnerScript(),
		FixtureOptions: []BrowserConversationFixtureOption{
			WithBrowserConversationFixtureBrokerOptions(webmcp.BrokerOptions{
				ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
					return browserConversationRunnerToolRef(), nil
				},
			}),
		},
		Oracle: oracle,
		PostSessionProbe: func(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error) {
			return BrowserConversationTabStateProbeResult{PageID: "checkout", Alive: true, Responsive: true, AllowsMutation: true}, nil
		},
		SessionOptions: SessionRunOptions{SessionInferencer: sessionInferencer},
	})
	if err != nil {
		t.Fatalf("RunBrowserConversation: %v", err)
	}
	if !result.Mechanical.Passed || result.Lifecycle.Outcome != BrowserConversationLifecycleCompleted {
		t.Fatalf("result = %+v, want mechanically completed run", result)
	}
	sessionInferencer.mu.Lock()
	audio := append([]byte(nil), sessionInferencer.audio...)
	sessionInferencer.mu.Unlock()
	if !bytes.Equal(audio, []byte{9, 8, 7}) {
		t.Fatalf("provider session audio = %v, want scheduled PCM", audio)
	}
}

func TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab(t *testing.T) {
	scenario := browserConversationInterruptScenario()
	inferencer := &browserConversationInterruptInferencer{toolRef: browserConversationRunnerToolRef()}
	result, err := RunBrowserConversation(context.Background(), nil, BrowserConversationRunOptions{
		Scenario: scenario,
		AudioByStep: map[string][]byte{
			"apply":     {1},
			"start":     {2},
			"interrupt": {3},
			"cancel":    {4},
		},
		FixtureScript: browserConversationInterruptScript(),
		FixtureOptions: []BrowserConversationFixtureOption{
			WithBrowserConversationFixtureBrokerOptions(webmcp.BrokerOptions{
				ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
					return browserConversationRunnerToolRef(), nil
				},
			}),
		},
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage(`{"value":false}`),
			json.RawMessage(`{"value":true}`),
			json.RawMessage(`{"value":true}`),
		}},
		SessionOptions: SessionRunOptions{SessionInferencer: inferencer},
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationSession) {
		t.Fatalf("RunBrowserConversation error = %v, want expected canceled session error", err)
	}
	if !result.Finalized || !result.Mechanical.Passed {
		t.Fatalf("result = %+v, want finalized mechanical pass", result)
	}
	cancellation := result.Cancellation
	if !cancellation.Interrupted || !cancellation.Requested || cancellation.InvocationID == "" || cancellation.FinalState != webmcp.InvocationCanceled {
		t.Fatalf("cancellation = %+v, want interrupted requested canceled invocation", cancellation)
	}
	if cancellation.InterruptedStepID != "start" || cancellation.CancelStepID != "cancel" ||
		!cancellation.OverlappingAudioSent || !cancellation.ExplicitCancelAudioSent || cancellation.LateEventsSuppressed == 0 {
		t.Fatalf("cancellation = %+v, want step/audio/late-event evidence", cancellation)
	}
	if result.Lifecycle.Outcome != BrowserConversationLifecycleCanceled || !result.Lifecycle.Detached ||
		result.Lifecycle.DetachCount != 1 || !result.Lifecycle.ExternalTabAlive ||
		!result.Lifecycle.ExternalTabResponsive || !result.Lifecycle.ExternalTabAllowsMutation ||
		!result.Lifecycle.ExternalTabRead || !result.Lifecycle.ExternalTabMutation ||
		result.Lifecycle.TargetClosed || result.Lifecycle.BrowserClosed {
		t.Fatalf("lifecycle = %+v, want canceled one-detach surviving-tab lifecycle", result.Lifecycle)
	}
	for _, turn := range result.Turns {
		if turn.ObservedText == "late response" {
			t.Fatalf("late response crossed customer evidence boundary: %+v", result.Turns)
		}
	}
	inferencer.mu.Lock()
	audio := append([]byte(nil), inferencer.audio...)
	inferencer.mu.Unlock()
	if !bytes.Equal(audio, []byte{1, 2, 3, 4}) {
		t.Fatalf("shared session audio = %v, want initial/in-flight/overlap/cancel audio", audio)
	}
}

func TestBrowserConversationHoldsStandaloneCancelUntilInFlightInvocation(t *testing.T) {
	scenario := browserConversationRunnerScenario()
	scenario.Steps = append(scenario.Steps, BrowserConversationStep{
		ID: "cancel", Utterance: "cancel", PageID: "checkout", Deadline: time.Second,
		Cancel: &BrowserConversationCancelRequest{Reason: "customer requested stop"},
	})
	inputs, err := scenario.ScheduleAudioInputs(map[string][]byte{"apply": {1}, "cancel": {2}})
	if err != nil {
		t.Fatalf("ScheduleAudioInputs: %v", err)
	}
	normal, special := partitionBrowserConversationAudio(scenario, inputs)
	if len(normal) != 1 || len(special) != 1 || len(special["cancel"].PCM) != 1 {
		t.Fatalf("partitioned audio normal=%+v special=%+v, want one ordinary and one held cancel input", normal, special)
	}
	run, err := NewBrowserConversationRun(scenario)
	if err != nil {
		t.Fatalf("NewBrowserConversationRun: %v", err)
	}
	tracker := newBrowserConversationEvidenceTracker(run, scenario)
	controller := newBrowserConversationInterruptionController(run, tracker, scenario, special)
	if controller == nil {
		t.Fatal("standalone cancel did not create an event-driven controller")
	}
	defer controller.Close()
	controller.observeInFlight("apply", "invocation-1", "write_state")
	select {
	case input := <-controller.AudioInterruptions():
		if input.EndOfTurn != true || !bytes.Equal(input.PCM, []byte{2}) {
			t.Fatalf("held cancel input = %+v, want PCM [2] with end-of-turn", input)
		}
	case <-time.After(time.Second):
		t.Fatal("standalone cancel was not released by in-flight observation")
	}
	cancellation := run.Snapshot().Cancellation
	if cancellation.Interrupted || !cancellation.ExplicitCancelAudioSent || cancellation.InvocationID != "invocation-1" {
		t.Fatalf("cancellation = %+v, want explicit cancel audio without interruption", cancellation)
	}
}

func browserConversationInterruptScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion,
		ID:      "interrupt-cancel-flow",
		Name:    "Interrupt and cancel flow",
		Fixture: BrowserConversationFixture{
			ID:          "shop",
			InitialPage: "checkout",
			Pages:       []BrowserConversationPage{{ID: "checkout", URL: "https://fixture.test/checkout"}},
		},
		Steps: []BrowserConversationStep{
			{
				ID: "apply", Utterance: "apply", PageID: "checkout", Deadline: time.Second,
				ExpectedState: &BrowserStateTransition{
					PageID: "checkout", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`),
				},
			},
			{ID: "start", Utterance: "start the slow request", PageID: "checkout", Deadline: time.Second},
			{
				ID: "interrupt", Utterance: "stop that request", PageID: "checkout", Deadline: time.Second,
				Interrupt: &BrowserConversationInterrupt{Trigger: BrowserInterruptOnInFlightInvocation, ToolName: "write_state"},
			},
			{
				ID: "cancel", Utterance: "cancel the session", PageID: "checkout", Deadline: time.Second,
				Cancel: &BrowserConversationCancelRequest{Reason: "customer requested stop"},
			},
		},
		RunTimeout: 3 * time.Second,
		PostSession: BrowserConversationTabStateRequired{
			PageID: "checkout", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func browserConversationInterruptScript() testkit.BrowserScript {
	base := browserConversationRunnerScript()
	operations := append([]testkit.BrowserScriptOperation(nil), base.Operations[:3]...)
	operations = append(operations,
		testkit.BrowserScriptOperation{
			Expect: testkit.OperationExpectation{
				Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state",
				Input: json.RawMessage(`{"value":false}`),
			},
			Result: json.RawMessage(`{"invocation_id":"slow-invocation"}`),
		},
		testkit.BrowserScriptOperation{
			Expect: testkit.OperationExpectation{Type: testkit.OperationCancelTool, InvocationID: "slow-invocation"},
			Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolResponded, InvocationID: "slow-invocation", Status: "Canceled",
				Error: json.RawMessage(`{"code":"invocation_canceled"}`),
			}},
		},
		testkit.BrowserScriptOperation{Expect: testkit.OperationExpectation{Type: testkit.OperationDetachTarget}},
	)
	base.Operations = operations
	return base
}

type browserConversationInterruptInferencer struct {
	mu      sync.Mutex
	toolRef webmcp.ToolRef
	audio   []byte
}

func (i *browserConversationInterruptInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &browserConversationInterruptSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](128),
		done:    make(chan struct{}),
		owner:   i,
		toolRef: i.toolRef,
	}
	if !session.write(ctx,
		messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("fixture-session", "session")},
		messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("fixture-session")},
	) {
		return nil, ctx.Err()
	}
	return session, nil
}

type browserConversationInterruptSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	owner   *browserConversationInterruptInferencer
	toolRef webmcp.ToolRef

	mu          sync.Mutex
	inputTurns  int
	responseEnd int
	closed      bool
}

func (s *browserConversationInterruptSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	switch message.Type {
	case messages.StreamTypeAudioDelta:
		if value, ok := message.Value.(*messages.AudioDeltaValue); ok && value != nil && s.owner != nil {
			s.owner.mu.Lock()
			s.owner.audio = append(s.owner.audio, value.Content...)
			s.owner.mu.Unlock()
		}
	case messages.StreamTypeMessageEnd:
		s.inputTurns++
		switch s.inputTurns {
		case 1:
			return s.write(ctx,
				messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("apply")},
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				s.toolCall("call-apply", `{"value":true}`),
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		case 2:
			return s.write(ctx,
				messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("start the slow request")},
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				s.toolCall("call-slow", `{"value":false}`),
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		case 3:
			return s.write(ctx, messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("stop that request")})
		case 4:
			return s.write(ctx,
				messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("cancel the session")},
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("late response")},
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		}
	case messages.StreamTypeResponseCreate:
		s.responseEnd++
		if s.responseEnd == 1 {
			return s.write(ctx,
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("applied")},
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		}
	case messages.StreamTypeSessionClose:
		s.closed = true
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
	return true
}

func (s *browserConversationInterruptSession) toolCall(id, input string) messages.StreamMessage {
	return messages.StreamMessage{
		Type: messages.StreamTypeToolCallEnd,
		Role: messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(id, webmcp.InvokeToolName,
			`{"tool_ref":"`+string(s.toolRef)+`","input_json":"`+strings.ReplaceAll(input, `"`, `\"`)+`","reason":"browser"}`),
	}
}

func (s *browserConversationInterruptSession) write(ctx context.Context, messagesToWrite ...messages.StreamMessage) bool {
	for _, message := range messagesToWrite {
		if !s.receive.Write(ctx, message) {
			return false
		}
	}
	return true
}

func (s *browserConversationInterruptSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *browserConversationInterruptSession) Done() <-chan struct{} { return s.done }

func (s *browserConversationInterruptSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

func TestRunBrowserConversationAttributesSessionFailureAndCleansOnce(t *testing.T) {
	scenario := browserConversationRunnerScenario()
	script := browserConversationRunnerScript()
	var fixture *BrowserConversationFixtureRun
	result, err := RunBrowserConversation(context.Background(), nil, BrowserConversationRunOptions{
		Scenario:    scenario,
		AudioByStep: map[string][]byte{"apply": {1, 2}},
		FixtureFactory: func(context.Context, BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
			created, createErr := NewBrowserConversationFixtureRun(
				script,
				WithBrowserConversationFixtureBrokerOptions(webmcp.BrokerOptions{
					ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
						return browserConversationRunnerToolRef(), nil
					},
				}),
			)
			fixture = created
			return created, createErr
		},
		Oracle: &browserConversationSequenceOracle{states: []json.RawMessage{json.RawMessage("{\"value\":true}")}},
		PostSessionProbe: func(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error) {
			return BrowserConversationTabStateProbeResult{PageID: "checkout", Alive: true, Responsive: true, AllowsMutation: true}, nil
		},
		SessionRunner: func(context.Context, io.Writer, BrowserConversationSessionRequest) error {
			return errors.New("provider failed Authorization: Bearer secret")
		},
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationSession) {
		t.Fatalf("error = %v, want attributed session failure", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(result.Lifecycle.Error, "secret") {
		t.Fatalf("credential leaked in error/result: %v / %q", err, result.Lifecycle.Error)
	}
	if result.Mechanical.Passed || result.Lifecycle.Outcome != BrowserConversationLifecycleFailed {
		t.Fatalf("result = %+v, want failed mechanical lifecycle", result)
	}
	if fixture == nil || fixture.CloseCount() != 1 {
		t.Fatalf("fixture close count = %v, want one", fixture)
	}
	if validateErr := result.Validate(); validateErr != nil {
		t.Fatalf("final result is invalid: %v", validateErr)
	}
}

func TestRunBrowserConversationRejectsFixtureBeforeSessionAndFinalizesEvidence(t *testing.T) {
	scenario := browserConversationRunnerScenario()
	result, err := RunBrowserConversation(context.Background(), nil, BrowserConversationRunOptions{
		Scenario:    scenario,
		AudioByStep: map[string][]byte{"apply": {1}},
		FixtureScript: testkit.BrowserScript{
			Version: "unsupported",
		},
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationFixtureStartup) {
		t.Fatalf("error = %v, want fixture startup failure", err)
	}
	if !result.Finalized || result.Lifecycle.Outcome != BrowserConversationLifecycleFailed {
		t.Fatalf("result = %+v, want finalized failed result", result)
	}
	if result.Validator.Status != BrowserConversationValidatorNotRun || result.Mechanical.Passed {
		t.Fatalf("result evidence = %+v, want mechanical failure and validator not_run", result)
	}
}

func TestRunBrowserConversationAttributesSessionStartupFailure(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(true)
	result, err := RunBrowserConversation(context.Background(), nil, browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{json.RawMessage("{\"value\":true}")}},
		nil,
	))
	if err == nil || !errors.Is(err, ErrBrowserConversationSessionStartup) {
		t.Fatalf("error = %v, want session startup failure", err)
	}
	if !result.Finalized || result.Lifecycle.SessionStarted != true || result.Lifecycle.SessionTerminated != true {
		t.Fatalf("lifecycle = %+v, want finalized attempted session", result.Lifecycle)
	}
	if fixture.CloseCount() != 1 || broker.closeCount != 1 {
		t.Fatalf("cleanup counts fixture=%d broker=%d, want one each", fixture.CloseCount(), broker.closeCount)
	}
}

func TestRunBrowserConversationReportsMissingTerminalResult(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(false)
	result, err := RunBrowserConversation(context.Background(), nil, browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":true}"),
			json.RawMessage("{\"value\":true}"),
		}},
		browserConversationFailureSession,
	))
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want evidence failure", err)
	}
	if result.Mechanical.Passed || !result.Finalized {
		t.Fatalf("result = %+v, want finalized mechanical failure", result)
	}
	var sawDispatched bool
	for _, call := range result.BrokerCalls {
		if call.Operation == BrowserConversationInvoke && call.State == webmcp.InvocationDispatched && !call.Terminal {
			sawDispatched = true
		}
	}
	if !sawDispatched {
		t.Fatalf("broker calls = %+v, want non-terminal invocation evidence", result.BrokerCalls)
	}
	if fixture.CloseCount() != 1 || broker.closeCount != 1 {
		t.Fatalf("cleanup counts fixture=%d broker=%d, want one each", fixture.CloseCount(), broker.closeCount)
	}
}

func TestRunBrowserConversationKeepsOracleMismatchMechanical(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(true)
	result, err := RunBrowserConversation(context.Background(), nil, browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":true}"),
		}},
		browserConversationFailureSession,
	))
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want oracle evidence failure", err)
	}
	if result.Mechanical.Passed || len(result.Mechanical.Failures) == 0 {
		t.Fatalf("mechanical = %+v, want oracle mismatch", result.Mechanical)
	}
}

func TestRunBrowserConversationRejectsMalformedStreamEvidence(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(true)
	result, err := RunBrowserConversation(context.Background(), nil, browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{json.RawMessage("{\"value\":true}")}},
		func(_ context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
			request.StreamObserver(messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptEnd,
				Role:  messages.RoleUser,
				Value: messages.NewTextDeltaValue("wrong value type"),
			})
			return nil
		},
	))
	if err == nil || !errors.Is(err, ErrBrowserConversationEvidence) {
		t.Fatalf("error = %v, want malformed evidence failure", err)
	}
	if result.Mechanical.Passed || !result.Finalized {
		t.Fatalf("result = %+v, want finalized mechanical failure", result)
	}
	if fixture.CloseCount() != 1 || broker.closeCount != 1 {
		t.Fatalf("cleanup counts fixture=%d broker=%d, want one each", fixture.CloseCount(), broker.closeCount)
	}
}

func TestRunBrowserConversationAttributesTimeoutWithoutStartingFixture(t *testing.T) {
	called := false
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, err := RunBrowserConversation(ctx, nil, BrowserConversationRunOptions{
		Scenario:    browserConversationRunnerScenario(),
		AudioByStep: map[string][]byte{"apply": {1}},
		FixtureFactory: func(context.Context, BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
			called = true
			return nil, errors.New("fixture must not start")
		},
	})
	if err == nil || !errors.Is(err, ErrBrowserConversationTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
	if called || result.Lifecycle.Outcome != BrowserConversationLifecycleTimedOut || !result.Finalized {
		t.Fatalf("called=%v lifecycle=%+v, want timed-out finalized result without fixture", called, result.Lifecycle)
	}
}

func TestRunBrowserConversationAttributesValidatorFailure(t *testing.T) {
	fixture, broker := newBrowserConversationFailureFixture(true)
	options := browserConversationFailureRunOptions(
		fixture,
		broker,
		&browserConversationSequenceOracle{states: []json.RawMessage{
			json.RawMessage("{\"value\":false}"),
			json.RawMessage("{\"value\":true}"),
			json.RawMessage("{\"value\":true}"),
		}},
		browserConversationFailureSession,
	)
	options.Validator = BrowserConversationValidatorFunc(func(BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
		return BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorFail,
			Passed:  false,
			Summary: "validator found a semantic concern",
		}, nil
	})
	result, err := RunBrowserConversation(context.Background(), nil, options)
	if err == nil || !errors.Is(err, ErrBrowserConversationValidator) {
		t.Fatalf("error = %v, want validator failure", err)
	}
	if result.Mechanical.Passed == false || result.Validator.Status != BrowserConversationValidatorFail || result.Validator.Passed {
		t.Fatalf("result = %+v, want mechanical pass and failed validator", result)
	}
	if fixture.CloseCount() != 1 || broker.closeCount != 1 {
		t.Fatalf("cleanup counts fixture=%d broker=%d, want one each", fixture.CloseCount(), broker.closeCount)
	}
}

type browserConversationSequenceOracle struct {
	mu     sync.Mutex
	states []json.RawMessage
	next   int
}

func (o *browserConversationSequenceOracle) ReadBrowserConversationState(context.Context, string) (json.RawMessage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.next >= len(o.states) {
		return nil, errors.New("oracle was read more times than scripted")
	}
	state := append(json.RawMessage(nil), o.states[o.next]...)
	o.next++
	return state, nil
}

func browserConversationRunnerScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion,
		ID:      "discount-flow",
		Name:    "Discount flow",
		Fixture: BrowserConversationFixture{
			ID:          "shop",
			Pages:       []BrowserConversationPage{{ID: "checkout", URL: "https://fixture.test/checkout"}},
			InitialPage: "checkout",
		},
		Steps: []BrowserConversationStep{{
			ID:        "apply",
			Utterance: "apply",
			PageID:    "checkout",
			ExpectedState: &BrowserStateTransition{
				PageID: "checkout",
				Before: json.RawMessage("{\"value\":false}"),
				After:  json.RawMessage("{\"value\":true}"),
			},
			Deadline: 2 * time.Second,
		}},
		RunTimeout: 3 * time.Second,
		PostSession: BrowserConversationTabStateRequired{
			PageID: "checkout", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func browserConversationRunnerScript() testkit.BrowserScript {
	return testkit.BrowserScript{
		Version: testkit.BrowserScriptVersion,
		Endpoint: testkit.BrowserEndpoint{
			Version: testkit.EndpointVersionInfo{
				Browser:              "Chrome/Fixture",
				ProtocolVersion:      "1.3",
				WebSocketDebuggerURL: "ws://fixture/browser",
			},
			Targets: []testkit.BrowserTarget{{
				ID:                   "tab-1",
				Type:                 "page",
				Title:                "Checkout",
				URL:                  "https://fixture.test/checkout",
				WebSocketDebuggerURL: "ws://fixture/page/tab-1",
			}},
		},
		Operations: []testkit.BrowserScriptOperation{
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolsAdded,
				Tools: []testkit.ToolDescriptor{{
					Name:        "write_state",
					Description: "Write fixture state",
					FrameID:     "frame-1",
					InputSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"boolean\"}},\"required\":[\"value\"],\"additionalProperties\":false}"),
				}},
			}}},
			{
				Expect: testkit.OperationExpectation{
					Type:     testkit.OperationInvokeTool,
					FrameID:  "frame-1",
					ToolName: "write_state",
					Input:    json.RawMessage("{\"value\":true}"),
				},
				Result: json.RawMessage("{\"invocation_id\":\"browser-invocation\"}"),
				Emit: []testkit.EmittedEvent{{
					Type:         testkit.EmittedToolResponded,
					InvocationID: "browser-invocation",
					Status:       "Completed",
					Output:       json.RawMessage("{\"ok\":true}"),
				}},
			},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationDetachTarget}},
		},
	}
}

func browserConversationRunnerToolRef() webmcp.ToolRef {
	return webmcp.ToolRef(webmcp.ToolRefPrefix + "AAAAAAAAAAAAAAAAAAAAAA")
}

func browserConversationFailureRunOptions(
	fixture *BrowserConversationFixtureRun,
	_ *browserConversationFailureBroker,
	oracle BrowserConversationOracleReader,
	sessionRunner BrowserConversationSessionRunner,
) BrowserConversationRunOptions {
	return BrowserConversationRunOptions{
		Scenario:    browserConversationRunnerScenario(),
		AudioByStep: map[string][]byte{"apply": {1, 2, 3}},
		FixtureFactory: func(context.Context, BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
			return fixture, nil
		},
		Oracle:        oracle,
		SessionRunner: sessionRunner,
		PostSessionProbe: func(context.Context, *BrowserConversationFixtureRun, string) (BrowserConversationTabStateProbeResult, error) {
			return BrowserConversationTabStateProbeResult{PageID: "checkout", Alive: true, Responsive: true, AllowsMutation: true}, nil
		},
	}
}

func browserConversationFailureSession(ctx context.Context, _ io.Writer, request BrowserConversationSessionRequest) error {
	if request.StreamObserver == nil || request.ToolExecutor == nil {
		return errors.New("shared session seams were not composed")
	}
	request.StreamObserver(messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptEnd,
		Role:  messages.RoleUser,
		Value: messages.NewTranscriptEndValue("apply"),
	})
	arguments := "{\"tool_ref\":\"" + string(browserConversationRunnerToolRef()) + "\",\"input_json\":\"{\\\"value\\\":true}\",\"reason\":\"apply\"}"
	if _, err := request.ToolExecutor.Execute(ctx, messages.ToolCall{
		ID: "call-1", Name: webmcp.InvokeToolName, Arguments: arguments,
	}); err != nil {
		return err
	}
	request.StreamObserver(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})
	request.StreamObserver(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("discount applied"),
	})
	request.StreamObserver(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	return nil
}

type browserConversationFailureBroker struct {
	terminal   bool
	closeCount int
}

func newBrowserConversationFailureFixture(terminal bool) (*BrowserConversationFixtureRun, *browserConversationFailureBroker) {
	broker := &browserConversationFailureBroker{terminal: terminal}
	return &BrowserConversationFixtureRun{Broker: broker}, broker
}

func (b *browserConversationFailureBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return []webmcp.BrowserCandidate{{ID: "fixture-browser", Explicit: true}}, nil
}

func (b *browserConversationFailureBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return []webmcp.Target{{BrowserID: "fixture-browser", ID: "tab-1", Generation: 1, Eligible: true}}, nil
}

func (b *browserConversationFailureBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	return webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: "fixture-browser", TargetID: "tab-1"},
		Generation: 1,
		Connected:  true,
		Ready:      true,
	}, nil
}

func (b *browserConversationFailureBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.Select(context.Background(), webmcp.TargetSelector{})
}

func (b *browserConversationFailureBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return webmcp.ToolCatalogSnapshot{
		Context: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "fixture-browser", TargetID: "tab-1"},
			Generation: 1,
			Connected:  true,
			Ready:      true,
		},
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{{
			Ref:         browserConversationRunnerToolRef(),
			Name:        "write_state",
			Description: "Write fixture state",
			FrameID:     "frame-1",
			InputSchema: json.RawMessage("{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"boolean\"}}}"),
		}},
	}, nil
}

func (b *browserConversationFailureBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{InvocationID: "missing-invocation", State: webmcp.InvocationDispatched}, nil
}

func (b *browserConversationFailureBroker) WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error) {
	if !b.terminal {
		return webmcp.InvokeResult{}, errors.New("terminal result unavailable")
	}
	return webmcp.InvokeResult{
		InvocationID: "missing-invocation",
		State:        webmcp.InvocationCompleted,
		Output:       json.RawMessage("{\"ok\":true}"),
	}, nil
}

func (b *browserConversationFailureBroker) Cancel(context.Context, webmcp.CancelRequest) error {
	return nil
}

func (b *browserConversationFailureBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	events := make(chan webmcp.BrokerEvent)
	close(events)
	return events
}

func (b *browserConversationFailureBroker) Close() error {
	b.closeCount++
	return nil
}

type browserConversationSessionInferencer struct {
	mu      sync.Mutex
	toolRef webmcp.ToolRef
	audio   []byte
}

func (i *browserConversationSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &browserConversationSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
		toolRef: i.toolRef,
		owner:   i,
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("fixture-session", "session"),
	}) {
		return nil, ctx.Err()
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("fixture-session"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type browserConversationSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	toolRef webmcp.ToolRef
	owner   *browserConversationSessionInferencer
	mu      sync.Mutex
	once    sync.Once
	first   bool
	final   bool
}

func (s *browserConversationSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch message.Type {
	case messages.StreamTypeAudioDelta:
		if value, ok := message.Value.(*messages.AudioDeltaValue); ok && value != nil && s.owner != nil {
			s.owner.mu.Lock()
			s.owner.audio = append(s.owner.audio, value.Content...)
			s.owner.mu.Unlock()
		}
	case messages.StreamTypeMessageEnd:
		if !s.first {
			s.first = true
			return s.write(ctx,
				messages.StreamMessage{
					Type:  messages.StreamTypeTranscriptEnd,
					Role:  messages.RoleUser,
					Value: messages.NewTranscriptEndValue("apply"),
				},
				messages.StreamMessage{
					Type:  messages.StreamTypeMessageStart,
					Role:  messages.RoleAssistant,
					Value: messages.NewMessageStartValue(),
				},
				messages.StreamMessage{
					Type: messages.StreamTypeToolCallEnd,
					Role: messages.RoleAssistant,
					Value: messages.NewToolCallEndValue(
						"call-1", webmcp.InvokeToolName,
						"{\"tool_ref\":\""+string(s.toolRef)+"\",\"input_json\":\"{\\\"value\\\":true}\",\"reason\":\"apply\"}",
					),
				},
				messages.StreamMessage{
					Type:  messages.StreamTypeMessageEnd,
					Role:  messages.RoleAssistant,
					Value: messages.NewMessageEndValue(messages.TokenUsage{}),
				},
			)
		}
	case messages.StreamTypeResponseCreate:
		if !s.final {
			s.final = true
			return s.write(ctx,
				messages.StreamMessage{
					Type:  messages.StreamTypeMessageStart,
					Role:  messages.RoleAssistant,
					Value: messages.NewMessageStartValue(),
				},
				messages.StreamMessage{
					Type:  messages.StreamTypeTextDelta,
					Role:  messages.RoleAssistant,
					Value: messages.NewTextDeltaValue("discount applied"),
				},
				messages.StreamMessage{
					Type:  messages.StreamTypeMessageEnd,
					Role:  messages.RoleAssistant,
					Value: messages.NewMessageEndValue(messages.TokenUsage{}),
				},
			)
		}
	case messages.StreamTypeSessionClose:
		s.once.Do(func() { close(s.done) })
	}
	return true
}

func (s *browserConversationSession) write(ctx context.Context, messagesToWrite ...messages.StreamMessage) bool {
	for _, message := range messagesToWrite {
		if !s.receive.Write(ctx, message) {
			return false
		}
	}
	return true
}

func (s *browserConversationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *browserConversationSession) Done() <-chan struct{} {
	return s.done
}

func (s *browserConversationSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
