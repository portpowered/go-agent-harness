package scenariov2

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// ToLegacy projects a provider-only v2 document onto the legacy probe
// scenario so the legacy runner can execute it. Documents that reference
// fixtures or use browser steps or expectations require this package's
// browser-aware executor and are rejected.
func ToLegacy(versioned probe.ScenarioV2, lookup probe.CorpusLookup) (probe.Scenario, error) {
	if versioned.BrowserFixture != "" || versioned.ProviderFixture != "" {
		return probe.Scenario{}, errors.New("probe.scenario.v2 includes fixture references but the legacy probe runner has no v2 fixture executor")
	}
	legacy := probe.Scenario{
		ID:           versioned.ID,
		Name:         versioned.Name,
		Description:  versioned.Description,
		Steps:        make([]probe.Step, 0, len(versioned.Steps)),
		Expectations: make([]probe.ExpectedBehavior, 0, len(versioned.Expectations)),
	}
	converters := legacyStepConverters()
	for index, step := range versioned.Steps {
		convert, ok := converters[step.Type]
		if !ok {
			return probe.Scenario{}, fmt.Errorf("probe.scenario.v2 step %q at index %d requires the browser-aware probe executor", step.Type, index)
		}
		legacy.Steps = append(legacy.Steps, convert(step))
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
	if err := legacy.Validate(lookup); err != nil {
		return probe.Scenario{}, fmt.Errorf("validate projected probe.scenario.v2 %q: %w", versioned.ID, err)
	}
	return legacy, nil
}

// legacyStepConverters holds the provider-session steps the legacy runner
// can execute; every other step type needs the browser-aware executor.
func legacyStepConverters() map[probe.ScenarioV2StepType]func(probe.ScenarioV2Step) probe.Step {
	return map[probe.ScenarioV2StepType]func(probe.ScenarioV2Step) probe.Step{
		probe.ScenarioV2StepSendText:  sendTextStep,
		probe.ScenarioV2StepSendAudio: sendAudioStep,
		probe.ScenarioV2StepSleepFake: func(step probe.ScenarioV2Step) probe.Step {
			return probe.Step{Type: probe.StepWait, Kind: probe.StepWait, Duration: probe.LogicalTime(step.DurationMS)}
		},
		probe.ScenarioV2StepClose: func(probe.ScenarioV2Step) probe.Step {
			return probe.Step{Type: probe.StepClose, Kind: probe.StepClose}
		},
	}
}

func sendTextStep(step probe.ScenarioV2Step) probe.Step {
	return probe.Step{Type: probe.StepSendText, Kind: probe.StepSendText, Text: step.Text}
}

func sendAudioStep(step probe.ScenarioV2Step) probe.Step {
	return probe.Step{
		Type:     probe.StepSendAudio,
		Kind:     probe.StepSendAudio,
		CorpusID: step.CorpusID,
		Corpus:   probe.AudioCorpusReference{ID: step.CorpusID, CorpusID: step.CorpusID},
		Text:     step.Text,
	}
}
