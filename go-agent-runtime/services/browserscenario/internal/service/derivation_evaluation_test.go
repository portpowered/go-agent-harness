package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDeriveBrowserConversationCorrectionsKeepsIndependentEvidence(t *testing.T) {
	scenario := validBrowserConversationScenario()
	result := BrowserConversationResult{
		Turns: []BrowserConversationTurn{
			{Sequence: 1, StepID: "inspect", Direction: BrowserConversationCustomerTurn, ObservedText: scenario.Steps[0].Utterance},
			{Sequence: 3, StepID: "inspect", Direction: BrowserConversationAssistantTurn, ObservedText: "The label is blue."},
			{Sequence: 4, StepID: "correct", Direction: BrowserConversationCustomerTurn, ObservedText: scenario.Steps[2].Utterance},
			{Sequence: 6, StepID: "correct", Direction: BrowserConversationAssistantTurn, ObservedText: "The label is unset again."},
		},
		BrokerCalls: []BrowserConversationBrokerCall{
			{Sequence: 2, StepID: "inspect", Operation: BrowserConversationInvoke, InvocationID: "original", ToolName: "write_label", State: "completed", Terminal: true},
			{Sequence: 5, StepID: "correct", Operation: BrowserConversationInvoke, InvocationID: "correction", ToolName: "write_label", State: "completed", Terminal: true},
		},
		Oracles: []BrowserConversationOracleSnapshot{
			{Sequence: 7, StepID: "inspect", PageID: "home", Phase: BrowserConversationOracleBefore, State: json.RawMessage(`{"label":"unset"}`)},
			{Sequence: 8, StepID: "inspect", PageID: "home", Phase: BrowserConversationOracleAfter, State: json.RawMessage(`{"label":"blue"}`)},
			{Sequence: 9, StepID: "correct", PageID: "home", Phase: BrowserConversationOracleBefore, State: json.RawMessage(`{"label":"blue"}`)},
			{Sequence: 10, StepID: "correct", PageID: "home", Phase: BrowserConversationOracleAfter, State: json.RawMessage(`{"label":"unset"}`)},
		},
	}

	corrections := DeriveBrowserConversationCorrections(scenario, result)
	if len(corrections) != 1 {
		t.Fatalf("corrections = %#v, want one correction", corrections)
	}
	correction := corrections[0]
	if !correction.Passed || !correction.OriginalInvocationCompleted || !correction.CorrectionInvocationCompleted {
		t.Fatalf("correction = %#v, want complete independent evidence", correction)
	}
	if correction.TargetUtterance != scenario.Steps[0].Utterance || correction.CorrectionUtterance != scenario.Steps[2].Utterance {
		t.Fatalf("utterances = %#v, want declared customer intents", correction)
	}
	if correction.OriginalInvocationID != "original" || correction.CorrectionInvocationID != "correction" {
		t.Fatalf("invocation identities = %#v, want stable identities", correction)
	}
	correction.OriginalBefore[2] = 'x'
	if string(result.Oracles[0].State) != `{"label":"unset"}` {
		t.Fatal("correction derivation aliased the source oracle")
	}
}

func TestDeriveBrowserConversationCorrectionsRetainsFailureFacts(t *testing.T) {
	scenario := validBrowserConversationScenario()
	result := BrowserConversationResult{
		Turns: []BrowserConversationTurn{
			{Sequence: 1, StepID: "inspect", Direction: BrowserConversationCustomerTurn, ObservedText: scenario.Steps[0].Utterance},
		},
	}
	corrections := DeriveBrowserConversationCorrections(scenario, result)
	if len(corrections) != 1 || corrections[0].Passed {
		t.Fatalf("corrections = %#v, want one failed derived record", corrections)
	}
	evaluation, err := EvaluateBrowserConversation(scenario, minimalResultForScenario(scenario), nil)
	if err != nil {
		t.Fatalf("EvaluateBrowserConversation: %v", err)
	}
	if evaluation.Passed || !containsBrowserConversationFailure(evaluation.Failures, "correction lacks a completed terminal invocation") {
		t.Fatalf("evaluation = %#v, want causal correction failure", evaluation)
	}
}

