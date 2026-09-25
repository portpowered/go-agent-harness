package objective

import (
	"encoding/json"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// Verification is the objective verdict recomputed from captured artifacts.
type Verification struct {
	CheckedClaim string
	Verified     bool
	Error        string
	Divergence   *Divergence
}

type providerCheck func(runtimeReplay.CaptureProbeObservation, probe.ScenarioV2Expectation) Check

// providerChecks maps every provider objective to its capture check. The key
// set is also the definition of a provider objective.
func providerChecks() map[probe.ScenarioV2ExpectationType]providerCheck {
	return map[probe.ScenarioV2ExpectationType]providerCheck{
		probe.ScenarioV2ExpectationTranscriptContains:    checkTranscript,
		probe.ScenarioV2ExpectationAssistantAudioStarted: checkAudioStarted,
		probe.ScenarioV2ExpectationAssistantAudioStopped: checkAudioStopped,
	}
}

// IsProviderObjective reports whether kind is verified from the provider capture.
func IsProviderObjective(kind probe.ScenarioV2ExpectationType) bool {
	_, ok := providerChecks()[kind]
	return ok
}

// ProviderCheck evaluates one provider objective against the capture report.
func ProviderCheck(capture runtimeReplay.CaptureProbeObservation, expectation probe.ScenarioV2Expectation) Check {
	check, ok := providerChecks()[expectation.Type]
	if !ok {
		return Check{
			EvidenceArtifact: ProviderArtifactPath,
			Expected:         "provider objective",
			Actual:           Unsupported,
			ErrorCode:        unsupportedExpectationCode,
		}
	}
	return check(capture, expectation)
}

func checkTranscript(capture runtimeReplay.CaptureProbeObservation, expectation probe.ScenarioV2Expectation) Check {
	present := strings.Contains(capture.Transcript, expectation.Text)
	return Check{EvidenceArtifact: ProviderArtifactPath, Expected: TextPresent, Actual: SafeText(present), Passed: present}
}

func checkAudioStarted(capture runtimeReplay.CaptureProbeObservation, _ probe.ScenarioV2Expectation) Check {
	return Check{
		EvidenceArtifact: ProviderArtifactPath,
		Expected:         "audio-started",
		Actual:           Presence(capture.AssistantAudioStarted),
		EventPosition:    capture.AssistantAudioStartEvent,
		Passed:           capture.AssistantAudioStarted,
	}
}

func checkAudioStopped(capture runtimeReplay.CaptureProbeObservation, _ probe.ScenarioV2Expectation) Check {
	return Check{
		EvidenceArtifact: ProviderArtifactPath,
		Expected:         "audio-stopped",
		Actual:           Presence(capture.AssistantAudioStopped),
		EventPosition:    capture.AssistantAudioStopEvent,
		Passed:           capture.AssistantAudioStopped,
	}
}

// VerifyEvidenceData recomputes the objective verdict for scenario from the
// persisted browser events, page-state oracle, and provider capture. The
// first failing objective, in declaration order per evidence family, wins.
func VerifyEvidenceData(
	scenario probe.ScenarioV2,
	events []testkit.Event,
	pageState json.RawMessage,
	capture runtimeReplay.CaptureProbeObservation,
	hasBrowserArtifact bool,
) Verification {
	if failed, ok := verifyBrowserObjectives(scenario, events, pageState, hasBrowserArtifact); ok {
		return failed
	}
	for index, expectation := range scenario.Expectations {
		if !IsProviderObjective(expectation.Type) {
			continue
		}
		if check := ProviderCheck(capture, expectation); !check.Passed {
			return failedObjective(scenario, index, expectation, check)
		}
	}
	return Verification{CheckedClaim: CheckedClaim(scenario), Verified: true}
}

// verifyBrowserObjectives returns the first failed browser objective, if any.
func verifyBrowserObjectives(
	scenario probe.ScenarioV2,
	events []testkit.Event,
	pageState json.RawMessage,
	hasBrowserArtifact bool,
) (Verification, bool) {
	hasBrowserObjectives := false
	for index, expectation := range scenario.Expectations {
		if !IsBrowserObjective(expectation.Type) {
			continue
		}
		hasBrowserObjectives = true
		if !hasBrowserArtifact || len(events) == 0 {
			return failedObjective(scenario, index, expectation, Check{
				Expected:         "captured objective evidence",
				Actual:           "missing browser event artifact",
				EvidenceArtifact: ExpectationArtifact(expectation.Type),
			}), true
		}
	}
	if !hasBrowserObjectives {
		return Verification{}, false
	}
	evidence := Index(events)
	for index, expectation := range scenario.Expectations {
		if !IsBrowserObjective(expectation.Type) {
			continue
		}
		if check := BrowserCheck(evidence, pageState, expectation); !check.Passed {
			return failedObjective(scenario, index, expectation, check), true
		}
	}
	return Verification{}, false
}

// CheckedClaim names the strongest objective family the scenario declares.
func CheckedClaim(scenario probe.ScenarioV2) string {
	for _, expectation := range scenario.Expectations {
		if expectation.Type == probe.ScenarioV2ExpectationPageStateEquals {
			return string(probe.ScenarioV2ExpectationPageStateEquals)
		}
	}
	for _, expectation := range scenario.Expectations {
		if IsBrowserObjective(expectation.Type) {
			return "browser objectives"
		}
	}
	for _, expectation := range scenario.Expectations {
		if IsProviderObjective(expectation.Type) {
			return "provider objectives"
		}
	}
	return "captured probe artifacts"
}

func failedObjective(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	check Check,
) Verification {
	divergence := MakeDivergence(scenario, index, expectation, check)
	return Verification{
		CheckedClaim: CheckedClaim(scenario),
		Verified:     false,
		Error:        divergence.Error(),
		Divergence:   divergence,
	}
}
