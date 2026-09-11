package browserscenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RecordRecovery records the derived stale-reference recovery evidence once.
// The underlying ordered broker calls remain the source of truth.
func (r *BrowserConversationRun) RecordRecovery(evidence []BrowserConversationRecoveryEvidence) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasRecovery {
		return ErrBrowserConversationDuplicateObservation
	}
	if err := validateBrowserConversationRecoveriesForObservation(evidence); err != nil {
		return err
	}
	r.hasRecovery = true
	r.result.Recovery = cloneBrowserConversationRecoveries(evidence)
	return nil
}

func validateBrowserConversationRecoveriesForObservation(recoveries []BrowserConversationRecoveryEvidence) error {
	for index, recovery := range recoveries {
		path := fmt.Sprintf("recovery[%d]", index)
		if err := validateBrowserConversationRecoveryIdentity(path, recovery); err != nil {
			return browserConversationObservationError(path, "requires step_id, from_page_id, and to_page_id")
		}
		if recovery.StaleRejected && recovery.StaleErrorCode != browserConversationStaleToolRef {
			return browserConversationObservationError(path+".stale_error_code", "must be %q when stale_rejected is true", browserConversationStaleToolRef)
		}
		if recovery.ToolsRelisted && browserConversationOpaqueLen(recovery.RelistedToolRefs) == 0 {
			return browserConversationObservationError(path+".relisted_tool_refs", "must include the fresh catalog references when tools_relisted is true")
		}
		if recovery.FreshInvocationCompleted && browserConversationOpaqueString(recovery.FreshToolRef) == "" {
			return browserConversationObservationError(path+".fresh_tool_ref", "is required when fresh invocation completed")
		}
	}
	return nil
}

// RecordCorrections records derived correction evidence once. The underlying
// turns, broker calls, and oracle snapshots remain the source of truth.
func (r *BrowserConversationRun) RecordCorrections(evidence []BrowserConversationCorrectionEvidence) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasCorrections {
		return ErrBrowserConversationDuplicateObservation
	}
	if err := validateBrowserConversationCorrectionsForObservation(evidence); err != nil {
		return err
	}
	r.hasCorrections = true
	r.result.Corrections = cloneBrowserConversationCorrections(evidence)
	return nil
}

func validateBrowserConversationCorrectionsForObservation(corrections []BrowserConversationCorrectionEvidence) error {
	for _, correction := range corrections {
		if strings.TrimSpace(correction.StepID) == "" || strings.TrimSpace(correction.TargetStepID) == "" {
			return browserConversationObservationError("correction", "requires step_id and target_step_id")
		}
		if strings.TrimSpace(correction.TargetUtterance) == "" || strings.TrimSpace(correction.CorrectionUtterance) == "" {
			return browserConversationObservationError("correction", "requires target and correction utterances")
		}
		if err := validateBrowserConversationCorrectionStates("correction", correction); err != nil {
			return err
		}
	}
	return nil
}

// ObserveOracleSnapshot appends an independent page-state reading.
func (r *BrowserConversationRun) ObserveOracleSnapshot(snapshot BrowserConversationOracleSnapshot) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if snapshot.PageID == "" {
		return browserConversationObservationError("oracle.page_id", "is required")
	}
	if !browserConversationOraclePhaseValid(snapshot.Phase) {
		return browserConversationObservationError("oracle.phase", "is unsupported")
	}
	if snapshot.Phase != BrowserConversationOraclePostSession {
		if snapshot.StepID == "" {
			return browserConversationObservationError("oracle.step_id", "is required for a step snapshot")
		}
		if _, ok := r.steps[snapshot.StepID]; !ok {
			return browserConversationObservationError("oracle.step_id", "references unknown step %q", snapshot.StepID)
		}
	}
	if err := validateJSONObject("oracle.state", snapshot.State); err != nil {
		return err
	}
	snapshot.Sequence = r.takeSequenceLocked()
	snapshot.State = append(json.RawMessage(nil), snapshot.State...)
	r.result.Oracles = append(r.result.Oracles, cloneBrowserConversationOracleSnapshot(snapshot))
	return nil
}

// RecordCancellation joins interruption, explicit-cancel, terminal, and late
// event facts into one run-scoped record. Each fact is monotonic: later
// observations may fill fields but can never turn a canceled invocation into
// a completed one or replace its identity.
func (r *BrowserConversationRun) RecordCancellation(evidence BrowserConversationCancellationEvidence) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if err := validateBrowserConversationCancellationObservation(r.result.Cancellation, evidence); err != nil {
		return err
	}
	if r.hasCancellation {
		r.result.Cancellation = mergeBrowserConversationCancellation(r.result.Cancellation, evidence)
		return nil
	}
	r.hasCancellation = true
	r.result.Cancellation = evidence
	return nil
}

