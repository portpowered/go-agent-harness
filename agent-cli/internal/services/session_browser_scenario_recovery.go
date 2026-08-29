package services

import "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"

func deriveBrowserConversationRecovery(scenario BrowserConversationScenario, result BrowserConversationResult) []BrowserConversationRecoveryEvidence {
	var recoveries []BrowserConversationRecoveryEvidence
	for _, step := range scenario.Steps {
		if step.Navigation == nil {
			continue
		}
		recovery := BrowserConversationRecoveryEvidence{
			StepID:     step.ID,
			FromPageID: step.Navigation.FromPageID,
			ToPageID:   step.Navigation.ToPageID,
		}
		navigationIndex := -1
	forCalls:
		for index := range result.BrokerCalls {
			call := result.BrokerCalls[index]
			if call.StepID == step.ID && call.Operation == BrowserConversationCustomerNavigate {
				navigationIndex = index
				recovery.NavigationObserved = call.ErrorCode == ""
				recovery.PreviousGeneration = call.PreviousGeneration
				recovery.CurrentGeneration = call.Generation
				break forCalls
			}
		}
		if navigationIndex >= 0 && !recovery.NavigationObserved {
			recoveries = append(recoveries, recovery)
			continue
		}
		if navigationIndex < 0 {
			recoveries = append(recoveries, recovery)
			continue
		}

		staleIndex := -1
		for index := navigationIndex + 1; index < len(result.BrokerCalls); index++ {
			call := result.BrokerCalls[index]
			if call.StepID != step.ID || call.Operation != BrowserConversationInvoke || !call.Terminal || call.ErrorCode != string(webmcp.ErrorStaleToolRef) {
				continue
			}
			staleIndex = index
			recovery.StaleToolRef = call.ToolRef
			recovery.StaleInvocationID = call.InvocationID
			recovery.StaleGeneration = call.Generation
			recovery.StaleErrorCode = call.ErrorCode
			recovery.StaleRejected = true
			break
		}

		listIndex := -1
		if staleIndex >= 0 {
			for index := staleIndex + 1; index < len(result.BrokerCalls); index++ {
				call := result.BrokerCalls[index]
				if call.StepID != step.ID || call.Operation != BrowserConversationListTools || call.ErrorCode != "" || len(call.ToolRefs) == 0 {
					continue
				}
				listIndex = index
				recovery.ToolsRelisted = true
				recovery.RelistedToolRefs = append([]webmcp.ToolRef(nil), call.ToolRefs...)
				recovery.RelistedGeneration = call.Generation
				if recovery.CurrentGeneration == 0 {
					recovery.CurrentGeneration = call.Generation
				}
				break
			}
		}

		if listIndex >= 0 {
			for index := listIndex + 1; index < len(result.BrokerCalls); index++ {
				call := result.BrokerCalls[index]
				if call.StepID != step.ID || call.Operation != BrowserConversationInvoke || !call.Terminal || call.State != webmcp.InvocationCompleted || call.ToolRef == recovery.StaleToolRef {
					continue
				}
				recovery.FreshToolRef = call.ToolRef
				recovery.FreshGeneration = call.Generation
				recovery.RetryInvocationID = call.InvocationID
				recovery.FreshInvocationCompleted = true
				break
			}
		}
		generationAdvanced := recovery.PreviousGeneration == 0 || recovery.CurrentGeneration == 0 || recovery.CurrentGeneration > recovery.PreviousGeneration
		staleGenerationMatches := recovery.PreviousGeneration == 0 || recovery.StaleGeneration == 0 || recovery.StaleGeneration == recovery.PreviousGeneration
		freshGenerationMatches := recovery.CurrentGeneration == 0 || recovery.FreshGeneration == 0 || recovery.FreshGeneration == recovery.CurrentGeneration
		relistedGenerationMatches := recovery.CurrentGeneration == 0 || recovery.RelistedGeneration == 0 || recovery.RelistedGeneration == recovery.CurrentGeneration
		recovery.Passed = recovery.NavigationObserved && recovery.StaleRejected && recovery.ToolsRelisted && recovery.FreshInvocationCompleted && generationAdvanced && staleGenerationMatches && freshGenerationMatches && relistedGenerationMatches
		recoveries = append(recoveries, recovery)
	}
	return recoveries
}

func browserConversationRecoveryFailures(scenario BrowserConversationScenario, result BrowserConversationResult) []string {
	recoveries := result.Recovery
	if len(recoveries) == 0 {
		recoveries = deriveBrowserConversationRecovery(scenario, result)
	}
	var failures []string
	for _, recovery := range recoveries {
		stepID := safeBrowserConversationText(recovery.StepID)
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
		if recovery.NavigationObserved && recovery.PreviousGeneration != 0 && recovery.CurrentGeneration != 0 && recovery.CurrentGeneration <= recovery.PreviousGeneration {
			failures = append(failures, "step "+stepID+": customer navigation did not advance the page generation")
		}
		if recovery.StaleRejected && recovery.PreviousGeneration != 0 && recovery.StaleGeneration != 0 && recovery.StaleGeneration != recovery.PreviousGeneration {
			failures = append(failures, "step "+stepID+": stale invocation did not use the pre-navigation generation")
		}
		if recovery.ToolsRelisted && recovery.CurrentGeneration != 0 && recovery.RelistedGeneration != 0 && recovery.RelistedGeneration != recovery.CurrentGeneration {
			failures = append(failures, "step "+stepID+": re-listed tools did not belong to the current generation")
		}
		if recovery.FreshInvocationCompleted && recovery.CurrentGeneration != 0 && recovery.FreshGeneration != 0 && recovery.FreshGeneration != recovery.CurrentGeneration {
			failures = append(failures, "step "+stepID+": fresh invocation did not use the current generation")
		}
	}
	return failures
}
