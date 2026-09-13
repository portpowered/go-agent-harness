package browserscenario

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Validate checks the joined evidence shape without rejecting invalid
// InputJSON. Invalid invocation input is an observation that must remain
// serializable for the validity measurement.
func (r BrowserConversationResult) Validate() error {
	if err := validateBrowserConversationResultIdentity(r); err != nil {
		return err
	}
	if err := validateBrowserConversationTurns(r.Turns); err != nil {
		return err
	}
	if err := validateBrowserConversationCalls(r.BrokerCalls); err != nil {
		return err
	}
	if err := validateBrowserConversationCorrections(r.Corrections); err != nil {
		return err
	}
	if err := validateBrowserConversationRecoveries(r.Recovery); err != nil {
		return err
	}
	if err := validateBrowserConversationOracles(r.Oracles); err != nil {
		return err
	}
	return validateBrowserConversationResultDispositions(r)
}

func validateBrowserConversationResultIdentity(result BrowserConversationResult) error {
	if strings.TrimSpace(result.ScenarioID) == "" {
		return browserConversationResultError("scenario_id", "is required")
	}
	if strings.TrimSpace(result.ScenarioName) == "" {
		return browserConversationResultError("scenario_name", "is required")
	}
	return nil
}

func validateBrowserConversationTurns(turns []BrowserConversationTurn) error {
	for index, turn := range turns {
		path := fmt.Sprintf("turns[%d]", index)
		if turn.Sequence == 0 || turn.StepID == "" || strings.TrimSpace(turn.ObservedText) == "" {
			return browserConversationResultError(path, "requires sequence, step_id, and observed_text")
		}
		if turn.Direction != BrowserConversationCustomerTurn && turn.Direction != BrowserConversationAssistantTurn {
			return browserConversationResultError(path+".direction", "is unsupported")
		}
	}
	return nil
}

func validateBrowserConversationCalls(calls []BrowserConversationBrokerCall) error {
	for index, call := range calls {
		path := fmt.Sprintf("broker_calls[%d]", index)
		if call.Sequence == 0 || !browserConversationBrokerOperationValid(call.Operation) {
			return browserConversationResultError(path, "requires a sequence and supported operation")
		}
		if call.Terminal && !browserConversationInvocationStateTerminal(call.State) {
			return browserConversationResultError(path+".state", "terminal calls require a terminal invocation state")
		}
	}
	return nil
}

func validateBrowserConversationCorrections(corrections []BrowserConversationCorrectionEvidence) error {
	for index, correction := range corrections {
		path := fmt.Sprintf("corrections[%d]", index)
		if strings.TrimSpace(correction.StepID) == "" || strings.TrimSpace(correction.TargetStepID) == "" {
			return browserConversationResultError(path, "requires step_id and target_step_id")
		}
		if strings.TrimSpace(correction.TargetUtterance) == "" || strings.TrimSpace(correction.CorrectionUtterance) == "" {
			return browserConversationResultError(path, "requires target and correction utterances")
		}
		if err := validateBrowserConversationCorrectionStates(path, correction); err != nil {
			return err
		}
	}
	return nil
}

func validateBrowserConversationCorrectionStates(path string, correction BrowserConversationCorrectionEvidence) error {
	states := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "original_before", raw: correction.OriginalBefore},
		{name: "original_after", raw: correction.OriginalAfter},
		{name: "correction_before", raw: correction.CorrectionBefore},
		{name: "correction_after", raw: correction.CorrectionAfter},
	}
	for _, state := range states {
		if len(state.raw) == 0 {
			continue
		}
		if err := validateJSONObject(path+"."+state.name, state.raw); err != nil {
			return err
		}
	}
	return nil
}

func validateBrowserConversationRecoveries(recoveries []BrowserConversationRecoveryEvidence) error {
	for index, recovery := range recoveries {
		path := fmt.Sprintf("recovery[%d]", index)
		if err := validateBrowserConversationRecoveryIdentity(path, recovery); err != nil {
			return err
		}
		if recovery.StaleRejected && recovery.StaleErrorCode != browserConversationStaleToolRef {
			return browserConversationResultError(path+".stale_error_code", "must be %q when stale_rejected is true", browserConversationStaleToolRef)
		}
		if recovery.ToolsRelisted && browserConversationOpaqueLen(recovery.RelistedToolRefs) == 0 {
			return browserConversationResultError(path+".relisted_tool_refs", "must include the fresh catalog references when tools_relisted is true")
		}
		if recovery.FreshInvocationCompleted && browserConversationOpaqueString(recovery.FreshToolRef) == "" {
			return browserConversationResultError(path+".fresh_tool_ref", "is required when fresh invocation completed")
		}
	}
	return nil
}