func TestDeriveBrowserConversationRecoveryPreservesReferenceGenerations(t *testing.T) {
	scenario := recoveryScenarioForTest()
	result := BrowserConversationResult{
		BrokerCalls: []BrowserConversationBrokerCall{
			{Sequence: 1, StepID: "change", Operation: BrowserConversationCustomerNavigate, PreviousGeneration: 1, Generation: 2},
			{Sequence: 2, StepID: "change", Operation: BrowserConversationInvoke, ToolRef: "old-ref", InvocationID: "stale", State: "error", Terminal: true, ErrorCode: "stale_tool_ref", Generation: 1},
			{Sequence: 3, StepID: "change", Operation: BrowserConversationListTools, ToolRefs: []string{"new-ref"}, Generation: 2},
			{Sequence: 4, StepID: "change", Operation: BrowserConversationInvoke, ToolRef: "new-ref", InvocationID: "fresh", State: "completed", Terminal: true, Generation: 2},
		},
	}
	recoveries := DeriveBrowserConversationRecovery(scenario, result)
	if len(recoveries) != 1 {
		t.Fatalf("recoveries = %#v, want one recovery", recoveries)
	}
	recovery := recoveries[0]
	if !recovery.Passed || !recovery.NavigationObserved || !recovery.StaleRejected || !recovery.ToolsRelisted || !recovery.FreshInvocationCompleted {
		t.Fatalf("recovery = %#v, want complete recovery facts", recovery)
	}
	if recovery.PreviousGeneration != 1 || recovery.CurrentGeneration != 2 || recovery.StaleGeneration != 1 || recovery.FreshGeneration != 2 {
		t.Fatalf("generations = %#v, want 1 -> 2", recovery)
	}
	refs, ok := recovery.RelistedToolRefs.([]string)
	if !ok || len(refs) != 1 || refs[0] != "new-ref" {
		t.Fatalf("relisted refs = %#v, want cloned fresh catalog", recovery.RelistedToolRefs)
	}
	refs[0] = browserConversationTestMutatedText
	sourceRefs, ok := result.BrokerCalls[2].ToolRefs.([]string)
	if !ok || len(sourceRefs) == 0 || sourceRefs[0] != "new-ref" {
		t.Fatal("recovery derivation aliased the source tool catalog")
	}
}

func TestDeriveBrowserConversationRecoveryRejectsMissingCausalStages(t *testing.T) {
	scenario := recoveryScenarioForTest()
	result := BrowserConversationResult{BrokerCalls: []BrowserConversationBrokerCall{
		{Sequence: 1, StepID: "change", Operation: BrowserConversationCustomerNavigate, PreviousGeneration: 1, Generation: 2},
		{Sequence: 2, StepID: "change", Operation: BrowserConversationInvoke, ToolRef: "new-ref", State: "completed", Terminal: true, Generation: 2},
	}}
	recoveries := DeriveBrowserConversationRecovery(scenario, result)
	if len(recoveries) != 1 || recoveries[0].Passed {
		t.Fatalf("recoveries = %#v, want failed causal record", recoveries)
	}
	evaluation, err := EvaluateBrowserConversation(scenario, recoveryResultForEvaluation(result), nil)
	if err != nil {
		t.Fatalf("EvaluateBrowserConversation: %v", err)
	}
	if evaluation.Passed || !containsBrowserConversationFailure(evaluation.Failures, "stale tool reference was not rejected") {
		t.Fatalf("evaluation = %#v, want stale-reference failure", evaluation)
	}
}

func TestEvaluateBrowserConversationComposesPositiveIndependentEvidence(t *testing.T) {
	scenario := simpleEvaluationScenario()
	evaluation, err := EvaluateBrowserConversation(scenario, simpleEvaluationResult(), nil)
	if err != nil {
		t.Fatalf("EvaluateBrowserConversation: %v", err)
	}
	if !evaluation.Passed || len(evaluation.Failures) != 0 {
		t.Fatalf("evaluation = %#v, want pass without failures", evaluation)
	}
}