func validateBrowserConversationCancellationObservation(current, evidence BrowserConversationCancellationEvidence) error {
	state := browserConversationOpaqueString(evidence.FinalState)
	if state == browserConversationInvocationCompleted && (evidence.Interrupted || evidence.Requested || current.Interrupted || current.Requested) {
		return browserConversationObservationError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
	}
	if state != "" && !browserConversationInvocationStateTerminal(evidence.FinalState) {
		return browserConversationObservationError("cancellation.final_state", "must be a terminal invocation state")
	}
	if evidence.LateEventsSuppressed < 0 {
		return browserConversationObservationError("cancellation.late_events_suppressed", "must not be negative")
	}
	if browserConversationOpaqueString(current.InvocationID) != "" && browserConversationOpaqueString(evidence.InvocationID) != "" && !opaqueEqual(current.InvocationID, evidence.InvocationID) {
		return browserConversationObservationError("cancellation.invocation_id", "cannot change after the invocation is identified")
	}
	if browserConversationOpaqueString(current.FinalState) != "" && state != "" && !opaqueEqual(current.FinalState, evidence.FinalState) {
		return browserConversationObservationError("cancellation.final_state", "cannot change after a terminal disposition is recorded")
	}
	return nil
}

func mergeBrowserConversationCancellation(current, evidence BrowserConversationCancellationEvidence) BrowserConversationCancellationEvidence {
	if current.InvocationID == nil {
		current.InvocationID = evidence.InvocationID
	}
	if current.FinalState == nil {
		current.FinalState = evidence.FinalState
	}
	current.Interrupted = current.Interrupted || evidence.Interrupted
	current.Requested = current.Requested || evidence.Requested
	current.Reason = firstBrowserConversationText(current.Reason, evidence.Reason)
	current.InterruptedStepID = firstBrowserConversationText(current.InterruptedStepID, evidence.InterruptedStepID)
	current.CancelStepID = firstBrowserConversationText(current.CancelStepID, evidence.CancelStepID)
	current.OverlappingAudioSent = current.OverlappingAudioSent || evidence.OverlappingAudioSent
	current.ExplicitCancelAudioSent = current.ExplicitCancelAudioSent || evidence.ExplicitCancelAudioSent
	current.LateEventsSuppressed += evidence.LateEventsSuppressed
	return current
}

func firstBrowserConversationText(current, fallback string) string {
	if current != "" {
		return current
	}
	return fallback
}

// RecordLifecycle records process/session ownership facts once.
func (r *BrowserConversationRun) RecordLifecycle(evidence BrowserConversationLifecycleEvidence) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasLifecycle {
		return ErrBrowserConversationDuplicateObservation
	}
	if evidence.DetachCount < 0 {
		return browserConversationObservationError("lifecycle.detach_count", "must not be negative")
	}
	r.hasLifecycle = true
	r.result.Lifecycle = evidence
	return nil
}

// RecordMechanicalEvaluation records the authoritative fact checks once.
func (r *BrowserConversationRun) RecordMechanicalEvaluation(evaluation BrowserConversationMechanicalEvaluation) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasMechanical {
		return ErrBrowserConversationDuplicateObservation
	}
	r.hasMechanical = true
	r.result.Mechanical = cloneBrowserConversationMechanicalEvaluation(evaluation)
	return nil
}

// RecordValidator records structured validator output once.
func (r *BrowserConversationRun) RecordValidator(verdict BrowserConversationValidatorVerdict) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasValidator {
		return ErrBrowserConversationDuplicateObservation
	}
	if verdict.Version == "" {
		verdict.Version = BrowserConversationValidatorVersion
	}
	if verdict.Version != BrowserConversationValidatorVersion {
		return browserConversationObservationError("validator.version", "must be %q", BrowserConversationValidatorVersion)
	}
	if verdict.Status == "" {
		return browserConversationObservationError("validator.status", "is required")
	}
	if !browserConversationValidatorStatusValid(verdict.Status) {
		return browserConversationObservationError("validator.status", "is unsupported")
	}
	r.hasValidator = true
	r.result.Validator = cloneBrowserConversationValidatorVerdict(verdict)
	return nil
}
