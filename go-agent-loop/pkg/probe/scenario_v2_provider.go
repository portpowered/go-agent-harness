package probe

import (
	"encoding/json"
	"fmt"
)

// ScenarioV2 is the only on-disk probe scenario format. A provider-only
// document (no fixtures, provider session steps, provider-measurable
// expectations) runs on the deterministic provider runner: ProviderScenario
// returns the runner's execution plan, the same Scenario value the built-in
// Go scenarios declare. Every other document needs the browser-aware
// executor.

// ProviderStep returns the provider runner step for a provider session step
// (send_text, send_audio, sleep_fake, close); ok is false for every browser
// or WebMCP step.
func (step ScenarioV2Step) ProviderStep() (Step, bool) {
	compile, ok := scenarioV2ProviderSteps()[step.Type]
	if !ok {
		return Step{}, false
	}
	return compile(step), true
}

// scenarioV2ProviderSteps compiles each provider session step type.
func scenarioV2ProviderSteps() map[ScenarioV2StepType]func(ScenarioV2Step) Step {
	return map[ScenarioV2StepType]func(ScenarioV2Step) Step{
		ScenarioV2StepSendText: func(step ScenarioV2Step) Step {
			return Step{Type: StepSendText, Kind: StepSendText, Text: step.Text}
		},
		ScenarioV2StepSendAudio: func(step ScenarioV2Step) Step {
			return Step{
				Type: StepSendAudio, Kind: StepSendAudio, CorpusID: step.CorpusID, Text: step.Text,
				Corpus: AudioCorpusReference{ID: step.CorpusID, CorpusID: step.CorpusID},
			}
		},
		ScenarioV2StepSleepFake: func(step ScenarioV2Step) Step {
			return Step{Type: StepWait, Kind: StepWait, Duration: LogicalTime(step.DurationMS)}
		},
		ScenarioV2StepClose: func(ScenarioV2Step) Step { return Step{Type: StepClose, Kind: StepClose} },
	}
}

// ProviderExpectation returns the provider runner's measurable expectation;
// ok is false for an expectation that needs the browser-aware executor.
func (expectation ScenarioV2Expectation) ProviderExpectation() (ExpectedBehavior, bool) {
	provider, ok := scenarioV2ProviderExpectations()[expectation.Type]
	if !ok {
		return ExpectedBehavior{}, false
	}
	compiled := ExpectedBehavior{Type: provider.kind, Kind: provider.kind}
	if provider.payload != nil {
		provider.payload(&compiled, expectation)
	}
	return compiled, true
}

// scenarioV2ProviderExpectation is the runner kind of one provider-measurable
// v2 expectation and how its payload carries over.
type scenarioV2ProviderExpectation struct {
	kind    ExpectationKind
	payload func(*ExpectedBehavior, ScenarioV2Expectation)
	// shared expectations are also measured by the browser-aware executor.
	shared bool
}

