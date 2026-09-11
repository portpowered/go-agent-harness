package browserscenario

import "encoding/json"

func cloneBrowserConversationResult(result BrowserConversationResult) BrowserConversationResult {
	clone := result
	clone.Turns = append([]BrowserConversationTurn(nil), result.Turns...)
	clone.BrokerCalls = make([]BrowserConversationBrokerCall, len(result.BrokerCalls))
	for index, call := range result.BrokerCalls {
		clone.BrokerCalls[index] = cloneBrowserConversationBrokerCall(call)
	}
	clone.InputJSONValidity = computeBrowserConversationInputJSONValidity(clone.BrokerCalls)
	clone.Oracles = make([]BrowserConversationOracleSnapshot, len(result.Oracles))
	for index, snapshot := range result.Oracles {
		clone.Oracles[index] = cloneBrowserConversationOracleSnapshot(snapshot)
	}
	clone.Corrections = cloneBrowserConversationCorrections(result.Corrections)
	clone.Recovery = cloneBrowserConversationRecoveries(result.Recovery)
	clone.Cancellation.InvocationID = cloneBrowserConversationOpaque(result.Cancellation.InvocationID)
	clone.Cancellation.FinalState = cloneBrowserConversationOpaque(result.Cancellation.FinalState)
	clone.Lifecycle.ExternalBrowserID = cloneBrowserConversationOpaque(result.Lifecycle.ExternalBrowserID)
	clone.Lifecycle.ExternalTargetID = cloneBrowserConversationOpaque(result.Lifecycle.ExternalTargetID)
	clone.Mechanical = cloneBrowserConversationMechanicalEvaluation(result.Mechanical)
	clone.Validator = cloneBrowserConversationValidatorVerdict(result.Validator)
	return clone
}

func cloneBrowserConversationTurn(turn BrowserConversationTurn) BrowserConversationTurn {
	return turn
}

func cloneBrowserConversationBrokerCall(call BrowserConversationBrokerCall) BrowserConversationBrokerCall {
	clone := call
	clone.ToolRef = cloneBrowserConversationOpaque(call.ToolRef)
	clone.InvocationID = cloneBrowserConversationOpaque(call.InvocationID)
	clone.State = cloneBrowserConversationOpaque(call.State)
	clone.Output = append(json.RawMessage(nil), call.Output...)
	clone.ToolRefs = cloneBrowserConversationOpaque(call.ToolRefs)
	return clone
}

func cloneBrowserConversationRecoveries(recoveries []BrowserConversationRecoveryEvidence) []BrowserConversationRecoveryEvidence {
	if recoveries == nil {
		return nil
	}
	clone := make([]BrowserConversationRecoveryEvidence, len(recoveries))
	for index, recovery := range recoveries {
		clone[index] = recovery
		clone[index].StaleToolRef = cloneBrowserConversationOpaque(recovery.StaleToolRef)
		clone[index].StaleInvocationID = cloneBrowserConversationOpaque(recovery.StaleInvocationID)
		clone[index].RelistedToolRefs = cloneBrowserConversationOpaque(recovery.RelistedToolRefs)
		clone[index].FreshToolRef = cloneBrowserConversationOpaque(recovery.FreshToolRef)
		clone[index].RetryInvocationID = cloneBrowserConversationOpaque(recovery.RetryInvocationID)
	}
	return clone
}

func cloneBrowserConversationCorrections(corrections []BrowserConversationCorrectionEvidence) []BrowserConversationCorrectionEvidence {
	if corrections == nil {
		return nil
	}
	clone := make([]BrowserConversationCorrectionEvidence, len(corrections))
	for index, correction := range corrections {
		clone[index] = correction
		clone[index].OriginalBefore = append(json.RawMessage(nil), correction.OriginalBefore...)
		clone[index].OriginalAfter = append(json.RawMessage(nil), correction.OriginalAfter...)
		clone[index].CorrectionBefore = append(json.RawMessage(nil), correction.CorrectionBefore...)
		clone[index].CorrectionAfter = append(json.RawMessage(nil), correction.CorrectionAfter...)
		clone[index].OriginalInvocationID = cloneBrowserConversationOpaque(correction.OriginalInvocationID)
		clone[index].CorrectionInvocationID = cloneBrowserConversationOpaque(correction.CorrectionInvocationID)
	}
	return clone
}

func cloneBrowserConversationOracleSnapshot(snapshot BrowserConversationOracleSnapshot) BrowserConversationOracleSnapshot {
	clone := snapshot
	clone.State = append(json.RawMessage(nil), snapshot.State...)
	return clone
}

func cloneBrowserConversationMechanicalEvaluation(evaluation BrowserConversationMechanicalEvaluation) BrowserConversationMechanicalEvaluation {
	clone := evaluation
	clone.Failures = append([]string(nil), evaluation.Failures...)
	return clone
}

func cloneBrowserConversationValidatorVerdict(verdict BrowserConversationValidatorVerdict) BrowserConversationValidatorVerdict {
	clone := verdict
	clone.Checks = append([]BrowserConversationValidatorCheck(nil), verdict.Checks...)
	return clone
}
