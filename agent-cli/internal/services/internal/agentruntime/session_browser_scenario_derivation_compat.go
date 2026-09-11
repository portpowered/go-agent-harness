package agentruntime

// Pure derivation/evaluation is delegated to the runtime service. The
// conversions here exist only because the read-only runner still owns its
// historical WebMCP-shaped result structs.
// Deprecated: use go-agent-runtime/services/browserscenario through its
// injected service contract instead.

import (
	"encoding/json"
	"errors"

	runtimeBrowser "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
	browserScenarioWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/wire"
)

func DeriveBrowserConversationCorrections(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationCorrectionEvidence {
	return deriveBrowserConversationCorrections(scenario, result)
}

func deriveBrowserConversationCorrections(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationCorrectionEvidence {
	runtimeResult, err := legacyResultToRuntime(result)
	if err != nil {
		return nil
	}
	derived := browserScenarioWire.NewService().DeriveCorrections(legacyScenarioToRuntime(scenario), runtimeResult)
	encoded, err := json.Marshal(derived)
	if err != nil {
		return nil
	}
	var converted []BrowserConversationCorrectionEvidence
	_ = json.Unmarshal(encoded, &converted)
	return converted
}

func DeriveBrowserConversationRecovery(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationRecoveryEvidence {
	return deriveBrowserConversationRecovery(scenario, result)
}

func deriveBrowserConversationRecovery(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationRecoveryEvidence {
	runtimeResult, err := legacyResultToRuntime(result)
	if err != nil {
		return nil
	}
	derived := browserScenarioWire.NewService().DeriveRecovery(legacyScenarioToRuntime(scenario), runtimeResult)
	encoded, err := json.Marshal(derived)
	if err != nil {
		return nil
	}
	var converted []BrowserConversationRecoveryEvidence
	_ = json.Unmarshal(encoded, &converted)
	return converted
}

func evaluateBrowserConversation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) BrowserConversationMechanicalEvaluation {
	runtimeResult, err := legacyResultToRuntime(result)
	if err != nil {
		return BrowserConversationMechanicalEvaluation{Failures: []string{err.Error()}}
	}
	if errors.Is(rootErr, ErrBrowserConversationSession) {
		rootErr = runtimeBrowser.ErrBrowserConversationSession
	}
	if errors.Is(rootErr, ErrBrowserConversationTimeout) {
		rootErr = runtimeBrowser.ErrBrowserConversationTimeout
	}
	evaluation, err := browserScenarioWire.NewService().Evaluate(legacyScenarioToRuntime(scenario), runtimeResult, rootErr)
	if err != nil {
		return BrowserConversationMechanicalEvaluation{Failures: []string{err.Error()}}
	}
	encoded, err := json.Marshal(evaluation)
	if err != nil {
		return BrowserConversationMechanicalEvaluation{Failures: []string{err.Error()}}
	}
	var converted BrowserConversationMechanicalEvaluation
	_ = json.Unmarshal(encoded, &converted)
	return converted
}
