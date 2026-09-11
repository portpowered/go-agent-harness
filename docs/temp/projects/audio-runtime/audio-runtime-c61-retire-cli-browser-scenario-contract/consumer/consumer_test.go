package consumer

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	browserscenario "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
	browserscenariowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/wire"
)

func TestPublicBrowserScenarioContractHasNoCLIConstructionDependency(t *testing.T) {
	scenario := consumerScenario()
	service := browserscenariowire.NewService()
	validated, err := service.AdmitScenario(scenario)
	if err != nil {
		t.Fatalf("NewBrowserConversationScenario: %v", err)
	}
	if validated.ID != scenario.ID || len(validated.Steps) != 3 {
		t.Fatalf("validated scenario = %#v", validated)
	}
	audio, err := validated.ScheduleAudioInputs(map[string][]byte{
		"apply": {1, 2}, "navigate": {3, 4}, "correct": {5, 6},
	})
	if err != nil || len(audio) != 3 || audio[1].AfterCompletedTurns != 1 {
		t.Fatalf("scheduled audio = %#v, err=%v", audio, err)
	}
	value, err := validated.ScenarioValue()
	if err != nil || value.BrowserConversationScenario().ID != scenario.ID {
		t.Fatalf("scenario value = %#v, err=%v", value.BrowserConversationScenario(), err)
	}
}

func TestPublicBrowserScenarioContractDerivesImmutableEvidenceAndEvaluation(t *testing.T) {
	scenario := consumerScenario()
	result := consumerResult()
	service := browserscenariowire.NewService()
	corrections := service.DeriveCorrections(scenario, result)
	recoveries := service.DeriveRecovery(scenario, result)
	if len(corrections) != 1 || !corrections[0].Passed {
		t.Fatalf("corrections = %#v, want one passing correction", corrections)
	}
	if len(recoveries) != 1 || !recoveries[0].Passed {
		t.Fatalf("recoveries = %#v, want one passing recovery", recoveries)
	}
	evaluation, err := service.Evaluate(scenario, result, nil)
	if err != nil || !evaluation.Passed {
		t.Fatalf("evaluation = %#v, err=%v", evaluation, err)
	}

	run, err := scenario.NewRun()
	if err != nil {
		t.Fatalf("NewBrowserConversationRun: %v", err)
	}
	if err := run.ObserveCustomerTurn("apply", "apply"); err != nil {
		t.Fatalf("ObserveCustomerTurn: %v", err)
	}
	first := run.Snapshot()
	first.Turns[0].ObservedText = "caller mutation"
	if run.Snapshot().Turns[0].ObservedText == "caller mutation" {
		t.Fatal("runtime snapshot shares mutable state")
	}
	finalized, err := run.Finalize()
	if err != nil || !finalized.Finalized {
		t.Fatalf("Finalize = %#v, err=%v", finalized, err)
	}
	if err := run.ObserveAssistantTurn("apply", "late"); !errors.Is(err, browserscenario.ErrBrowserConversationRunFinalized) {
		t.Fatalf("late observation error = %v", err)
	}
}

