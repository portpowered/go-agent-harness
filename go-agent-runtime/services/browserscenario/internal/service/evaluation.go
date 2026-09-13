package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// EvaluateBrowserConversation validates and evaluates one externally collected
// browser conversation. Live production runners use this boundary after they
// have joined their session-log, browser-event, oracle, and lifecycle
// observations into the same result contract as the hermetic runner.
func EvaluateBrowserConversation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) (BrowserConversationMechanicalEvaluation, error) {
	normalizedScenario, err := scenario.Admit()
	if err != nil {
		return BrowserConversationMechanicalEvaluation{}, err
	}
	if err := result.Validate(); err != nil {
		return BrowserConversationMechanicalEvaluation{}, err
	}
	return evaluateBrowserConversation(normalizedScenario, result, rootErr), nil
}

func evaluateBrowserConversation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) BrowserConversationMechanicalEvaluation {
	var failures []string
	failures = append(failures, browserConversationRootFailures(scenario, result, rootErr)...)
	failures = append(failures, browserConversationCancellationFailures(scenario, result)...)
	failures = append(failures, browserConversationStepFailures(scenario, result)...)
	failures = append(failures, browserConversationCorrectionFailures(scenario, result)...)
	failures = append(failures, browserConversationRecoveryFailures(scenario, result)...)
	failures = append(failures, browserConversationPostSessionFailures(scenario, result)...)
	return BrowserConversationMechanicalEvaluation{Passed: len(failures) == 0, Failures: failures}
}

func browserConversationRootFailures(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) []string {
	if rootErr == nil || browserConversationExpectedCancellation(scenario, result, rootErr) {
		return nil
	}
	return []string{"run: " + safeBrowserConversationError(rootErr)}
}

func browserConversationCancellationFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	var failures []string
	if browserConversationHasInterruption(scenario) {
		failures = append(failures, browserConversationInterruptionFailures(result)...)
	}
	if browserConversationHasCancel(scenario) {
		failures = append(failures, browserConversationCancelFailures(result)...)
	}
	return failures
}

func browserConversationInterruptionFailures(result BrowserConversationResult) []string {
	var failures []string
	if !result.Cancellation.Interrupted {
		failures = append(failures, "cancellation: declared interruption was not observed")
	}
	if browserConversationOpaqueString(result.Cancellation.InvocationID) == "" {
		failures = append(failures, "cancellation: interrupted invocation identity is missing")
	}
	if browserConversationOpaqueString(result.Cancellation.FinalState) != browserConversationInvocationCanceled {
		failures = append(failures, "cancellation: interrupted invocation did not reach canceled terminal state")
	}
	if !result.Cancellation.OverlappingAudioSent {
		failures = append(failures, "cancellation: overlapping customer audio was not sent")
	}
	return failures
}

func browserConversationCancelFailures(result BrowserConversationResult) []string {
	var failures []string
	if !result.Cancellation.Requested {
		failures = append(failures, "cancellation: explicit customer cancel was not observed")
	}
	if browserConversationOpaqueString(result.Cancellation.FinalState) != browserConversationInvocationCanceled {
		failures = append(failures, "cancellation: explicit cancel did not preserve a canceled terminal state")
	}
	return failures
}

func browserConversationStepFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	var failures []string
	for _, step := range scenario.Steps {
		failures = append(failures, browserConversationStepFailure(step, result)...)
	}
	return failures
}

func browserConversationStepFailure(step BrowserConversationStep, result BrowserConversationResult) []string {
	var failures []string
	customer, assistant := browserConversationTurnsForStep(result.Turns, step.ID)
	stepID := safeBrowserConversationText(step.ID)
	if customer == nil {
		failures = append(failures, "step "+stepID+": missing customer transcript")
	} else if !strings.EqualFold(strings.TrimSpace(customer.ObservedText), strings.TrimSpace(step.Utterance)) {
		failures = append(failures, "step "+stepID+": customer transcript does not match utterance")
	}
	if assistant == nil && !browserConversationAssistantOptional(step, result) {
		failures = append(failures, "step "+stepID+": missing assistant turn")
	}
	return append(failures, browserConversationExpectedStateFailures(step, result)...)
}