func TestEvaluateBrowserConversationPreservesExpectedCancellation(t *testing.T) {
	scenario := cancellationScenarioForTest()
	result := BrowserConversationResult{
		ScenarioID: "cancel", ScenarioName: "cancel",
		Turns: []BrowserConversationTurn{
			{Sequence: 1, StepID: "set", Direction: BrowserConversationCustomerTurn, ObservedText: "set it"},
			{Sequence: 4, StepID: "set", Direction: BrowserConversationAssistantTurn, ObservedText: "done"},
			{Sequence: 5, StepID: "interrupt", Direction: BrowserConversationCustomerTurn, ObservedText: "stop"},
		},
		BrokerCalls: []BrowserConversationBrokerCall{{Sequence: 2, StepID: "set", Operation: BrowserConversationInvoke, ToolRef: "ref", InvocationID: "invoke", State: "completed", Terminal: true, InputJSON: `{}`, Output: json.RawMessage(`{"value":true}`)}},
		Oracles: []BrowserConversationOracleSnapshot{
			{Sequence: 3, StepID: "set", PageID: "home", Phase: BrowserConversationOracleBefore, State: json.RawMessage(`{"value":false}`)},
			{Sequence: 6, StepID: "set", PageID: "home", Phase: BrowserConversationOracleAfter, State: json.RawMessage(`{"value":true}`)},
			{Sequence: 7, PageID: "home", Phase: BrowserConversationOraclePostSession, State: json.RawMessage(`{"ok":true}`)},
		},
		Cancellation: BrowserConversationCancellationEvidence{Interrupted: true, Requested: true, InvocationID: "invoke-1", FinalState: "canceled", OverlappingAudioSent: true},
		Lifecycle:    BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleCanceled, SessionStarted: true, SessionTerminated: true, ExternalTabAlive: true, ExternalTabResponsive: true, ExternalTabAllowsMutation: true},
	}
	evaluation, err := EvaluateBrowserConversation(scenario, result, context.Canceled)
	if err != nil {
		t.Fatalf("EvaluateBrowserConversation: %v", err)
	}
	if !evaluation.Passed {
		t.Fatalf("evaluation = %#v, want expected cancellation pass", evaluation)
	}
}

func TestEvaluateBrowserConversationRetainsUnexpectedRootAndLifecycleFailures(t *testing.T) {
	scenario := simpleEvaluationScenario()
	result := BrowserConversationResult{ScenarioID: "simple", ScenarioName: "simple"}
	evaluation, err := EvaluateBrowserConversation(scenario, result, errors.New("provider stopped unexpectedly"))
	if err != nil {
		t.Fatalf("EvaluateBrowserConversation: %v", err)
	}
	if evaluation.Passed || !containsBrowserConversationFailure(evaluation.Failures, "provider stopped unexpectedly") || !containsBrowserConversationFailure(evaluation.Failures, "missing customer transcript") || !containsBrowserConversationFailure(evaluation.Failures, "session was not started") {
		t.Fatalf("evaluation = %#v, want root and lifecycle failures", evaluation)
	}
}

func TestBrowserConversationRunAliasesAndOpaqueEvidenceAreDefensive(t *testing.T) {
	type toolRef string
	type invocationState string
	scenario := simpleEvaluationScenario()
	validated, err := NewBrowserScenario(scenario)
	if err != nil {
		t.Fatalf("NewBrowserScenario: %v", err)
	}
	value, err := NewBrowserConversationScenarioValue(validated)
	if err != nil {
		t.Fatalf("NewBrowserConversationScenarioValue: %v", err)
	}
	if value.BrowserConversationScenario().ID != scenario.ID {
		t.Fatalf("scenario value = %#v, want %q", value.BrowserConversationScenario(), scenario.ID)
	}
	run, err := NewBrowserScenarioRun(validated)
	if err != nil {
		t.Fatalf("NewBrowserScenarioRun: %v", err)
	}
	refs := []toolRef{"ref-1", "ref-2"}
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID: "step", Operation: BrowserConversationListTools, ToolRef: toolRef("ref-1"), ToolRefs: refs,
		InvocationID: []toolRef{"inv-1"}, State: invocationState("completed"),
	}); err != nil {
		t.Fatalf("ObserveBrokerCall: %v", err)
	}
	refs[0] = "caller-mutated"
	snapshot := run.Snapshot()
	snapshotRefs, ok := snapshot.BrokerCalls[0].ToolRefs.([]toolRef)
	if !ok || len(snapshotRefs) == 0 || snapshotRefs[0] != "ref-1" {
		t.Fatalf("snapshot tool refs = %#v, caller mutation leaked", snapshot.BrokerCalls[0].ToolRefs)
	}
	snapshotRefs[0] = browserConversationTestMutatedText
	savedRefs, ok := run.Snapshot().BrokerCalls[0].ToolRefs.([]toolRef)
	if !ok || len(savedRefs) == 0 || savedRefs[0] != "ref-1" {
		t.Fatal("snapshot tool refs share run state")
	}
	if !opaqueEqual(toolRef("same"), "same") || opaqueEqual(toolRef("one"), "two") {
		t.Fatal("opaque equality does not compare host-named values")
	}
	if browserConversationOpaqueLen(refs) != 2 || browserConversationOpaqueString(nil) != "" {
		t.Fatal("opaque helpers returned incorrect shape")
	}
}

