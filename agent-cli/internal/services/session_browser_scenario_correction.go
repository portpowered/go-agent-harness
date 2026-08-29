package services

import (
	"encoding/json"
)

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
		targetStep := browserConversationStepByID(scenario, step.Correction.TargetStepID)
		correction := BrowserConversationCorrectionEvidence{
			StepID:              step.ID,
			TargetStepID:        step.Correction.TargetStepID,
			CorrectionUtterance: step.Utterance,
		}
		if targetStep != nil {
			correction.TargetUtterance = targetStep.Utterance
		}

		if targetTurn, assistantTurn := browserConversationTurnsForStep(result.Turns, step.Correction.TargetStepID); targetTurn != nil {
			correction.TargetUtterance = targetTurn.ObservedText
			if assistantTurn != nil {
				correction.OriginalAssistantText = assistantTurn.ObservedText
			}
		}
		if _, assistantTurn := browserConversationTurnsForStep(result.Turns, step.ID); assistantTurn != nil {
			correction.CorrectionAssistantText = assistantTurn.ObservedText
		}

		if before := browserConversationOracleForStep(result.Oracles, step.Correction.TargetStepID, BrowserConversationOracleBefore); before != nil {
			correction.OriginalBefore = append(json.RawMessage(nil), before.State...)
		}
		if after := browserConversationOracleForStep(result.Oracles, step.Correction.TargetStepID, BrowserConversationOracleAfter); after != nil {
			correction.OriginalAfter = append(json.RawMessage(nil), after.State...)
		}
		if before := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleBefore); before != nil {
			correction.CorrectionBefore = append(json.RawMessage(nil), before.State...)
		}
		if after := browserConversationOracleForStep(result.Oracles, step.ID, BrowserConversationOracleAfter); after != nil {
			correction.CorrectionAfter = append(json.RawMessage(nil), after.State...)
		}

		if call := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.Correction.TargetStepID); call != nil {
			correction.OriginalInvocationID = call.InvocationID
			correction.OriginalToolName = call.ToolName
			correction.OriginalInvocationCompleted = true
		}
		if call := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.ID); call != nil {
			correction.CorrectionInvocationID = call.InvocationID
			correction.CorrectionToolName = call.ToolName
			correction.CorrectionInvocationCompleted = true
		}
		correction.Passed = browserConversationCorrectionEvidencePassed(scenario, step, correction, result)
		corrections = append(corrections, correction)
	}
	return corrections
}

func browserConversationCorrectionEvidencePassed(
	scenario BrowserConversationScenario,
	step BrowserConversationStep,
	evidence BrowserConversationCorrectionEvidence,
	result BrowserConversationResult,
) bool {
	if step.Correction == nil {
		return false
	}
	targetStep := browserConversationStepByID(scenario, step.Correction.TargetStepID)
	if targetStep == nil {
		return false
	}
	targetTransition := browserConversationExpectedState(targetStep)
	correctionTransition := browserConversationExpectedState(&step)
	if targetTransition == nil || correctionTransition == nil {
		return false
	}
	if !evidence.OriginalInvocationCompleted || !evidence.CorrectionInvocationCompleted {
		return false
	}
	if !browserConversationJSONEqual(evidence.OriginalBefore, targetTransition.Before) ||
		!browserConversationJSONEqual(evidence.OriginalAfter, targetTransition.After) ||
		!browserConversationJSONEqual(evidence.CorrectionBefore, correctionTransition.Before) ||
		!browserConversationJSONEqual(evidence.CorrectionAfter, correctionTransition.After) {
		return false
	}
	// The correction can happen after other valid actions or navigation have
	// changed unrelated fields. The independent transition pairs must match
	// their declared expectations; they do not need byte-for-byte equality at
	// the hand-off between the original and correction.
	if browserConversationJSONEqual(evidence.OriginalAfter, evidence.CorrectionAfter) ||
		browserConversationJSONEqual(evidence.CorrectionBefore, evidence.CorrectionAfter) {
		return false
	}
	targetCustomer, targetAssistant := browserConversationTurnsForStep(result.Turns, targetStep.ID)
	correctionCustomer, correctionAssistant := browserConversationTurnsForStep(result.Turns, step.ID)
	if targetCustomer == nil || targetAssistant == nil || correctionCustomer == nil || correctionAssistant == nil {
		return false
	}
	targetCall := browserConversationTerminalInvokeForStep(result.BrokerCalls, targetStep.ID)
	correctionCall := browserConversationTerminalInvokeForStep(result.BrokerCalls, step.ID)
	if targetCall == nil || correctionCall == nil || targetCall.Sequence >= correctionCall.Sequence {
		return false
	}
	return targetAssistant.Sequence > targetCall.Sequence && correctionAssistant.Sequence > correctionCall.Sequence
}

func browserConversationCorrectionFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	corrections := result.Corrections
	if len(corrections) == 0 {
		corrections = deriveBrowserConversationCorrections(scenario, result)
	}
	var failures []string
	for _, correction := range corrections {
		stepID := safeBrowserConversationText(correction.StepID)
		targetID := safeBrowserConversationText(correction.TargetStepID)
		if !correction.OriginalInvocationCompleted {
			failures = append(failures, "step "+targetID+": original browser action lacks a completed terminal invocation")
		}
		if !correction.CorrectionInvocationCompleted {
			failures = append(failures, "step "+stepID+": correction lacks a completed terminal invocation")
		}
		if len(correction.OriginalBefore) == 0 || len(correction.OriginalAfter) == 0 {
			failures = append(failures, "step "+targetID+": original action is missing an independent oracle transition")
		}
		if len(correction.CorrectionBefore) == 0 || len(correction.CorrectionAfter) == 0 {
			failures = append(failures, "step "+stepID+": correction is missing an independent oracle transition")
		}
		if len(correction.OriginalAfter) != 0 && len(correction.CorrectionAfter) != 0 &&
			browserConversationJSONEqual(correction.OriginalAfter, correction.CorrectionAfter) {
			failures = append(failures, "step "+stepID+": correction left the superseded state in place")
		}
		if len(correction.CorrectionBefore) != 0 && len(correction.CorrectionAfter) != 0 &&
			browserConversationJSONEqual(correction.CorrectionBefore, correction.CorrectionAfter) {
			failures = append(failures, "step "+stepID+": correction oracle shows no state change")
		}
		if correction.OriginalAssistantText == "" {
			failures = append(failures, "step "+targetID+": original action is missing assistant confirmation")
		}
		if correction.CorrectionAssistantText == "" {
			failures = append(failures, "step "+stepID+": correction is missing assistant confirmation")
		}
		if !correction.Passed {
			failures = append(failures, "step "+stepID+": correction evidence did not prove the corrected intent")
		}
	}
	return failures
}