func browserConversationAssistantOptional(step BrowserConversationStep, result BrowserConversationResult) bool {
	return (result.Cancellation.Interrupted && result.Cancellation.InterruptedStepID == step.ID) ||
		(step.Interrupt != nil && result.Cancellation.Interrupted) ||
		(step.Cancel != nil && result.Cancellation.CancelStepID == step.ID)
}

func browserConversationExpectedStateFailures(step BrowserConversationStep, result BrowserConversationResult) []string {
	expectedState := browserConversationExpectedState(&step)
	if expectedState == nil {
		return nil
	}
	stepID := safeBrowserConversationText(step.ID)
	var failures []string
	before := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleBefore)
	if before == nil {
		failures = append(failures, "step "+stepID+": missing before oracle")
	} else if !browserConversationJSONEqual(before.State, expectedState.Before) {
		failures = append(failures, "step "+stepID+": before oracle mismatch")
	}
	after := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleAfter)
	if after == nil {
		failures = append(failures, "step "+stepID+": missing after oracle")
	} else if !browserConversationJSONEqual(after.State, expectedState.After) {
		failures = append(failures, "step "+stepID+": after oracle mismatch")
	}
	terminalInvoke := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.ID)
	if terminalInvoke == nil {
		return append(failures, "step "+stepID+": missing terminal tool result")
	}
	if assistant := browserConversationAssistantTurnForStep(result.Turns, step.ID); assistant != nil && assistant.Sequence <= terminalInvoke.Sequence {
		failures = append(failures, "step "+stepID+": assistant turn was observed before the completed browser invocation")
	}
	return failures
}

func browserConversationAssistantTurnForStep(turns []BrowserConversationTurn, stepID string) *BrowserConversationTurn {
	_, assistant := browserConversationTurnsForStep(turns, stepID)
	return assistant
}

func browserConversationPostSessionFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	var failures []string
	if browserConversationOracleForStep(result.Oracles, "", BrowserConversationOraclePostSession) == nil {
		failures = append(failures, "post-session: missing independent oracle")
	}
	required := scenario.PostSession
	if !result.Lifecycle.ExternalTabAlive && required.MustRemainAlive {
		failures = append(failures, "post-session: external tab is not alive")
	}
	if !result.Lifecycle.ExternalTabResponsive && required.MustBeResponsive {
		failures = append(failures, "post-session: external tab is not responsive")
	}
	if !result.Lifecycle.ExternalTabAllowsMutation && required.MustAllowMutation {
		failures = append(failures, "post-session: external tab does not allow mutation")
	}
	if result.Lifecycle.BrowserClosed {
		failures = append(failures, "lifecycle: externally owned browser was closed")
	}
	if result.Lifecycle.TargetClosed {
		failures = append(failures, "lifecycle: externally owned target was closed")
	}
	return append(failures, browserConversationLifecycleFailures(result.Lifecycle)...)
}

func browserConversationLifecycleFailures(lifecycle BrowserConversationLifecycleEvidence) []string {
	var failures []string
	if lifecycle.DetachRequired && lifecycle.DetachCount != 1 {
		failures = append(failures, fmt.Sprintf("lifecycle: fixture detached %d times, want exactly once", lifecycle.DetachCount))
	}
	if !lifecycle.SessionStarted {
		failures = append(failures, "lifecycle: session was not started")
	}
	if !lifecycle.SessionTerminated {
		failures = append(failures, "lifecycle: session was not terminated")
	}
	return failures
}

func browserConversationHasInterruption(scenario BrowserConversationScenario) bool {
	for _, step := range scenario.Steps {
		if step.Interrupt != nil {
			return true
		}
	}
	return false
}

func browserConversationHasCancel(scenario BrowserConversationScenario) bool {
	for _, step := range scenario.Steps {
		if step.Cancel != nil {
			return true
		}
	}
	return false
}

func browserConversationExpectedCancellation(scenario BrowserConversationScenario, result BrowserConversationResult, rootErr error) bool {
	if !browserConversationHasInterruption(scenario) && !browserConversationHasCancel(scenario) {
		return false
	}
	if result.Lifecycle.Outcome != BrowserConversationLifecycleCanceled ||
		!result.Cancellation.Requested || browserConversationOpaqueString(result.Cancellation.FinalState) != browserConversationInvocationCanceled {
		return false
	}
	if rootErr == nil {
		return true
	}
	return errors.Is(rootErr, context.Canceled) || errors.Is(rootErr, ErrBrowserConversationSession)
}
