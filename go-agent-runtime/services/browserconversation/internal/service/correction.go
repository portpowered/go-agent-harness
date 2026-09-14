package service

import "encoding/json"

// DeriveBrowserConversationCorrections exposes the shared correction evidence
// derivation to live report builders without introducing a second evaluator.
func DeriveBrowserConversationCorrections(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationCorrectionEvidence {
	return deriveBrowserConversationCorrections(scenario, result)
}

// deriveBrowserConversationCorrections joins the original and correcting
// turns, completed invocations, and oracle pairs after the session has
// stopped. Keeping this derivation separate from collection means a plausible
// assistant response cannot manufacture correction evidence.
func deriveBrowserConversationCorrections(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationCorrectionEvidence {
	var corrections []BrowserConversationCorrectionEvidence
	for _, step := range scenario.Steps {
		if step.Correction == nil {
			continue
		}
		corrections = append(corrections, deriveBrowserConversationCorrection(scenario, result, step))
	}
	return corrections
}

func deriveBrowserConversationCorrection(scenario BrowserConversationScenario, result BrowserConversationResult, step BrowserConversationStep) BrowserConversationCorrectionEvidence {
	correction := BrowserConversationCorrectionEvidence{
		StepID:              step.ID,
		TargetStepID:        step.Correction.TargetStepID,
		CorrectionUtterance: step.Utterance,
	}
	if targetStep := browserConversationStepByID(scenario, step.Correction.TargetStepID); targetStep != nil {
		correction.TargetUtterance = targetStep.Utterance
	}
	addBrowserConversationCorrectionTurns(&correction, result.Turns, step)
	addBrowserConversationCorrectionOracles(&correction, result.Oracles, step)
	addBrowserConversationCorrectionInvocations(&correction, result.BrokerCalls, step)
	correction.Passed = browserConversationCorrectionEvidencePassed(scenario, step, correction, result)
	return correction
}

func addBrowserConversationCorrectionTurns(correction *BrowserConversationCorrectionEvidence, turns []BrowserConversationTurn, step BrowserConversationStep) {
	if targetTurn, assistantTurn := browserConversationTurnsForStep(turns, step.Correction.TargetStepID); targetTurn != nil {
		correction.TargetUtterance = targetTurn.ObservedText
		if assistantTurn != nil {
			correction.OriginalAssistantText = assistantTurn.ObservedText
		}
	}
	if _, assistantTurn := browserConversationTurnsForStep(turns, step.ID); assistantTurn != nil {
		correction.CorrectionAssistantText = assistantTurn.ObservedText
	}
}

func addBrowserConversationCorrectionOracles(correction *BrowserConversationCorrectionEvidence, oracles []BrowserConversationOracleSnapshot, step BrowserConversationStep) {
	if before := browserConversationOracleForStep(oracles, step.Correction.TargetStepID, BrowserConversationOracleBefore); before != nil {
		correction.OriginalBefore = append(json.RawMessage(nil), before.State...)
	}
	if after := browserConversationOracleForStep(oracles, step.Correction.TargetStepID, BrowserConversationOracleAfter); after != nil {
		correction.OriginalAfter = append(json.RawMessage(nil), after.State...)
	}
	if before := browserConversationOracleForStep(oracles, step.ID, BrowserConversationOracleBefore); before != nil {
		correction.CorrectionBefore = append(json.RawMessage(nil), before.State...)
	}
	if after := browserConversationOracleForStep(oracles, step.ID, BrowserConversationOracleAfter); after != nil {
		correction.CorrectionAfter = append(json.RawMessage(nil), after.State...)
	}
}

func addBrowserConversationCorrectionInvocations(correction *BrowserConversationCorrectionEvidence, calls []BrowserConversationBrokerCall, step BrowserConversationStep) {
	if call := browserConversationTerminalInvokeForStep(calls, step.Correction.TargetStepID); call != nil {
		correction.OriginalInvocationID = call.InvocationID
		correction.OriginalToolName = call.ToolName
		correction.OriginalInvocationCompleted = true
	}
	if call := browserConversationTerminalInvokeForStep(calls, step.ID); call != nil {
		correction.CorrectionInvocationID = call.InvocationID
		correction.CorrectionToolName = call.ToolName
		correction.CorrectionInvocationCompleted = true
	}
}

func browserConversationCorrectionEvidencePassed(scenario BrowserConversationScenario, step BrowserConversationStep, evidence BrowserConversationCorrectionEvidence, result BrowserConversationResult) bool {
	if !browserConversationCorrectionDefinitionValid(scenario, step, evidence) {
		return false
	}
	if !browserConversationCorrectionStatesMatch(scenario, step, evidence) {
		return false
	}
	if !browserConversationCorrectionTurnsValid(step, evidence, result) {
		return false
	}
	return browserConversationCorrectionInvocationOrderValid(step, result)
}

func browserConversationCorrectionDefinitionValid(scenario BrowserConversationScenario, step BrowserConversationStep, evidence BrowserConversationCorrectionEvidence) bool {
	if step.Correction == nil {
		return false
	}
	targetStep := browserConversationStepByID(scenario, step.Correction.TargetStepID)
	if targetStep == nil {
		return false
	}
	if browserConversationExpectedState(targetStep) == nil || browserConversationExpectedState(&step) == nil {
		return false
	}
	return evidence.OriginalInvocationCompleted && evidence.CorrectionInvocationCompleted
}

func browserConversationCorrectionStatesMatch(scenario BrowserConversationScenario, step BrowserConversationStep, evidence BrowserConversationCorrectionEvidence) bool {
	targetStep := browserConversationStepByID(scenario, step.Correction.TargetStepID)
	if targetStep == nil {
		return false
	}
	targetTransition := browserConversationExpectedState(targetStep)
	correctionTransition := browserConversationExpectedState(&step)
	if targetTransition == nil || correctionTransition == nil {
		return false
	}
	return browserConversationJSONEqual(evidence.OriginalBefore, targetTransition.Before) &&
		browserConversationJSONEqual(evidence.OriginalAfter, targetTransition.After) &&
		browserConversationJSONEqual(evidence.CorrectionBefore, correctionTransition.Before) &&
		browserConversationJSONEqual(evidence.CorrectionAfter, correctionTransition.After) &&
		!browserConversationJSONEqual(evidence.OriginalAfter, evidence.CorrectionAfter) &&
		!browserConversationJSONEqual(evidence.CorrectionBefore, evidence.CorrectionAfter)
}

func browserConversationCorrectionTurnsValid(step BrowserConversationStep, evidence BrowserConversationCorrectionEvidence, result BrowserConversationResult) bool {
	targetStep := step.Correction.TargetStepID
	targetCustomer, targetAssistant := browserConversationTurnsForStep(result.Turns, targetStep)
	correctionCustomer, correctionAssistant := browserConversationTurnsForStep(result.Turns, step.ID)
	return targetCustomer != nil && targetAssistant != nil && correctionCustomer != nil && correctionAssistant != nil &&
		targetAssistant.ObservedText == evidence.OriginalAssistantText && correctionAssistant.ObservedText == evidence.CorrectionAssistantText
}

func browserConversationCorrectionInvocationOrderValid(step BrowserConversationStep, result BrowserConversationResult) bool {
	targetCall := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.Correction.TargetStepID)
	correctionCall := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.ID)
	if targetCall == nil || correctionCall == nil || targetCall.Sequence >= correctionCall.Sequence {
		return false
	}
	_, targetAssistant := browserConversationTurnsForStep(result.Turns, step.Correction.TargetStepID)
	_, correctionAssistant := browserConversationTurnsForStep(result.Turns, step.ID)
	return targetAssistant != nil && correctionAssistant != nil && targetAssistant.Sequence > targetCall.Sequence && correctionAssistant.Sequence > correctionCall.Sequence
}

func browserConversationCorrectionFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	corrections := result.Corrections
	if len(corrections) == 0 {
		corrections = deriveBrowserConversationCorrections(scenario, result)
	}
	var failures []string
	for _, correction := range corrections {
		failures = append(failures, browserConversationCorrectionFailureMessages(correction)...)
	}
	return failures
}

func browserConversationCorrectionFailureMessages(correction BrowserConversationCorrectionEvidence) []string {
	stepID := safeBrowserConversationText(correction.StepID)
	targetID := safeBrowserConversationText(correction.TargetStepID)
	var failures []string
	if !correction.OriginalInvocationCompleted {
		failures = append(failures, "step "+targetID+": original browser action lacks a completed terminal invocation")
	}
	if !correction.CorrectionInvocationCompleted {
		failures = append(failures, "step "+stepID+": correction lacks a completed terminal invocation")
	}
	failures = append(failures, browserConversationCorrectionStateFailures(stepID, targetID, correction)...)
	if correction.OriginalAssistantText == "" {
		failures = append(failures, "step "+targetID+": original action is missing assistant confirmation")
	}
	if correction.CorrectionAssistantText == "" {
		failures = append(failures, "step "+stepID+": correction is missing assistant confirmation")
	}
	if !correction.Passed {
		failures = append(failures, "step "+stepID+": correction evidence did not prove the corrected intent")
	}
	return failures
}

func browserConversationCorrectionStateFailures(stepID, targetID string, correction BrowserConversationCorrectionEvidence) []string {
	var failures []string
	if len(correction.OriginalBefore) == 0 || len(correction.OriginalAfter) == 0 {
		failures = append(failures, "step "+targetID+": original action is missing an independent oracle transition")
	}
	if len(correction.CorrectionBefore) == 0 || len(correction.CorrectionAfter) == 0 {
		failures = append(failures, "step "+stepID+": correction is missing an independent oracle transition")
	}
	if len(correction.OriginalAfter) != 0 && len(correction.CorrectionAfter) != 0 && browserConversationJSONEqual(correction.OriginalAfter, correction.CorrectionAfter) {
		failures = append(failures, "step "+stepID+": correction left the superseded state in place")
	}
	if len(correction.CorrectionBefore) != 0 && len(correction.CorrectionAfter) != 0 && browserConversationJSONEqual(correction.CorrectionBefore, correction.CorrectionAfter) {
		failures = append(failures, "step "+stepID+": correction oracle shows no state change")
	}
	return failures
}
