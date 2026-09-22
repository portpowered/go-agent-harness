package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func TestServiceRunExecutesAndReportsAdmittedScenario(t *testing.T) {
	service := NewService()
	scenario := testBrowserConversationScenario()
	pcm := []byte{0, 1, 0xff, 0x7f}
	fixture := &behaviorFixture{}
	broker := &behaviorBroker{fixture: fixture}
	called := false

	result, err := service.Run(context.Background(), browserconversation.RunRequest{
		Scenario: scenario, AudioByStep: map[string][]byte{"step-1": pcm},
		Broker: broker, Fixture: fixture, Oracle: fixture,
		SessionRunner: func(ctx context.Context, _ io.Writer, request browserconversation.SessionRequest) error {
			called = true
			if len(request.AudioInputs) != 1 || !bytes.Equal(request.AudioInputs[0].PCM, pcm) {
				return errors.New("session runner received different scheduled PCM")
			}
			request.StreamObserver(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue(scenario.Steps[0].Utterance)})
			invocation, invokeErr := request.Broker.Invoke(ctx, browserconversation.BrowserInvokeRequest{ToolRef: "tool-1", Input: json.RawMessage(`{"value":"after"}`)})
			if invokeErr != nil {
				return invokeErr
			}
			if invocation.State != "completed" || invocation.InvocationID == "" {
				return errors.New("browser invocation did not complete")
			}
			request.StreamObserver(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
			request.StreamObserver(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("The label was updated.")})
			request.StreamObserver(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called || !result.Finalized || result.ScenarioID != scenario.ID || !result.Mechanical.Passed {
		t.Fatalf("run result called=%t finalized=%t scenario=%q mechanical=%+v", called, result.Finalized, result.ScenarioID, result.Mechanical)
	}
	if len(result.Oracles) != 3 || result.Oracles[0].Phase != browserconversation.BrowserConversationOracleBefore || result.Oracles[1].Phase != browserconversation.BrowserConversationOracleAfter || result.Oracles[2].Phase != browserconversation.BrowserConversationOraclePostSession {
		t.Fatalf("oracle evidence order = %+v", result.Oracles)
	}
	if fixture.closeCalls != 1 || fixture.probeCalls != 1 {
		t.Fatalf("fixture close/probe calls = %d/%d, want 1/1", fixture.closeCalls, fixture.probeCalls)
	}

	metadata := browserconversation.BrowserConversationReportMetadata{Command: "public behavior test"}
	var output bytes.Buffer
	if err := service.WriteReport(&output, result, metadata); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	for _, expected := range []string{scenario.ID, scenario.Name, metadata.Command} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("rendered report omitted %q: %s", expected, output.String())
		}
	}
}

func TestServiceParsesScenarioJSONAndKeepsParserFailuresPrivate(t *testing.T) {
	service := NewService()
	want := testBrowserConversationScenario()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal scenario: %v", err)
	}
	got, err := service.ParseScenarioJSON(data)
	if err != nil {
		t.Fatalf("ParseScenarioJSON: %v", err)
	}
	if got.RunTimeout != want.RunTimeout || len(got.Steps) != len(want.Steps) || got.Steps[0].Deadline != want.Steps[0].Deadline {
		t.Fatalf("parsed durations = run %s, step %s; want %s, %s", got.RunTimeout, got.Steps[0].Deadline, want.RunTimeout, want.Steps[0].Deadline)
	}

	withUnknownField := append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"unexpected":"field"}`)...)
	if _, err := service.ParseScenarioJSON(withUnknownField); !errors.Is(err, browserconversation.ErrInvalidBrowserConversationScenario) {
		t.Fatalf("unknown-field parse error = %v, want invalid-scenario error", err)
	}

	withSensitiveDuration := strings.Replace(string(data), `"run_timeout":"2s"`, `"run_timeout":"credential-canary"`, 1)
	if _, err := service.ParseScenarioJSON([]byte(withSensitiveDuration)); !errors.Is(err, browserconversation.ErrInvalidBrowserConversationScenario) || strings.Contains(err.Error(), "credential-canary") {
		t.Fatalf("invalid-duration parse error = %v, want sanitized invalid-scenario error", err)
	}
}

func TestServiceRejectsCredentialBearingScenarioBeforeFixtureCreation(t *testing.T) {
	scenario := testBrowserConversationScenario()
	scenario.Fixture.Pages[0].URL = "https://customer:credential-canary@fixture.test/"
	created := false
	_, err := NewService().Run(context.Background(), browserconversation.RunRequest{
		Scenario: scenario,
		FixtureFactory: func(context.Context, browserconversation.BrowserConversationScenario) (browserconversation.Fixture, error) {
			created = true
			return &behaviorFixture{}, nil
		},
	})
	if !errors.Is(err, browserconversation.ErrInvalidBrowserConversationScenario) || created {
		t.Fatalf("Run err=%v fixture-created=%t, want credential rejection before effects", err, created)
	}
	if bytes.Contains([]byte(err.Error()), []byte("credential-canary")) {
		t.Fatalf("scenario rejection exposed credential-shaped input: %v", err)
	}
}

func TestServiceScheduleAudioInputsPreservesPCMBytes(t *testing.T) {
	service := NewService()
	scenario := testBrowserConversationScenario()
	pcm := []byte{0, 0x80, 0xff, 0x7f}
	inputs, err := service.ScheduleAudioInputs(scenario, map[string][]byte{"step-1": pcm})
	if err != nil {
		t.Fatalf("ScheduleAudioInputs: %v", err)
	}
	if len(inputs) != 1 || inputs[0].AfterCompletedTurns != 0 || !inputs[0].EndOfTurn || !bytes.Equal(inputs[0].PCM, pcm) {
		t.Fatalf("scheduled audio = %+v", inputs)
	}
	pcm[0] = 0x55
	if inputs[0].PCM[0] != 0 {
		t.Fatalf("scheduled PCM aliases caller bytes: %v", inputs[0].PCM)
	}
	inputs[0].PCM[1] = 0x44
	if pcm[1] != 0x80 {
		t.Fatalf("caller PCM changed through scheduled value: %v", pcm)
	}
}

func TestServiceRunClosesFixtureAfterSessionFailure(t *testing.T) {
	fixture := &behaviorFixture{}
	runnerErr := errors.New("session runner failed")
	result, err := NewService().Run(context.Background(), browserconversation.RunRequest{
		Scenario: testBrowserConversationScenario(), AudioByStep: map[string][]byte{"step-1": {1, 2}},
		Broker: &behaviorBroker{fixture: fixture}, Fixture: fixture, Oracle: fixture,
		SessionRunner: func(context.Context, io.Writer, browserconversation.SessionRequest) error { return runnerErr },
	})
	if !errors.Is(err, browserconversation.ErrBrowserConversationSession) || !errors.Is(err, runnerErr) {
		t.Fatalf("Run err=%v, want typed session failure and root cause", err)
	}
	if !result.Finalized || fixture.closeCalls != 1 || fixture.probeCalls != 1 {
		t.Fatalf("failure result finalized=%t close/probe=%d/%d", result.Finalized, fixture.closeCalls, fixture.probeCalls)
	}
}

func TestServiceRunBoundsPostSessionObservationsAfterCancellation(t *testing.T) {
	waitForCleanupDeadline := func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("post-session cleanup context has no deadline")
		}
		if err := ctx.Err(); err != nil {
			return errors.New("post-session cleanup inherited caller cancellation")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return errors.New("post-session cleanup deadline did not bound observation")
		}
	}
	tests := []struct {
		name      string
		configure func(*browserconversation.RunRequest)
	}{
		{
			name: "browser probe",
			configure: func(request *browserconversation.RunRequest) {
				request.PostSessionProbe = func(ctx context.Context, _ browserconversation.Fixture, pageID string) (browserconversation.BrowserConversationTabStateProbeResult, error) {
					return browserconversation.BrowserConversationTabStateProbeResult{PageID: pageID}, waitForCleanupDeadline(ctx)
				}
			},
		},
		{
			name: "oracle read",
			configure: func(request *browserconversation.RunRequest) {
				request.PostSessionProbe = func(_ context.Context, _ browserconversation.Fixture, pageID string) (browserconversation.BrowserConversationTabStateProbeResult, error) {
					return browserconversation.BrowserConversationTabStateProbeResult{PageID: pageID}, nil
				}
				request.Oracle = blockingOracle{readState: func(ctx context.Context) error {
					return waitForCleanupDeadline(ctx)
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := testBrowserConversationScenario()
			scenario.RunTimeout = 100 * time.Millisecond
			scenario.Steps[0].Deadline = 50 * time.Millisecond
			fixture := &behaviorFixture{}
			callerContext, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			request := browserconversation.RunRequest{
				Scenario:    scenario,
				AudioByStep: map[string][]byte{"step-1": {1, 2}},
				Broker:      &behaviorBroker{fixture: fixture},
				Fixture:     fixture,
				SessionRunner: func(context.Context, io.Writer, browserconversation.SessionRequest) error {
					cancelCaller()
					return nil
				},
			}
			test.configure(&request)
			started := time.Now()
			_, err := NewService().Run(callerContext, request)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("Run took %s, want bounded post-session cleanup", elapsed)
			}
			if !errors.Is(err, browserconversation.ErrBrowserConversationCleanup) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run error = %v, want bounded cleanup deadline failure", err)
			}
		})
	}
}

func TestServiceRunBoundsFixtureCloseAfterCancellation(t *testing.T) {
	scenario := testBrowserConversationScenario()
	scenario.RunTimeout = 100 * time.Millisecond
	scenario.Steps[0].Deadline = 50 * time.Millisecond
	fixture := &behaviorFixture{close: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("fixture close context has no deadline")
		}
		if err := ctx.Err(); err != nil {
			return errors.New("fixture close inherited caller cancellation")
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	callerContext, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	started := time.Now()
	_, err := NewService().Run(callerContext, browserconversation.RunRequest{
		Scenario: scenario, AudioByStep: map[string][]byte{"step-1": {1, 2}},
		Broker: &behaviorBroker{fixture: fixture}, Fixture: fixture,
		SessionRunner: func(context.Context, io.Writer, browserconversation.SessionRequest) error {
			cancelCaller()
			return nil
		},
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run took %s, want bounded fixture shutdown", elapsed)
	}
	if !errors.Is(err, browserconversation.ErrBrowserConversationCleanup) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want bounded fixture-close cleanup failure", err)
	}
}

type blockingOracle struct {
	readState func(context.Context) error
}

func (o blockingOracle) ReadState(ctx context.Context, _ string) (json.RawMessage, error) {
	if err := o.readState(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(`{}`), nil
}

func TestServiceDerivesOrderedCorrectionAndNavigationRecovery(t *testing.T) {
	service := NewService()
	scenario := testBrowserConversationScenario()
	scenario.Steps[0].ID = "original"
	scenario.Steps[0].Utterance = "Set the label to old."
	scenario.Steps[0].ExpectedState = &browserconversation.BrowserStateTransition{PageID: "home", Before: json.RawMessage(`{"value":"before"}`), After: json.RawMessage(`{"value":"old"}`)}
	scenario.Fixture.Pages = append(scenario.Fixture.Pages, browserconversation.BrowserConversationPage{ID: "settings", URL: "https://fixture.test/settings"})
	scenario.Steps = append(scenario.Steps,
		browserconversation.BrowserConversationStep{ID: "correction", Utterance: "Actually set the label to new.", PageID: "home", Deadline: time.Second,
			Correction: &browserconversation.BrowserConversationCorrection{TargetStepID: "original", ExpectedState: browserconversation.BrowserStateTransition{PageID: "home", Before: json.RawMessage(`{"value":"old"}`), After: json.RawMessage(`{"value":"new"}`)}}},
		browserconversation.BrowserConversationStep{ID: "recovery", Utterance: "Open settings.", PageID: "settings", Deadline: time.Second,
			Navigation: &browserconversation.BrowserCustomerNavigation{FromPageID: "home", ToPageID: "settings", URL: "https://fixture.test/settings"}},
	)
	result := browserconversation.BrowserConversationResult{
		Turns: []browserconversation.BrowserConversationTurn{
			{Sequence: 1, StepID: "original", Direction: browserconversation.BrowserConversationCustomerTurn, ObservedText: "Set the label to old.", Complete: true},
			{Sequence: 3, StepID: "original", Direction: browserconversation.BrowserConversationAssistantTurn, ObservedText: "Changed to old.", Complete: true},
			{Sequence: 4, StepID: "correction", Direction: browserconversation.BrowserConversationCustomerTurn, ObservedText: "Actually set the label to new.", Complete: true},
			{Sequence: 6, StepID: "correction", Direction: browserconversation.BrowserConversationAssistantTurn, ObservedText: "Changed to new.", Complete: true},
		},
		BrokerCalls: []browserconversation.BrowserConversationBrokerCall{
			{Sequence: 2, StepID: "original", Operation: browserconversation.BrowserConversationInvoke, InvocationID: "invoke-original", ToolRef: "label", ToolName: "set_label", State: "completed", Terminal: true},
			{Sequence: 5, StepID: "correction", Operation: browserconversation.BrowserConversationInvoke, InvocationID: "invoke-correction", ToolRef: "label", ToolName: "set_label", State: "completed", Terminal: true},
			{Sequence: 7, StepID: "recovery", Operation: browserconversation.BrowserConversationCustomerNavigate, Generation: 2, PreviousGeneration: 1},
			{Sequence: 8, StepID: "recovery", Operation: browserconversation.BrowserConversationInvoke, InvocationID: "invoke-stale", ToolRef: "stale-ref", ToolName: "set_priority", State: "error", Terminal: true, ErrorCode: "stale_tool_ref", Generation: 1},
			{Sequence: 9, StepID: "recovery", Operation: browserconversation.BrowserConversationListTools, ToolRefs: []string{"fresh-ref"}, Generation: 2},
			{Sequence: 10, StepID: "recovery", Operation: browserconversation.BrowserConversationInvoke, InvocationID: "invoke-fresh", ToolRef: "fresh-ref", ToolName: "set_priority", State: "completed", Terminal: true, Generation: 2},
		},
		Oracles: []browserconversation.BrowserConversationOracleSnapshot{
			{Sequence: 11, StepID: "original", PageID: "home", Phase: browserconversation.BrowserConversationOracleBefore, State: json.RawMessage(`{"value":"before"}`)},
			{Sequence: 12, StepID: "original", PageID: "home", Phase: browserconversation.BrowserConversationOracleAfter, State: json.RawMessage(`{"value":"old"}`)},
			{Sequence: 13, StepID: "correction", PageID: "home", Phase: browserconversation.BrowserConversationOracleBefore, State: json.RawMessage(`{"value":"old"}`)},
			{Sequence: 14, StepID: "correction", PageID: "home", Phase: browserconversation.BrowserConversationOracleAfter, State: json.RawMessage(`{"value":"new"}`)},
		},
	}

	corrections := service.DeriveCorrections(scenario, result)
	if len(corrections) != 1 || !corrections[0].Passed || corrections[0].TargetStepID != "original" || corrections[0].OriginalInvocationID != "invoke-original" || corrections[0].CorrectionInvocationID != "invoke-correction" {
		t.Fatalf("correction evidence = %+v", corrections)
	}
	recoveries := service.DeriveRecovery(scenario, result)
	if len(recoveries) != 1 || !recoveries[0].Passed || recoveries[0].StaleToolRef != "stale-ref" || recoveries[0].FreshToolRef != "fresh-ref" || recoveries[0].PreviousGeneration != 1 || recoveries[0].CurrentGeneration != 2 {
		t.Fatalf("recovery evidence = %+v", recoveries)
	}

	result.ScenarioID, result.ScenarioName = scenario.ID, scenario.Name
	result.Corrections, result.Recovery = corrections, recoveries
	result.BrokerCalls = nil
	evaluation, err := service.Evaluate(scenario, result, nil)
	if err != nil {
		t.Fatalf("Evaluate forged evidence: %v", err)
	}
	failures := strings.Join(evaluation.Failures, "\n")
	if evaluation.Passed || !strings.Contains(failures, "correction lacks a completed terminal invocation") || !strings.Contains(failures, "customer navigation was not observed") {
		t.Fatalf("evaluation trusted supplied correction/recovery claims: %+v", evaluation)
	}
}

type behaviorFixture struct {
	after      bool
	closeCalls int
	probeCalls int
	close      func(context.Context) error
}

func (f *behaviorFixture) Close(ctx context.Context) error {
	f.closeCalls++
	if f.close != nil {
		return f.close(ctx)
	}
	return nil
}

func (f *behaviorFixture) Navigate(context.Context, browserconversation.BrowserCustomerNavigation) error {
	return nil
}

func (f *behaviorFixture) ReadState(_ context.Context, pageID string) (json.RawMessage, error) {
	if pageID != "home" {
		return nil, errors.New("unexpected page")
	}
	if f.after {
		return json.RawMessage(`{"value":"after"}`), nil
	}
	return json.RawMessage(`{"value":"before"}`), nil
}

func (f *behaviorFixture) ProbeTab(_ context.Context, pageID string) (browserconversation.BrowserConversationTabStateProbeResult, error) {
	f.probeCalls++
	return browserconversation.BrowserConversationTabStateProbeResult{PageID: pageID, BrowserID: "browser-1", TargetID: "home", Alive: true, Responsive: true, AllowsMutation: true, ReadSucceeded: true, MutationSucceeded: true}, nil
}

type behaviorBroker struct{ fixture *behaviorFixture }

func (b *behaviorBroker) Discover(context.Context, browserconversation.BrowserDiscoveryOptions) ([]browserconversation.BrowserCandidate, error) {
	return []browserconversation.BrowserCandidate{{ID: "browser-1"}}, nil
}

func (b *behaviorBroker) ListTargets(context.Context, browserconversation.BrowserSelector) ([]browserconversation.BrowserTarget, error) {
	return []browserconversation.BrowserTarget{{BrowserID: "browser-1", ID: "home"}}, nil
}

func (b *behaviorBroker) Select(_ context.Context, selector browserconversation.BrowserTargetSelector) (browserconversation.BrowserPageContext, error) {
	return browserconversation.BrowserPageContext{BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: 1, Connected: true}, nil
}

func (b *behaviorBroker) Selected(context.Context) (browserconversation.BrowserPageContext, error) {
	return browserconversation.BrowserPageContext{BrowserID: "browser-1", TargetID: "home", Generation: 1, Connected: true}, nil
}

func (b *behaviorBroker) ListTools(context.Context, browserconversation.BrowserListToolsOptions) (browserconversation.BrowserToolCatalog, error) {
	return browserconversation.BrowserToolCatalog{Context: browserconversation.BrowserPageContext{BrowserID: "browser-1", TargetID: "home", Generation: 1, Connected: true}, Generation: 1, Tools: []browserconversation.BrowserToolDescriptor{{Ref: "tool-1", Name: "set_value", InputSchema: json.RawMessage(`{"type":"object"}`), Generation: 1}}}, nil
}

func (b *behaviorBroker) Invoke(_ context.Context, request browserconversation.BrowserInvokeRequest) (browserconversation.BrowserInvokeResult, error) {
	b.fixture.after = true
	return browserconversation.BrowserInvokeResult{InvocationID: "invocation-1", State: "completed", Output: json.RawMessage(`{"ok":true}`)}, nil
}

func (*behaviorBroker) Cancel(context.Context, browserconversation.BrowserCancelRequest) error {
	return nil
}
func (*behaviorBroker) Watch(context.Context) <-chan browserconversation.BrowserEvent { return nil }
func (*behaviorBroker) Close() error                                                  { return nil }
