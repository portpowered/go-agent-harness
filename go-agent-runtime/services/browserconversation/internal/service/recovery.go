package service

// DeriveBrowserConversationRecovery exposes the same evidence derivation used
// by the shared runner to production report builders. It only derives facts
// from observed calls; it never repairs or retries a stale reference.
func DeriveBrowserConversationRecovery(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationRecoveryEvidence {
	return deriveBrowserConversationRecovery(scenario, result)
}

func deriveBrowserConversationRecovery(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationRecoveryEvidence {
	var recoveries []BrowserConversationRecoveryEvidence
	for _, step := range scenario.Steps {
		if !browserConversationRecoveryStepEligible(step) {
			continue
		}
		recoveries = append(recoveries, deriveBrowserConversationRecoveryForStep(step, result))
	}
	return recoveries
}

func browserConversationRecoveryStepEligible(step BrowserConversationStep) bool {
	return step.Navigation != nil && step.Correction == nil
}

func deriveBrowserConversationRecoveryForStep(step BrowserConversationStep, result BrowserConversationResult) BrowserConversationRecoveryEvidence {
	recovery := BrowserConversationRecoveryEvidence{
		StepID: step.ID, FromPageID: step.Navigation.FromPageID, ToPageID: step.Navigation.ToPageID,
	}
	navigationIndex, navigation := browserConversationNavigationCall(result.BrokerCalls, step.ID)
	if navigationIndex < 0 {
		return recovery
	}
	recovery.NavigationObserved = navigation.ErrorCode == ""
	recovery.PreviousGeneration = navigation.PreviousGeneration
	recovery.CurrentGeneration = navigation.Generation
	if !recovery.NavigationObserved {
		return recovery
	}
	staleIndex, stale := browserConversationStaleCall(result.BrokerCalls, step.ID, navigationIndex)
	if staleIndex < 0 {
		return recovery
	}
	setBrowserConversationStaleEvidence(&recovery, stale)
	listIndex, listed := browserConversationRelistedCall(result.BrokerCalls, step.ID, staleIndex)
	if listIndex < 0 {
		return recovery
	}
	setBrowserConversationRelistedEvidence(&recovery, listed)
	if recovery.CurrentGeneration == 0 {
		recovery.CurrentGeneration = listed.Generation
	}
	freshIndex, fresh := browserConversationFreshCall(result.BrokerCalls, step.ID, listIndex, recovery.StaleToolRef)
	if freshIndex >= 0 {
		setBrowserConversationFreshEvidence(&recovery, fresh)
	}
	recovery.Passed = browserConversationRecoveryEvidencePassed(recovery)
	return recovery
}

func browserConversationNavigationCall(calls []BrowserConversationBrokerCall, stepID string) (int, BrowserConversationBrokerCall) {
	for index, call := range calls {
		if call.StepID == stepID && call.Operation == BrowserConversationCustomerNavigate {
			return index, call
		}
	}
	return -1, BrowserConversationBrokerCall{}
}

func browserConversationStaleCall(calls []BrowserConversationBrokerCall, stepID string, after int) (int, BrowserConversationBrokerCall) {
	for index := after + 1; index < len(calls); index++ {
		call := calls[index]
		if call.StepID == stepID && call.Operation == BrowserConversationInvoke && call.Terminal && call.ErrorCode == browserConversationStaleToolRef {
			return index, call
		}
	}
	return -1, BrowserConversationBrokerCall{}
}

func browserConversationRelistedCall(calls []BrowserConversationBrokerCall, stepID string, after int) (int, BrowserConversationBrokerCall) {
	for index := after + 1; index < len(calls); index++ {
		call := calls[index]
		if call.StepID == stepID && call.Operation == BrowserConversationListTools && call.ErrorCode == "" && browserConversationOpaqueLen(call.ToolRefs) > 0 {
			return index, call
		}
	}
	return -1, BrowserConversationBrokerCall{}
}

func browserConversationFreshCall(calls []BrowserConversationBrokerCall, stepID string, after int, staleToolRef any) (int, BrowserConversationBrokerCall) {
	for index := after + 1; index < len(calls); index++ {
		call := calls[index]
		if call.StepID == stepID && call.Operation == BrowserConversationInvoke && call.Terminal &&
			browserConversationOpaqueString(call.State) == browserConversationInvocationCompleted && !opaqueEqual(call.ToolRef, staleToolRef) {
			return index, call
		}
	}
	return -1, BrowserConversationBrokerCall{}
}

func setBrowserConversationStaleEvidence(recovery *BrowserConversationRecoveryEvidence, call BrowserConversationBrokerCall) {
	recovery.StaleToolRef = call.ToolRef
	recovery.StaleInvocationID = call.InvocationID
	recovery.StaleGeneration = call.Generation
	recovery.StaleErrorCode = call.ErrorCode
	recovery.StaleRejected = true
}

func setBrowserConversationRelistedEvidence(recovery *BrowserConversationRecoveryEvidence, call BrowserConversationBrokerCall) {
	recovery.ToolsRelisted = true
	recovery.RelistedToolRefs = cloneBrowserConversationOpaque(call.ToolRefs)
	recovery.RelistedGeneration = call.Generation
}

func setBrowserConversationFreshEvidence(recovery *BrowserConversationRecoveryEvidence, call BrowserConversationBrokerCall) {
	recovery.FreshToolRef = call.ToolRef
	recovery.FreshGeneration = call.Generation
	recovery.RetryInvocationID = call.InvocationID
	recovery.FreshInvocationCompleted = true
}

func browserConversationRecoveryEvidencePassed(recovery BrowserConversationRecoveryEvidence) bool {
	return recovery.NavigationObserved && recovery.StaleRejected && recovery.ToolsRelisted && recovery.FreshInvocationCompleted &&
		browserConversationRecoveryGenerationsValid(recovery)
}

func browserConversationRecoveryGenerationsValid(recovery BrowserConversationRecoveryEvidence) bool {
	generationAdvanced := recovery.PreviousGeneration == 0 || recovery.CurrentGeneration == 0 || recovery.CurrentGeneration > recovery.PreviousGeneration
	staleGenerationMatches := recovery.PreviousGeneration == 0 || recovery.StaleGeneration == 0 || recovery.StaleGeneration == recovery.PreviousGeneration
	freshGenerationMatches := recovery.CurrentGeneration == 0 || recovery.FreshGeneration == 0 || recovery.FreshGeneration == recovery.CurrentGeneration
	relistedGenerationMatches := recovery.CurrentGeneration == 0 || recovery.RelistedGeneration == 0 || recovery.RelistedGeneration == recovery.CurrentGeneration
	return generationAdvanced && staleGenerationMatches && freshGenerationMatches && relistedGenerationMatches
}

func browserConversationRecoveryFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	recoveries := result.Recovery
	if len(recoveries) == 0 {
		recoveries = deriveBrowserConversationRecovery(scenario, result)
	}
	var failures []string
	for _, recovery := range recoveries {
		failures = append(failures, browserConversationRecoveryFailureMessages(recovery)...)
	}
	return failures
}

func browserConversationRecoveryFailureMessages(recovery BrowserConversationRecoveryEvidence) []string {
	stepID := safeBrowserConversationText(recovery.StepID)
	var failures []string
	if !recovery.NavigationObserved {
		failures = append(failures, "step "+stepID+": customer navigation was not observed")
	}
	if !recovery.StaleRejected {
		failures = append(failures, "step "+stepID+": stale tool reference was not rejected as stale_tool_ref")
	}
	if !recovery.ToolsRelisted {
		failures = append(failures, "step "+stepID+": fresh tool catalog was not listed after stale reference rejection")
	}
	if !recovery.FreshInvocationCompleted {
		failures = append(failures, "step "+stepID+": fresh tool reference was not invoked to completion")
	}
	return append(failures, browserConversationRecoveryGenerationFailures(stepID, recovery)...)
}

func browserConversationRecoveryGenerationFailures(stepID string, recovery BrowserConversationRecoveryEvidence) []string {
	var failures []string
	failures = append(failures, browserConversationNavigationGenerationFailure(stepID, recovery)...)
	failures = append(failures, browserConversationStaleGenerationFailure(stepID, recovery)...)
	failures = append(failures, browserConversationRelistedGenerationFailure(stepID, recovery)...)
	failures = append(failures, browserConversationFreshGenerationFailure(stepID, recovery)...)
	return failures
}

func browserConversationNavigationGenerationFailure(stepID string, recovery BrowserConversationRecoveryEvidence) []string {
	if recovery.NavigationObserved && recovery.PreviousGeneration != 0 && recovery.CurrentGeneration != 0 && recovery.CurrentGeneration <= recovery.PreviousGeneration {
		return []string{"step " + stepID + ": customer navigation did not advance the page generation"}
	}
	return nil
}

func browserConversationStaleGenerationFailure(stepID string, recovery BrowserConversationRecoveryEvidence) []string {
	if recovery.StaleRejected && recovery.PreviousGeneration != 0 && recovery.StaleGeneration != 0 && recovery.StaleGeneration != recovery.PreviousGeneration {
		return []string{"step " + stepID + ": stale invocation did not use the pre-navigation generation"}
	}
	return nil
}

func browserConversationRelistedGenerationFailure(stepID string, recovery BrowserConversationRecoveryEvidence) []string {
	if recovery.ToolsRelisted && recovery.CurrentGeneration != 0 && recovery.RelistedGeneration != 0 && recovery.RelistedGeneration != recovery.CurrentGeneration {
		return []string{"step " + stepID + ": re-listed tools did not belong to the current generation"}
	}
	return nil
}

func browserConversationFreshGenerationFailure(stepID string, recovery BrowserConversationRecoveryEvidence) []string {
	if recovery.FreshInvocationCompleted && recovery.CurrentGeneration != 0 && recovery.FreshGeneration != 0 && recovery.FreshGeneration != recovery.CurrentGeneration {
		return []string{"step " + stepID + ": fresh invocation did not use the current generation"}
	}
	return nil
}