func validateBrowserConversationRecoveryIdentity(path string, recovery BrowserConversationRecoveryEvidence) error {
	if strings.TrimSpace(recovery.StepID) == "" || strings.TrimSpace(recovery.FromPageID) == "" || strings.TrimSpace(recovery.ToPageID) == "" {
		return browserConversationResultError(path, "requires step_id, from_page_id, and to_page_id")
	}
	return nil
}

func validateBrowserConversationOracles(oracles []BrowserConversationOracleSnapshot) error {
	for index, snapshot := range oracles {
		path := fmt.Sprintf("oracle_snapshots[%d]", index)
		if snapshot.Sequence == 0 || snapshot.PageID == "" {
			return browserConversationResultError(path, "requires sequence and page_id")
		}
		if !browserConversationOraclePhaseValid(snapshot.Phase) {
			return browserConversationResultError(path+".phase", "is unsupported")
		}
		if err := validateJSONObject(path+".state", snapshot.State); err != nil {
			return err
		}
	}
	return nil
}

func browserConversationOraclePhaseValid(phase BrowserConversationOraclePhase) bool {
	switch phase {
	case BrowserConversationOracleBefore, BrowserConversationOracleAfter, BrowserConversationOraclePostSession:
		return true
	default:
		return false
	}
}

func validateBrowserConversationResultDispositions(result BrowserConversationResult) error {
	if result.Lifecycle.DetachCount < 0 {
		return browserConversationResultError("lifecycle.detach_count", "must not be negative")
	}
	if result.Cancellation.LateEventsSuppressed < 0 {
		return browserConversationResultError("cancellation.late_events_suppressed", "must not be negative")
	}
	if err := validateBrowserConversationCancellation(result.Cancellation); err != nil {
		return err
	}
	if result.Validator.Version != "" && result.Validator.Version != BrowserConversationValidatorVersion {
		return browserConversationResultError("validator.version", "must be %q", BrowserConversationValidatorVersion)
	}
	if result.Validator.Status != "" && !browserConversationValidatorStatusValid(result.Validator.Status) {
		return browserConversationResultError("validator.status", "is unsupported")
	}
	return nil
}

func validateBrowserConversationCancellation(cancellation BrowserConversationCancellationEvidence) error {
	state := browserConversationOpaqueString(cancellation.FinalState)
	if state != "" && !browserConversationInvocationStateTerminal(cancellation.FinalState) {
		return browserConversationResultError("cancellation.final_state", "must be a terminal invocation state")
	}
	if state == browserConversationInvocationCompleted && (cancellation.Interrupted || cancellation.Requested) {
		return browserConversationResultError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
	}
	return nil
}

func browserConversationValidatorStatusValid(status BrowserConversationValidatorStatus) bool {
	return status == BrowserConversationValidatorPass || status == BrowserConversationValidatorFail || status == BrowserConversationValidatorNotRun
}

func browserConversationBrokerOperationValid(operation BrowserConversationBrokerOperation) bool {
	switch operation {
	case BrowserConversationListTools, BrowserConversationInvoke, BrowserConversationCancel, BrowserConversationSelectPage, BrowserConversationWaitReady, BrowserConversationCustomerNavigate:
		return true
	default:
		return false
	}
}

func browserConversationInvocationStateTerminal(state any) bool {
	switch browserConversationOpaqueString(state) {
	case browserConversationInvocationCompleted, browserConversationInvocationError, browserConversationInvocationCanceled, browserConversationInvocationTimedOut, browserConversationInvocationOrphaned, browserConversationInvocationPolicyDenied:
		return true
	default:
		return false
	}
}

type browserConversationResultErr struct {
	Path   string
	Reason string
}

func (e *browserConversationResultErr) Error() string {
	if e == nil {
		return ErrInvalidBrowserConversationResult.Error()
	}
	return fmt.Sprintf("%s at %s: %s", ErrInvalidBrowserConversationResult, e.Path, e.Reason)
}

func (e *browserConversationResultErr) Unwrap() error {
	return ErrInvalidBrowserConversationResult
}

func browserConversationResultError(path, format string, args ...any) error {
	return &browserConversationResultErr{Path: path, Reason: fmt.Sprintf(format, args...)}
}

type browserConversationObservationErr struct {
	Path   string
	Reason string
}

func (e *browserConversationObservationErr) Error() string {
	if e == nil {
		return "invalid browser conversation observation"
	}
	return fmt.Sprintf("invalid browser conversation observation at %s: %s", e.Path, e.Reason)
}

func browserConversationObservationErrorf(path, format string, args ...any) error {
	return &browserConversationObservationErr{Path: path, Reason: fmt.Sprintf(format, args...)}
}

func browserConversationObservationError(path, format string, args ...any) error {
	return browserConversationObservationErrorf(path, format, args...)
}