func TestRecordMethodsRejectDuplicatesAndInvalidEvidence(t *testing.T) {
	run, err := NewBrowserConversationRun(simpleEvaluationScenario())
	if err != nil {
		t.Fatalf("new run: %v", err)
	}
	if err := run.RecordRecovery([]BrowserConversationRecoveryEvidence{{StepID: "step", FromPageID: "home", ToPageID: "home"}}); err != nil {
		t.Fatalf("RecordRecovery: %v", err)
	}
	if err := run.RecordRecovery([]BrowserConversationRecoveryEvidence{{StepID: "step", FromPageID: "home", ToPageID: "home"}}); !errors.Is(err, ErrBrowserConversationDuplicateObservation) {
		t.Fatalf("duplicate recovery error = %v", err)
	}
	if err := run.RecordCorrections([]BrowserConversationCorrectionEvidence{{StepID: "step", TargetStepID: "step", TargetUtterance: "x", CorrectionUtterance: "y"}}); err != nil {
		t.Fatalf("RecordCorrections: %v", err)
	}
	if err := run.RecordCorrections([]BrowserConversationCorrectionEvidence{{StepID: "step", TargetStepID: "step", TargetUtterance: "x", CorrectionUtterance: "y"}}); !errors.Is(err, ErrBrowserConversationDuplicateObservation) {
		t.Fatalf("duplicate corrections error = %v", err)
	}
	if err := run.ObserveTurn(BrowserConversationTurn{StepID: "step", Direction: "unknown", ObservedText: "x"}); err == nil {
		t.Fatal("invalid assembled turn was accepted")
	}
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{StepID: "step", Operation: "unsupported"}); err == nil {
		t.Fatal("unsupported broker operation was accepted")
	}
	if err := run.ObserveOracleSnapshot(BrowserConversationOracleSnapshot{PageID: "home", Phase: BrowserConversationOracleBefore, StepID: "step", State: json.RawMessage(`[]`)}); err == nil {
		t.Fatal("non-object oracle state was accepted")
	}
}

