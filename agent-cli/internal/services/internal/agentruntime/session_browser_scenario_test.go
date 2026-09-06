package agentruntime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func validBrowserConversationScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion,
		ID:      "customer-browser-flow",
		Name:    "customer browser flow",
		Fixture: BrowserConversationFixture{
			ID:          "customer-fixture",
			InitialPage: "home",
			Pages: []BrowserConversationPage{
				{ID: "home", URL: "http://fixture.test/home"},
				{ID: "settings", URL: "http://fixture.test/settings"},
			},
		},
		RunTimeout: 5 * time.Second,
		Steps: []BrowserConversationStep{
			{
				ID:        "inspect",
				Utterance: "Please inspect the current page and set the label to blue.",
				PageID:    "home",
				Deadline:  time.Second,
				ExpectedState: &BrowserStateTransition{
					PageID: "home", Before: json.RawMessage(`{"label":"unset"}`), After: json.RawMessage(`{"label":"blue"}`),
				},
			},
			{
				ID:        "navigate",
				Utterance: "Now open settings.",
				PageID:    "home",
				Deadline:  time.Second,
				Navigation: &BrowserCustomerNavigation{
					FromPageID: "home", ToPageID: "settings", URL: "http://fixture.test/settings",
				},
			},
			{
				ID:        "correct",
				Utterance: "Actually undo the blue label and set it back to unset.",
				PageID:    "settings",
				Deadline:  time.Second,
				Correction: &BrowserConversationCorrection{
					TargetStepID: "inspect",
					ExpectedState: BrowserStateTransition{
						PageID: "home", Before: json.RawMessage(`{"label":"blue"}`), After: json.RawMessage(`{"label":"unset"}`),
					},
				},
			},
			{
				ID:        "interrupt",
				Utterance: "Stop that request; I need to cancel.",
				PageID:    "settings",
				Deadline:  time.Second,
				Interrupt: &BrowserConversationInterrupt{Trigger: BrowserInterruptOnInFlightInvocation, ToolName: "write_setting"},
			},
			{
				ID:        "cancel",
				Utterance: "Cancel the session now.",
				PageID:    "settings",
				Deadline:  time.Second,
				Cancel:    &BrowserConversationCancelRequest{Reason: "customer requested stop"},
			},
		},
		PostSession: BrowserConversationTabStateRequired{
			PageID: "settings", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func TestNewBrowserConversationScenarioValidatesAndCopiesContract(t *testing.T) {
	scenario := validBrowserConversationScenario()
	validated, err := NewBrowserConversationScenario(scenario)
	if err != nil {
		t.Fatalf("NewBrowserConversationScenario: %v", err)
	}
	if err := validated.Validate(); err != nil {
		t.Fatalf("validated scenario: %v", err)
	}

	scenario.Fixture.Pages[0].ID = "mutated"
	scenario.Steps[0].ExpectedState.After[2] = 'x'
	if validated.Fixture.Pages[0].ID != "home" {
		t.Fatalf("validated fixture shares caller pages: %#v", validated.Fixture.Pages)
	}
	if string(validated.Steps[0].ExpectedState.After) != `{"label":"blue"}` {
		t.Fatalf("validated state shares caller bytes: %s", validated.Steps[0].ExpectedState.After)
	}

	encoded, err := json.Marshal(validated)
	if err != nil {
		t.Fatalf("marshal scenario: %v", err)
	}
	encodedText := string(encoded)
	for _, want := range []string{`"version":"` + BrowserConversationScenarioVersion + `"`, `"run_timeout":"5s"`, `"deadline":"1s"`, `"expected_state"`, `"navigation"`, `"correction"`, `"interrupt"`, `"cancel"`, `"post_session"`} {
		if !strings.Contains(encodedText, want) {
			t.Fatalf("scenario JSON missing %q: %s", want, encodedText)
		}
	}
	for _, forbidden := range []string{"api_key", "credentials", "authorization", "sk-"} {
		if strings.Contains(strings.ToLower(encodedText), forbidden) {
			t.Fatalf("scenario JSON contains forbidden credential marker %q: %s", forbidden, encodedText)
		}
	}

	var decoded BrowserConversationScenario
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal scenario: %v", err)
	}
	if decoded.RunTimeout != 5*time.Second || decoded.Steps[0].Deadline != time.Second {
		t.Fatalf("decoded bounds = (%s, %s)", decoded.RunTimeout, decoded.Steps[0].Deadline)
	}
	if decoded.Steps[2].Correction == nil || decoded.Steps[3].Interrupt == nil || decoded.Steps[4].Cancel == nil {
		t.Fatalf("decoded special steps lost: %#v", decoded.Steps)
	}
}

