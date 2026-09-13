package browserscenario

import (
	"encoding/json"
	"strings"
)

// Sanitized returns a defensive report-safe copy. Invalid input JSON remains
// attributable while credential-shaped fields are redacted.
func (result BrowserConversationResult) Sanitized() BrowserConversationResult {
	validity := BrowserConversationTrace(result.BrokerCalls).InputJSONValidity()
	clone := cloneBrowserConversationResult(result)
	clone.ScenarioID = sanitizeBrowserConversationReportText(clone.ScenarioID)
	clone.ScenarioName = sanitizeBrowserConversationReportText(clone.ScenarioName)
	sanitizeBrowserConversationTurns(clone.Turns)
	sanitizeBrowserConversationCalls(clone.BrokerCalls)
	sanitizeBrowserConversationOracles(clone.Oracles)
	sanitizeBrowserConversationCorrectionsForReport(clone.Corrections)
	sanitizeBrowserConversationRecoveriesForReport(clone.Recovery)
	clone.Cancellation = sanitizeBrowserConversationCancellation(clone.Cancellation)
	clone.Lifecycle = sanitizeBrowserConversationLifecycle(clone.Lifecycle)
	clone.Mechanical.Failures = sanitizeBrowserConversationReportTexts(clone.Mechanical.Failures)
	clone.Validator = sanitizeBrowserConversationVerdict(clone.Validator)
	clone.InputJSONValidity = sanitizeBrowserConversationInputJSONValidity(validity)
	return clone
}

func sanitizeBrowserConversationTurns(turns []BrowserConversationTurn) {
	for index := range turns {
		turns[index].StepID = sanitizeBrowserConversationReportText(turns[index].StepID)
		turns[index].ExpectedText = sanitizeBrowserConversationReportText(turns[index].ExpectedText)
		turns[index].ObservedText = sanitizeBrowserConversationReportText(turns[index].ObservedText)
	}
}

func sanitizeBrowserConversationCalls(calls []BrowserConversationBrokerCall) {
	for index := range calls {
		call := &calls[index]
		call.StepID = sanitizeBrowserConversationReportText(call.StepID)
		call.ToolRef = sanitizeBrowserConversationOpaque(call.ToolRef)
		call.ToolName = sanitizeBrowserConversationReportText(call.ToolName)
		call.InvocationID = sanitizeBrowserConversationOpaque(call.InvocationID)
		call.InputJSON = sanitizeBrowserConversationInputJSON(call.InputJSON)
		call.State = sanitizeBrowserConversationOpaque(call.State)
		call.ErrorCode = sanitizeBrowserConversationReportText(call.ErrorCode)
		call.Output = sanitizeBrowserConversationRawJSON(call.Output)
		call.ToolRefs = sanitizeBrowserConversationOpaque(call.ToolRefs)
	}
}

func sanitizeBrowserConversationOracles(oracles []BrowserConversationOracleSnapshot) {
	for index := range oracles {
		oracles[index].StepID = sanitizeBrowserConversationReportText(oracles[index].StepID)
		oracles[index].PageID = sanitizeBrowserConversationReportText(oracles[index].PageID)
		oracles[index].State = sanitizeBrowserConversationRawJSON(oracles[index].State)
	}
}

func sanitizeBrowserConversationCorrectionsForReport(values []BrowserConversationCorrectionEvidence) {
	for index := range values {
		value := &values[index]
		value.StepID = sanitizeBrowserConversationReportText(value.StepID)
		value.TargetStepID = sanitizeBrowserConversationReportText(value.TargetStepID)
		value.TargetUtterance = sanitizeBrowserConversationReportText(value.TargetUtterance)
		value.CorrectionUtterance = sanitizeBrowserConversationReportText(value.CorrectionUtterance)
		value.OriginalToolName = sanitizeBrowserConversationReportText(value.OriginalToolName)
		value.CorrectionToolName = sanitizeBrowserConversationReportText(value.CorrectionToolName)
		value.OriginalAssistantText = sanitizeBrowserConversationReportText(value.OriginalAssistantText)
		value.CorrectionAssistantText = sanitizeBrowserConversationReportText(value.CorrectionAssistantText)
		value.OriginalBefore = sanitizeBrowserConversationRawJSON(value.OriginalBefore)
		value.OriginalAfter = sanitizeBrowserConversationRawJSON(value.OriginalAfter)
		value.CorrectionBefore = sanitizeBrowserConversationRawJSON(value.CorrectionBefore)
		value.CorrectionAfter = sanitizeBrowserConversationRawJSON(value.CorrectionAfter)
		value.OriginalInvocationID = sanitizeBrowserConversationOpaque(value.OriginalInvocationID)
		value.CorrectionInvocationID = sanitizeBrowserConversationOpaque(value.CorrectionInvocationID)
	}
}

