package deviceprobe

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func scenarioDeviceProbeTranscript(scenario probe.Scenario) string {
	for _, expectation := range scenario.Expectations {
		kind := expectation.Type
		if kind == "" {
			kind = expectation.Kind
		}
		if kind != probe.ExpectTranscriptContains {
			continue
		}
		if strings.TrimSpace(expectation.Text) != "" {
			return strings.TrimSpace(expectation.Text)
		}
		if strings.TrimSpace(expectation.Value) != "" {
			return strings.TrimSpace(expectation.Value)
		}
	}
	return "device round trip"
}

func scenarioDeviceProbeInput(scenario probe.Scenario) (deviceProbeInputPlan, error) {
	var corpusID string
	count := 0
	var utterance string
	for _, step := range scenario.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if kind != probe.StepSendAudio {
			continue
		}
		count++
		candidate := step.CorpusID
		if candidate == "" {
			candidate = step.Corpus.CorpusID
		}
		if corpusID == "" {
			corpusID = candidate
		}
		if corpusID != candidate {
			return deviceProbeInputPlan{}, fmt.Errorf("device probe scenario must contain exactly one send_audio step with one committed audio corpus")
		}
		utterance = strings.TrimSpace(step.Text)
	}
	if count != 1 || strings.TrimSpace(corpusID) == "" {
		return deviceProbeInputPlan{}, fmt.Errorf("device probe scenario must contain exactly one send_audio step with a committed audio corpus")
	}
	if utterance == "" {
		return deviceProbeInputPlan{}, fmt.Errorf("device probe send_audio step for corpus %q must declare text for the manual microphone utterance", corpusID)
	}
	return deviceProbeInputPlan{CorpusID: corpusID, Utterance: utterance}, nil
}

func deviceProbeInstructionsForInput(input deviceProbeInputPlan, response string) string {
	return fmt.Sprintf("This is a T2 real-device probe. Speak the authored utterance %q (corpus_id %q) into the selected physical microphone. The probe reads that microphone; it does not inject a WAV or replay fixture. After recognizing the utterance, respond by saying exactly %q. Keep the response short.", input.Utterance, input.CorpusID, response)
}