func simpleEvaluationScenario() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion, ID: "simple", Name: "simple", RunTimeout: time.Second,
		Fixture:     BrowserConversationFixture{ID: "fixture", InitialPage: "home", Pages: []BrowserConversationPage{{ID: "home", URL: "https://fixture.test/home"}}},
		Steps:       []BrowserConversationStep{{ID: "step", Utterance: "set it", PageID: "home", Deadline: time.Second, ExpectedState: &BrowserStateTransition{PageID: "home", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`)}}},
		PostSession: BrowserConversationTabStateRequired{PageID: "home", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func simpleEvaluationResult() BrowserConversationResult {
	return BrowserConversationResult{
		ScenarioID: "simple", ScenarioName: "simple",
		Turns: []BrowserConversationTurn{
			{Sequence: 1, StepID: "step", Direction: BrowserConversationCustomerTurn, ObservedText: "set it"},
			{Sequence: 4, StepID: "step", Direction: BrowserConversationAssistantTurn, ObservedText: "done"},
		},
		BrokerCalls: []BrowserConversationBrokerCall{{Sequence: 2, StepID: "step", Operation: BrowserConversationInvoke, ToolRef: "ref", InvocationID: "invoke", State: "completed", Terminal: true, InputJSON: `{}`, Output: json.RawMessage(`{"value":true}`)}},
		Oracles: []BrowserConversationOracleSnapshot{
			{Sequence: 3, StepID: "step", PageID: "home", Phase: BrowserConversationOracleBefore, State: json.RawMessage(`{"value":false}`)},
			{Sequence: 5, StepID: "step", PageID: "home", Phase: BrowserConversationOracleAfter, State: json.RawMessage(`{"value":true}`)},
			{Sequence: 6, PageID: "home", Phase: BrowserConversationOraclePostSession, State: json.RawMessage(`{"value":true}`)},
		},
		Lifecycle: BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleCompleted, SessionStarted: true, SessionTerminated: true, ExternalTabAlive: true, ExternalTabResponsive: true, ExternalTabAllowsMutation: true},
	}
}

func recoveryScenarioForTest() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion, ID: "recovery", Name: "recovery", RunTimeout: time.Second,
		Fixture:     BrowserConversationFixture{ID: "fixture", InitialPage: "home", Pages: []BrowserConversationPage{{ID: "home", URL: "https://fixture.test/home"}, {ID: "next", URL: "https://fixture.test/next"}}},
		Steps:       []BrowserConversationStep{{ID: "change", Utterance: "change page", PageID: "next", Deadline: time.Second, Navigation: &BrowserCustomerNavigation{FromPageID: "home", ToPageID: "next", URL: "https://fixture.test/next"}, ExpectedState: &BrowserStateTransition{PageID: "next", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`)}}},
		PostSession: BrowserConversationTabStateRequired{PageID: "next", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func recoveryResultForEvaluation(result BrowserConversationResult) BrowserConversationResult {
	result.ScenarioID, result.ScenarioName = "recovery", "recovery"
	result.Turns = []BrowserConversationTurn{
		{Sequence: 5, StepID: "change", Direction: BrowserConversationCustomerTurn, ObservedText: "change page"},
		{Sequence: 6, StepID: "change", Direction: BrowserConversationAssistantTurn, ObservedText: "done"},
	}
	result.Oracles = []BrowserConversationOracleSnapshot{
		{Sequence: 7, StepID: "change", PageID: "next", Phase: BrowserConversationOracleBefore, State: json.RawMessage(`{"value":false}`)},
		{Sequence: 8, StepID: "change", PageID: "next", Phase: BrowserConversationOracleAfter, State: json.RawMessage(`{"value":true}`)},
		{Sequence: 9, PageID: "next", Phase: BrowserConversationOraclePostSession, State: json.RawMessage(`{"ok":true}`)},
	}
	result.Lifecycle = BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleCompleted, SessionStarted: true, SessionTerminated: true, ExternalTabAlive: true, ExternalTabResponsive: true, ExternalTabAllowsMutation: true}
	return result
}

func cancellationScenarioForTest() BrowserConversationScenario {
	return BrowserConversationScenario{
		Version: BrowserConversationScenarioVersion, ID: "cancel", Name: "cancel", RunTimeout: time.Second,
		Fixture: BrowserConversationFixture{ID: "fixture", InitialPage: "home", Pages: []BrowserConversationPage{{ID: "home", URL: "https://fixture.test/home"}}},
		Steps: []BrowserConversationStep{
			{ID: "set", Utterance: "set it", PageID: "home", Deadline: time.Second, ExpectedState: &BrowserStateTransition{PageID: "home", Before: json.RawMessage(`{"value":false}`), After: json.RawMessage(`{"value":true}`)}},
			{ID: "interrupt", Utterance: "stop", PageID: "home", Deadline: time.Second, Interrupt: &BrowserConversationInterrupt{Trigger: BrowserInterruptOnInFlightInvocation}},
		},
		PostSession: BrowserConversationTabStateRequired{PageID: "home", MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func minimalResultForScenario(scenario BrowserConversationScenario) BrowserConversationResult {
	return BrowserConversationResult{ScenarioID: scenario.ID, ScenarioName: scenario.Name, Lifecycle: BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleCompleted, SessionStarted: true, SessionTerminated: true, ExternalTabAlive: true, ExternalTabResponsive: true, ExternalTabAllowsMutation: true}, Oracles: []BrowserConversationOracleSnapshot{{Sequence: 1, PageID: scenario.PostSession.PageID, Phase: BrowserConversationOraclePostSession, State: json.RawMessage(`{"ok":true}`)}}}
}

func containsBrowserConversationFailure(failures []string, want string) bool {
	for _, failure := range failures {
		if strings.Contains(failure, want) {
			return true
		}
	}
	return false
}