// scenarioV2ProviderExpectations maps every provider-measurable v2
// expectation to the runner's expectation.
func scenarioV2ProviderExpectations() map[ScenarioV2ExpectationType]scenarioV2ProviderExpectation {
	stringValue := func(compiled *ExpectedBehavior, expectation ScenarioV2Expectation) {
		var value string
		if json.Unmarshal(expectation.Value, &value) == nil {
			compiled.Value = value
		}
	}
	toolCallID := func(compiled *ExpectedBehavior, expectation ScenarioV2Expectation) {
		compiled.ToolCallID = expectation.ToolCallID
	}
	return map[ScenarioV2ExpectationType]scenarioV2ProviderExpectation{
		ScenarioV2ExpectationTranscriptContains: {kind: ExpectTranscriptContains, shared: true,
			payload: func(compiled *ExpectedBehavior, expectation ScenarioV2Expectation) { compiled.Text = expectation.Text }},
		ScenarioV2ExpectationResponseCanceled: {kind: ExpectResponseCancel, shared: true},
		ScenarioV2ExpectationFrameCount: {kind: ExpectFrameCount,
			payload: func(compiled *ExpectedBehavior, expectation ScenarioV2Expectation) {
				compiled.Count = int(expectation.Equals)
			}},
		ScenarioV2ExpectationTerminalReason:     {kind: ExpectTerminalReason, payload: stringValue},
		ScenarioV2ExpectationTerminalProvenance: {kind: ExpectTerminalProvenance, payload: stringValue},
		ScenarioV2ExpectationOutputState:        {kind: ExpectOutputState, payload: stringValue},
		ScenarioV2ExpectationBufferDisposition:  {kind: ExpectBufferDisposition, payload: stringValue},
		ScenarioV2ExpectationAudioEnergy:        {kind: ExpectAudioEnergy},
		ScenarioV2ExpectationToolCalled: {kind: ExpectToolCalled,
			payload: func(compiled *ExpectedBehavior, expectation ScenarioV2Expectation) {
				compiled.ToolName = expectation.Name
			}},
		ScenarioV2ExpectationToolResultDelivered:  {kind: ExpectToolResultDelivered, payload: toolCallID},
		ScenarioV2ExpectationToolResultDiscarded:  {kind: ExpectToolResultDiscarded, payload: toolCallID},
		ScenarioV2ExpectationNoOrphanedToolResult: {kind: ExpectNoOrphanedToolResult},
	}
}

// providerRunnerOnly reports whether an expectation can be measured only by
// the provider runner, so it is invalid in a document that needs the
// browser-aware executor.
func providerRunnerOnly(expectationType ScenarioV2ExpectationType) bool {
	provider, ok := scenarioV2ProviderExpectations()[expectationType]
	return ok && !provider.shared
}

// ProviderOnly reports whether the document runs on the provider runner: it
// references no fixtures, every step is a provider session step, and every
// expectation is provider-measurable.
func (s ScenarioV2) ProviderOnly() bool {
	if s.BrowserFixture != "" || s.ProviderFixture != "" {
		return false
	}
	for _, step := range s.Steps {
		if _, ok := step.ProviderStep(); !ok {
			return false
		}
	}
	for _, expectation := range s.Expectations {
		if _, ok := expectation.ProviderExpectation(); !ok {
			return false
		}
	}
	return true
}

// ProviderScenario returns the provider runner's execution plan for a
// provider-only document, validated against the optional corpus lookup.
func (s ScenarioV2) ProviderScenario(lookups ...CorpusLookup) (Scenario, error) {
	if !s.ProviderOnly() {
		return Scenario{}, newScenarioV2Error("scenario", "document needs the browser-aware executor: it references fixtures or uses browser steps or expectations")
	}
	if err := s.Validate(lookups...); err != nil {
		return Scenario{}, err
	}
	plan := Scenario{
		ID: s.ID, Name: s.Name, Description: s.Description,
		Steps:        make([]Step, 0, len(s.Steps)),
		Expectations: make([]ExpectedBehavior, 0, len(s.Expectations)),
	}
	for _, step := range s.Steps {
		compiled, _ := step.ProviderStep()
		plan.Steps = append(plan.Steps, compiled)
	}
	for _, expectation := range s.Expectations {
		compiled, _ := expectation.ProviderExpectation()
		plan.Expectations = append(plan.Expectations, compiled)
	}
	plan.Expected = plan.Expectations
	plan.ExpectedBehavior = plan.Expectations
	if err := plan.Validate(lookups...); err != nil {
		return Scenario{}, fmt.Errorf("validate probe.scenario.v2 %q provider plan: %w", s.ID, err)
	}
	return plan, nil
}

// validateScenarioV2ProviderExpectations rejects provider-runner-only
// expectations in a document that needs the browser-aware executor.
func validateScenarioV2ProviderExpectations(s ScenarioV2) error {
	if s.ProviderOnly() {
		return nil
	}
	for index, expectation := range s.Expectations {
		if providerRunnerOnly(expectation.Type) {
			return newScenarioV2Error(fmt.Sprintf("scenario.expectations[%d].type", index),
				"%s is measured by the provider runner and needs a provider-only document (no fixtures, provider session steps only)", expectation.Type)
		}
	}
	return nil
}