func sanitizeBrowserConversationRecoveriesForReport(values []BrowserConversationRecoveryEvidence) {
	for index := range values {
		value := &values[index]
		value.StepID = sanitizeBrowserConversationReportText(value.StepID)
		value.FromPageID = sanitizeBrowserConversationReportText(value.FromPageID)
		value.ToPageID = sanitizeBrowserConversationReportText(value.ToPageID)
		value.StaleToolRef = sanitizeBrowserConversationOpaque(value.StaleToolRef)
		value.StaleInvocationID = sanitizeBrowserConversationOpaque(value.StaleInvocationID)
		value.StaleErrorCode = sanitizeBrowserConversationReportText(value.StaleErrorCode)
		value.RelistedToolRefs = sanitizeBrowserConversationOpaque(value.RelistedToolRefs)
		value.FreshToolRef = sanitizeBrowserConversationOpaque(value.FreshToolRef)
		value.RetryInvocationID = sanitizeBrowserConversationOpaque(value.RetryInvocationID)
	}
}

func sanitizeBrowserConversationCancellation(value BrowserConversationCancellationEvidence) BrowserConversationCancellationEvidence {
	value.Reason = sanitizeBrowserConversationReportText(value.Reason)
	value.InterruptedStepID = sanitizeBrowserConversationReportText(value.InterruptedStepID)
	value.CancelStepID = sanitizeBrowserConversationReportText(value.CancelStepID)
	value.InvocationID = sanitizeBrowserConversationOpaque(value.InvocationID)
	value.FinalState = sanitizeBrowserConversationOpaque(value.FinalState)
	return value
}

func sanitizeBrowserConversationLifecycle(value BrowserConversationLifecycleEvidence) BrowserConversationLifecycleEvidence {
	value.ExternalBrowserID = sanitizeBrowserConversationOpaque(value.ExternalBrowserID)
	value.ExternalTargetID = sanitizeBrowserConversationOpaque(value.ExternalTargetID)
	value.Error = sanitizeBrowserConversationReportText(value.Error)
	return value
}

func sanitizeBrowserConversationVerdict(value BrowserConversationValidatorVerdict) BrowserConversationValidatorVerdict {
	value.Summary = sanitizeBrowserConversationReportText(value.Summary)
	value.Checks = append([]BrowserConversationValidatorCheck(nil), value.Checks...)
	for index := range value.Checks {
		value.Checks[index].Name = sanitizeBrowserConversationReportText(value.Checks[index].Name)
		value.Checks[index].Detail = sanitizeBrowserConversationReportText(value.Checks[index].Detail)
	}
	return value
}

func sanitizeBrowserConversationInputJSONValidity(value BrowserConversationInputJSONValidity) BrowserConversationInputJSONValidity {
	value.Attempts = append([]BrowserConversationInputJSONAttempt(nil), value.Attempts...)
	for index := range value.Attempts {
		attempt := &value.Attempts[index]
		attempt.InputJSON = sanitizeBrowserConversationInputJSON(attempt.InputJSON)
		attempt.StepID = sanitizeBrowserConversationReportText(attempt.StepID)
		attempt.InvocationID = sanitizeBrowserConversationOpaque(attempt.InvocationID)
		attempt.ToolRef = sanitizeBrowserConversationOpaque(attempt.ToolRef)
		attempt.ToolName = sanitizeBrowserConversationReportText(attempt.ToolName)
		attempt.State = sanitizeBrowserConversationOpaque(attempt.State)
	}
	return value
}

func sanitizeBrowserConversationReportMetadata(value BrowserConversationReportMetadata) BrowserConversationReportMetadata {
	value.Command = sanitizeBrowserConversationReportText(value.Command)
	value.Configuration = sanitizeBrowserConversationReportText(value.Configuration)
	value.Provider = sanitizeBrowserConversationReportText(value.Provider)
	value.Model = sanitizeBrowserConversationReportText(value.Model)
	value.BrowserChannel = sanitizeBrowserConversationReportText(value.BrowserChannel)
	value.BrowserVersion = sanitizeBrowserConversationReportText(value.BrowserVersion)
	value.BrowserRevision = sanitizeBrowserConversationReportText(value.BrowserRevision)
	value.PR269Status = sanitizeBrowserConversationReportText(value.PR269Status)
	value.LaneIBranch = sanitizeBrowserConversationReportText(value.LaneIBranch)
	value.LaneIPullRequest = sanitizeBrowserConversationReportText(value.LaneIPullRequest)
	value.DependencyBaseline = sanitizeBrowserConversationReportTexts(value.DependencyBaseline)
	return value
}

func sanitizeBrowserConversationReportTexts(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = sanitizeBrowserConversationReportText(value)
	}
	return result
}

func sanitizeBrowserConversationInputJSON(value string) string {
	if browserConversationContainsCredentialMarker(value) {
		return browserConversationRedactedText
	}
	return value
}

func sanitizeBrowserConversationRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	if browserConversationContainsCredentialMarker(string(value)) {
		return json.RawMessage(`"` + browserConversationRedactedText + `"`)
	}
	if json.Valid(value) {
		return append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(string(value))
	if err != nil {
		return json.RawMessage(`"[invalid_json]"`)
	}
	return encoded
}

func sanitizeBrowserConversationReportText(value string) string {
	if browserConversationContainsCredentialMarker(value) {
		return browserConversationRedactedText
	}
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '\n', '\r', '\t':
			builder.WriteByte(' ')
		default:
			if char < 0x20 || char == 0x7f {
				builder.WriteByte(' ')
			} else {
				builder.WriteRune(char)
			}
		}
	}
	return builder.String()
}

func browserConversationContainsCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "api_key", "api-key", "access_token", "refresh_token", "client_secret", "password", "-----begin ", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