func TestBrowserConversationScenarioSchedulesThroughSharedAudioContract(t *testing.T) {
	scenario := validBrowserConversationScenario()
	pcm := map[string][]byte{
		"inspect":   {1, 2},
		"navigate":  {3, 4},
		"correct":   {5, 6},
		"interrupt": {7, 8},
		"cancel":    {9, 10},
	}
	inputs, err := scenario.ScheduleAudioInputs(pcm)
	if err != nil {
		t.Fatalf("ScheduleAudioInputs: %v", err)
	}
	if len(inputs) != len(scenario.Steps) {
		t.Fatalf("scheduled inputs = %d, want %d", len(inputs), len(scenario.Steps))
	}
	for index, input := range inputs {
		if input.AfterCompletedTurns != index || !input.EndOfTurn || len(input.PCM) != 2 || input.PCM[0] != byte(index*2+1) {
			t.Fatalf("scheduled input %d = %#v", index, input)
		}
	}
	pcm["inspect"][0] = 99
	if inputs[0].PCM[0] != 1 {
		t.Fatal("scheduled audio shares caller bytes")
	}
	delete(pcm, "cancel")
	if _, err := scenario.ScheduleAudioInputs(pcm); err == nil || !strings.Contains(err.Error(), "steps[4].audio") {
		t.Fatalf("missing audio error = %v", err)
	}
}

func TestBrowserConversationScenarioRejectsInvalidAdmissionBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name string
		edit func(*BrowserConversationScenario)
		path string
	}{
		{name: "version", edit: func(s *BrowserConversationScenario) { s.Version = "" }, path: "version"},
		{name: "run bound", edit: func(s *BrowserConversationScenario) { s.RunTimeout = 0 }, path: "run_timeout"},
		{name: "fixture page", edit: func(s *BrowserConversationScenario) { s.Steps[0].PageID = "missing" }, path: "steps[0].page_id"},
		{name: "step bound", edit: func(s *BrowserConversationScenario) { s.Steps[1].Deadline = 0 }, path: "steps[1].deadline"},
		{name: "state JSON", edit: func(s *BrowserConversationScenario) { s.Steps[0].ExpectedState.After = json.RawMessage(`{`) }, path: "steps[0].expected_state.after"},
		{name: "navigation URL", edit: func(s *BrowserConversationScenario) { s.Steps[1].Navigation.URL = "" }, path: "steps[1].navigation.url"},
		{name: "correction order", edit: func(s *BrowserConversationScenario) { s.Steps[2].Correction.TargetStepID = "cancel" }, path: "steps[2].correction.target_step_id"},
		{name: "interrupt trigger", edit: func(s *BrowserConversationScenario) { s.Steps[3].Interrupt.Trigger = "after_sleep" }, path: "steps[3].interrupt.trigger"},
		{name: "post-session ownership", edit: func(s *BrowserConversationScenario) { s.PostSession.MustRemainAlive = false }, path: "post_session.must_remain_alive"},
		{name: "credential text", edit: func(s *BrowserConversationScenario) { s.Steps[0].Utterance = "use Authorization: Bearer abc" }, path: "steps[0].utterance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := validBrowserConversationScenario()
			test.edit(&scenario)
			if _, err := NewBrowserConversationRun(scenario); err == nil {
				t.Fatal("invalid scenario was admitted")
			} else {
				var scenarioErr *BrowserConversationScenarioError
				if !errors.As(err, &scenarioErr) || !strings.Contains(scenarioErr.Path, test.path) {
					t.Fatalf("error = %v, want path %q", err, test.path)
				}
				if !errors.Is(err, ErrInvalidBrowserConversationScenario) {
					t.Fatalf("error does not preserve invalid-scenario identity: %v", err)
				}
			}
		})
	}

	var decoded BrowserConversationScenario
	if err := json.Unmarshal([]byte(`{"version":"`+BrowserConversationScenarioVersion+`","id":"id","name":"name","credentials":{"api_key":"sk-test"}}`), &decoded); err == nil {
		t.Fatal("credential-shaped unknown field was accepted")
	} else if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("credential-shaped field error = %v", err)
	}
}