func TestPublicBrowserScenarioContractSanitizesReportAndRunsBoundedValidator(t *testing.T) {
	result := consumerResult()
	service := browserscenariowire.NewService()
	report, err := service.NewReport(result, browserscenario.BrowserConversationReportMetadata{
		Command: "validator --api-key=test-value", Provider: "fixture-provider",
	})
	if err != nil {
		t.Fatalf("NewBrowserConversationReport: %v", err)
	}
	encoded, err := report.Marshal()
	if err != nil || strings.Contains(string(encoded), "test-value") || !strings.Contains(string(encoded), "claim_grounding") {
		t.Fatalf("report bytes = %s, err=%v", encoded, err)
	}

	validator, err := service.NewCommandValidator(browserscenario.BrowserConversationValidatorCommand{
		Command: []string{os.Args[0], "-test.run=^TestConsumerValidatorHelper$"},
		Env:     []string{"C61_CONSUMER_VALIDATOR=1"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewBrowserConversationCommandValidator: %v", err)
	}
	verdict, err := validator.ValidateBrowserConversation(result)
	if err != nil || verdict.Status != browserscenario.BrowserConversationValidatorPass || len(verdict.Checks) != 8 {
		t.Fatalf("verdict = %#v, err=%v", verdict, err)
	}
}

func TestConsumerValidatorHelper(t *testing.T) {
	if os.Getenv("C61_CONSUMER_VALIDATOR") != "1" {
		return
	}
	verdict := browserscenario.BrowserConversationValidatorVerdict{
		Version: browserscenario.BrowserConversationValidatorVersion,
		Status:  browserscenario.BrowserConversationValidatorPass,
		Passed:  true,
	}
	for _, name := range (browserscenario.BrowserConversationValidatorRubric{}).Values() {
		verdict.Checks = append(verdict.Checks, browserscenario.BrowserConversationValidatorCheck{Name: name, Passed: true})
	}
	_ = json.NewEncoder(os.Stdout).Encode(verdict)
	os.Exit(0)
}

func consumerScenario() browserscenario.BrowserConversationScenario {
	return browserscenario.BrowserConversationScenario{
		Version: browserscenario.BrowserConversationScenarioVersion,
		ID:      "consumer-browser-flow",
		Name:    "consumer browser flow",
		Fixture: browserscenario.BrowserConversationFixture{
			ID: "fixture", InitialPage: "home",
			Pages: []browserscenario.BrowserConversationPage{
				{ID: "home", URL: "https://fixture.test/home"},
				{ID: "next", URL: "https://fixture.test/next"},
			},
		},
		RunTimeout: 5 * time.Second,
		Steps: []browserscenario.BrowserConversationStep{
			{ID: "apply", Utterance: "apply", PageID: "home", Deadline: time.Second, ExpectedState: &browserscenario.BrowserStateTransition{
				PageID: "home", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`),
			}},
			{ID: "navigate", Utterance: "navigate", PageID: "next", Deadline: time.Second, Navigation: &browserscenario.BrowserCustomerNavigation{
				FromPageID: "home", ToPageID: "next", URL: "https://fixture.test/next",
			}, ExpectedState: &browserscenario.BrowserStateTransition{
				PageID: "next", Before: json.RawMessage(`{"value":true}`), After: json.RawMessage(`{"value":false}`),
			}},
			{ID: "correct", Utterance: "correct", PageID: "next", Deadline: time.Second, Correction: &browserscenario.BrowserConversationCorrection{
				TargetStepID: "apply", ExpectedState: browserscenario.BrowserStateTransition{
					PageID: "home", Before: json.RawMessage(`{"value":true}`), After: json.RawMessage(`{"value":false}`),
				},
			}},
		},
		PostSession: browserscenario.BrowserConversationTabStateRequired{
			PageID: "next", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true,
		},
	}
}

func consumerResult() browserscenario.BrowserConversationResult {
	return browserscenario.BrowserConversationResult{
		ScenarioID: "consumer-browser-flow", ScenarioName: "consumer browser flow",
		Turns: []browserscenario.BrowserConversationTurn{
			{Sequence: 1, StepID: "apply", Direction: browserscenario.BrowserConversationCustomerTurn, ObservedText: "apply"},
			{Sequence: 3, StepID: "apply", Direction: browserscenario.BrowserConversationAssistantTurn, ObservedText: "applied"},
			{Sequence: 11, StepID: "navigate", Direction: browserscenario.BrowserConversationCustomerTurn, ObservedText: "navigate"},
			{Sequence: 12, StepID: "navigate", Direction: browserscenario.BrowserConversationAssistantTurn, ObservedText: "navigated"},
			{Sequence: 4, StepID: "correct", Direction: browserscenario.BrowserConversationCustomerTurn, ObservedText: "correct"},
			{Sequence: 6, StepID: "correct", Direction: browserscenario.BrowserConversationAssistantTurn, ObservedText: "corrected"},
		},
		BrokerCalls: []browserscenario.BrowserConversationBrokerCall{
			{Sequence: 2, StepID: "apply", Operation: browserscenario.BrowserConversationInvoke, ToolRef: "apply-ref", InvocationID: "apply-call", State: "completed", Terminal: true, InputJSON: `{}`, Output: json.RawMessage(`{"value":true}`)},
			{Sequence: 5, StepID: "correct", Operation: browserscenario.BrowserConversationInvoke, ToolRef: "correct-ref", InvocationID: "correct-call", State: "completed", Terminal: true, InputJSON: `{}`, Output: json.RawMessage(`{"value":false}`)},
			{Sequence: 7, StepID: "navigate", Operation: browserscenario.BrowserConversationCustomerNavigate, PreviousGeneration: 1, Generation: 2},
			{Sequence: 8, StepID: "navigate", Operation: browserscenario.BrowserConversationInvoke, ToolRef: "old-ref", InvocationID: "stale-call", State: "error", Terminal: true, ErrorCode: "stale_tool_ref", Generation: 1},
			{Sequence: 9, StepID: "navigate", Operation: browserscenario.BrowserConversationListTools, ToolRefs: []string{"new-ref"}, Generation: 2},
			{Sequence: 10, StepID: "navigate", Operation: browserscenario.BrowserConversationInvoke, ToolRef: "new-ref", InvocationID: "fresh-call", State: "completed", Terminal: true, InputJSON: `{}`, Generation: 2, Output: json.RawMessage(`{"value":false}`)},
		},
		Oracles: []browserscenario.BrowserConversationOracleSnapshot{
			{Sequence: 13, StepID: "apply", PageID: "home", Phase: browserscenario.BrowserConversationOracleBefore, State: json.RawMessage(`{"value":false}`)},
			{Sequence: 14, StepID: "apply", PageID: "home", Phase: browserscenario.BrowserConversationOracleAfter, State: json.RawMessage(`{"value":true}`)},
			{Sequence: 15, StepID: "navigate", PageID: "next", Phase: browserscenario.BrowserConversationOracleBefore, State: json.RawMessage(`{"value":true}`)},
			{Sequence: 16, StepID: "navigate", PageID: "next", Phase: browserscenario.BrowserConversationOracleAfter, State: json.RawMessage(`{"value":false}`)},
			{Sequence: 17, StepID: "correct", PageID: "home", Phase: browserscenario.BrowserConversationOracleBefore, State: json.RawMessage(`{"value":true}`)},
			{Sequence: 18, StepID: "correct", PageID: "home", Phase: browserscenario.BrowserConversationOracleAfter, State: json.RawMessage(`{"value":false}`)},
			{Sequence: 19, PageID: "next", Phase: browserscenario.BrowserConversationOraclePostSession, State: json.RawMessage(`{"value":false}`)},
		},
		Lifecycle: browserscenario.BrowserConversationLifecycleEvidence{
			Outcome: browserscenario.BrowserConversationLifecycleCompleted, SessionStarted: true, SessionTerminated: true,
			ExternalTabAlive: true, ExternalTabResponsive: true, ExternalTabAllowsMutation: true,
		},
	}
}
