package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// loadProbeScenarioV2 validates a browser-aware probe document with the
// offline CLI's existing committed-corpus lookup. The legacy
// loadProbeScenario path remains unchanged so its accepted aliases continue
// to load the same public values.
func loadProbeScenarioV2(data []byte, scenarioPath string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2(data, scenarioPath, replayCorpusLookup{})
}

// loadProbeScenarioV2File is the path-aware counterpart used by callers that
// want the loader to read the scenario and resolve both fixture references
// relative to its canonical containing directory.
func loadProbeScenarioV2File(path string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2File(path, replayCorpusLookup{})
}

func loadProbeScenarioFile(path string) (probe.Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("read scenario %q: %w", path, err)
	}
	if hasScenarioV2Envelope(data) {
		versioned, loadErr := loadProbeScenarioV2File(path)
		if loadErr != nil {
			return probe.Scenario{}, loadErr
		}
		return scenarioV2ToLegacy(versioned)
	}
	return loadProbeScenario(data)
}

func hasScenarioV2Envelope(data []byte) bool {
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return false
	}
	_, present := envelope["schema_version"]
	return present
}

func scenarioV2ToLegacy(versioned probe.ScenarioV2) (probe.Scenario, error) {
	if versioned.BrowserFixture != "" || versioned.ProviderFixture != "" {
		return probe.Scenario{}, fmt.Errorf("probe.scenario.v2 includes fixture references but the legacy probe runner has no v2 fixture executor")
	}
	legacy := probe.Scenario{
		ID:           versioned.ID,
		Name:         versioned.Name,
		Description:  versioned.Description,
		Steps:        make([]probe.Step, 0, len(versioned.Steps)),
		Expectations: make([]probe.ExpectedBehavior, 0, len(versioned.Expectations)),
	}
	for index, step := range versioned.Steps {
		var converted probe.Step
		switch step.Type {
		case probe.ScenarioV2StepSendText:
			converted = probe.Step{Type: probe.StepSendText, Kind: probe.StepSendText, Text: step.Text}
		case probe.ScenarioV2StepSendAudio:
			converted = probe.Step{
				Type:     probe.StepSendAudio,
				Kind:     probe.StepSendAudio,
				CorpusID: step.CorpusID,
				Corpus:   probe.AudioCorpusReference{ID: step.CorpusID, CorpusID: step.CorpusID},
				Text:     step.Text,
			}
		case probe.ScenarioV2StepSleepFake:
			converted = probe.Step{Type: probe.StepWait, Kind: probe.StepWait, Duration: probe.LogicalTime(step.DurationMS)}
		case probe.ScenarioV2StepClose:
			converted = probe.Step{Type: probe.StepClose, Kind: probe.StepClose}
		default:
			return probe.Scenario{}, fmt.Errorf("probe.scenario.v2 step %q at index %d requires the browser-aware probe executor", step.Type, index)
		}
		legacy.Steps = append(legacy.Steps, converted)
	}
	for index, expectation := range versioned.Expectations {
		if expectation.Type != probe.ScenarioV2ExpectationTranscriptContains {
			return probe.Scenario{}, fmt.Errorf("probe.scenario.v2 expectation %q at index %d requires the browser-aware probe executor", expectation.Type, index)
		}
		legacy.Expectations = append(legacy.Expectations, probe.ExpectedBehavior{
			Type: probe.ExpectTranscriptContains, Kind: probe.ExpectTranscriptContains, Text: expectation.Text,
		})
	}
	legacy.Expected = legacy.Expectations
	legacy.ExpectedBehavior = legacy.Expectations
	if err := legacy.Validate(replayCorpusLookup{}); err != nil {
		return probe.Scenario{}, fmt.Errorf("validate projected probe.scenario.v2 %q: %w", versioned.ID, err)
	}
	return legacy, nil
}