func TestBrowserConversationRunPublishesJoinedImmutableEvidence(t *testing.T) {
	run, err := NewBrowserConversationRun(validBrowserConversationScenario())
	if err != nil {
		t.Fatalf("NewBrowserConversationRun: %v", err)
	}
	if err := run.ObserveCustomerTurn("inspect", "Please inspect the current page and set the label to blue."); err != nil {
		t.Fatalf("customer turn: %v", err)
	}
	if err := run.ObserveOracleSnapshot(BrowserConversationOracleSnapshot{
		StepID: "inspect", PageID: "home", Phase: BrowserConversationOracleBefore,
		State: json.RawMessage(`{"label":"unset"}`),
	}); err != nil {
		t.Fatalf("before oracle: %v", err)
	}
	input := `{"value":1}`
	output := json.RawMessage(`{"label":"blue"}`)
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID: "inspect", Operation: BrowserConversationInvoke, ToolName: "write_label",
		ToolRef: "webmcp.tool-ref.v1:ABCDEFGHIJKLMNOPQRSTUV", InvocationID: "inv-1",
		InputJSON: input, State: "completed", Terminal: true, Output: output,
	}); err != nil {
		t.Fatalf("broker call: %v", err)
	}
	if err := run.ObserveOracleSnapshot(BrowserConversationOracleSnapshot{
		StepID: "inspect", PageID: "home", Phase: BrowserConversationOracleAfter,
		State: output,
	}); err != nil {
		t.Fatalf("after oracle: %v", err)
	}
	if err := run.ObserveAssistantTurn("inspect", "The label is now blue."); err != nil {
		t.Fatalf("assistant turn: %v", err)
	}
	if err := run.RecordCancellation(BrowserConversationCancellationEvidence{
		Interrupted: true, Requested: true, InvocationID: "inv-2", FinalState: "canceled", Reason: "customer requested stop",
	}); err != nil {
		t.Fatalf("cancellation: %v", err)
	}
	if err := run.RecordLifecycle(BrowserConversationLifecycleEvidence{
		Outcome: BrowserConversationLifecycleCanceled, SessionStarted: true, SessionTerminated: true,
		Detached: true, DetachCount: 1, ExternalTabAlive: true,
	}); err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	if err := run.RecordMechanicalEvaluation(BrowserConversationMechanicalEvaluation{Passed: true}); err != nil {
		t.Fatalf("mechanical evaluation: %v", err)
	}
	if err := run.RecordValidator(BrowserConversationValidatorVerdict{
		Status: BrowserConversationValidatorPass, Passed: true,
		Checks: []BrowserConversationValidatorCheck{{Name: "grounding", Passed: true}},
	}); err != nil {
		t.Fatalf("validator: %v", err)
	}

	result, err := run.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !result.Finalized || result.ScenarioID != "customer-browser-flow" || len(result.Turns) != 2 || len(result.BrokerCalls) != 1 || len(result.Oracles) != 2 {
		t.Fatalf("joined result = %#v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("joined result validation: %v", err)
	}
	if result.BrokerCalls[0].InputJSON != input || string(result.BrokerCalls[0].Output) != string(output) {
		t.Fatalf("broker evidence = %#v", result.BrokerCalls[0])
	}
	for index, sequence := range []uint64{result.Turns[0].Sequence, result.Oracles[0].Sequence, result.BrokerCalls[0].Sequence, result.Oracles[1].Sequence, result.Turns[1].Sequence} {
		if sequence != uint64(index+1) {
			t.Fatalf("evidence sequence at %d = %d", index, sequence)
		}
	}

	result.Turns[0].ObservedText = "mutated"
	result.BrokerCalls[0].Output[2] = 'x'
	result.Oracles[0].State[2] = 'x'
	snapshot := run.Snapshot()
	if snapshot.Turns[0].ObservedText == "mutated" || string(snapshot.BrokerCalls[0].Output) != string(output) || string(snapshot.Oracles[0].State) != `{"label":"unset"}` {
		t.Fatalf("run retained caller mutations: %#v", snapshot)
	}
	if err := run.ObserveAssistantTurn("cancel", "late response"); !errors.Is(err, ErrBrowserConversationRunFinalized) {
		t.Fatalf("late observation error = %v", err)
	}
	if err := run.RecordLifecycle(BrowserConversationLifecycleEvidence{}); !errors.Is(err, ErrBrowserConversationRunFinalized) {
		t.Fatalf("late lifecycle error = %v", err)
	}
	repeated, err := run.Finalize()
	if err != nil || repeated.Turns[0].ObservedText != "Please inspect the current page and set the label to blue." {
		t.Fatalf("repeated finalize = %#v, err=%v", repeated, err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, field := range []string{"turns", "broker_calls", "oracle_snapshots", "cancellation", "lifecycle", "mechanical", "validator", "input_json"} {
		if !strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("result JSON missing %q: %s", field, encoded)
		}
	}
}

func TestBrowserConversationEvidenceRejectsContradictoryTerminalFacts(t *testing.T) {
	run, err := NewBrowserConversationRun(validBrowserConversationScenario())
	if err != nil {
		t.Fatalf("new run: %v", err)
	}
	if err := run.RecordCancellation(BrowserConversationCancellationEvidence{
		Requested: true, FinalState: webmcp.InvocationCompleted,
	}); err == nil || !strings.Contains(err.Error(), "final_state") {
		t.Fatalf("contradictory cancellation error = %v", err)
	}
	if err := run.RecordValidator(BrowserConversationValidatorVerdict{
		Version: "old-rubric", Status: BrowserConversationValidatorPass,
	}); err == nil || !strings.Contains(err.Error(), "validator.version") {
		t.Fatalf("validator version error = %v", err)
	}

	invalid := BrowserConversationResult{
		ScenarioID: "id", ScenarioName: "name",
		Turns: []BrowserConversationTurn{{Sequence: 1, StepID: "step", Direction: "unknown", ObservedText: "text"}},
	}
	if err := invalid.Validate(); err == nil || !errors.Is(err, ErrInvalidBrowserConversationResult) {
		t.Fatalf("invalid result error = %v", err)
	}
	invalid.Turns[0].Direction = BrowserConversationAssistantTurn
	invalid.BrokerCalls = []BrowserConversationBrokerCall{{Sequence: 2, Operation: BrowserConversationInvoke, State: "dispatched", Terminal: true}}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "broker_calls[0].state") {
		t.Fatalf("terminal state error = %v", err)
	}
}

func TestBrowserConversationValidatorFunctionUsesResultSeam(t *testing.T) {
	called := false
	validator := BrowserConversationValidatorFunc(func(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
		called = result.Finalized
		return BrowserConversationValidatorVerdict{Version: BrowserConversationValidatorVersion, Status: BrowserConversationValidatorPass, Passed: true}, nil
	})
	run, err := NewBrowserConversationRun(validBrowserConversationScenario())
	if err != nil {
		t.Fatalf("new run: %v", err)
	}
	result, err := run.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	verdict, err := validator.ValidateBrowserConversation(result)
	if err != nil || !called || !verdict.Passed {
		t.Fatalf("validator result = %#v, called=%t, err=%v", verdict, called, err)
	}
	var nilValidator BrowserConversationValidatorFunc
	if _, err := nilValidator.ValidateBrowserConversation(result); err == nil {
		t.Fatal("nil validator function did not fail")
	}
}
