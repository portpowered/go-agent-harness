package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation/internal/service/policy"
)

// RecordRecovery records the derived stale-reference recovery evidence once.
// The underlying ordered broker calls remain the source of truth.
func (r *browserConversationRun) RecordRecovery(evidence []BrowserConversationRecoveryEvidence) error {
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
	r.result.Recovery = policy.CloneRecoveries(evidence)
	return nil
}

func validateBrowserConversationRecoveriesForObservation(recoveries []BrowserConversationRecoveryEvidence) error {
	for index, recovery := range recoveries {
		path := fmt.Sprintf("recovery[%d]", index)
		if err := policy.ValidateRecoveryIdentity(path, recovery); err != nil {
			return policy.ObservationError(path, "requires step_id, from_page_id, and to_page_id")
		}
		if recovery.StaleRejected && recovery.StaleErrorCode != policy.StaleToolRef {
			return policy.ObservationError(path+".stale_error_code", "must be %q when stale_rejected is true", policy.StaleToolRef)
		}
		if recovery.ToolsRelisted && policy.OpaqueLen(recovery.RelistedToolRefs) == 0 {
			return policy.ObservationError(path+".relisted_tool_refs", "must include the fresh catalog references when tools_relisted is true")
		}
		if recovery.FreshInvocationCompleted && policy.OpaqueString(recovery.FreshToolRef) == "" {
			return policy.ObservationError(path+".fresh_tool_ref", "is required when fresh invocation completed")
		}
	}
	return nil
}

// RecordCorrections records derived correction evidence once. The underlying
// turns, broker calls, and oracle snapshots remain the source of truth.
func (r *browserConversationRun) RecordCorrections(evidence []BrowserConversationCorrectionEvidence) error {
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
	r.result.Corrections = policy.CloneCorrections(evidence)
	return nil
}

func validateBrowserConversationCorrectionsForObservation(corrections []BrowserConversationCorrectionEvidence) error {
	for _, correction := range corrections {
		if strings.TrimSpace(correction.StepID) == "" || strings.TrimSpace(correction.TargetStepID) == "" {
			return policy.ObservationError("correction", "requires step_id and target_step_id")
		}
		if strings.TrimSpace(correction.TargetUtterance) == "" || strings.TrimSpace(correction.CorrectionUtterance) == "" {
			return policy.ObservationError("correction", "requires target and correction utterances")
		}
		if err := policy.ValidateCorrectionStates("correction", correction); err != nil {
			return err
		}
	}
	return nil
}

// ObserveOracleSnapshot appends an independent page-state reading.
func (r *browserConversationRun) ObserveOracleSnapshot(snapshot BrowserConversationOracleSnapshot) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if snapshot.PageID == "" {
		return policy.ObservationError("oracle.page_id", "is required")
	}
	if !policy.OraclePhaseValid(snapshot.Phase) {
		return policy.ObservationError("oracle.phase", "is unsupported")
	}
	if snapshot.Phase != BrowserConversationOraclePostSession {
		if snapshot.StepID == "" {
			return policy.ObservationError("oracle.step_id", "is required for a step snapshot")
		}
		if _, ok := r.steps[snapshot.StepID]; !ok {
			return policy.ObservationError("oracle.step_id", "references unknown step %q", snapshot.StepID)
		}
	}
	if err := policy.ValidateJSONObject("oracle.state", snapshot.State); err != nil {
		return err
	}
	snapshot.Sequence = r.takeSequenceLocked()
	snapshot.State = append(json.RawMessage(nil), snapshot.State...)
	r.result.Oracles = append(r.result.Oracles, policy.CloneOracleSnapshot(snapshot))
	return nil
}

// RecordCancellation joins interruption, explicit-cancel, terminal, and late
// event facts into one run-scoped record. Each fact is monotonic: later
// observations may fill fields but can never turn a canceled invocation into
// a completed one or replace its identity.
func (r *browserConversationRun) RecordCancellation(evidence BrowserConversationCancellationEvidence) error {
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
		r.appendInvocationObservationLocked("record_cancellation", evidence)
		return nil
	}
	r.hasCancellation = true
	r.result.Cancellation = evidence
	r.appendInvocationObservationLocked("record_cancellation", evidence)
	return nil
}

// ObserveInvocationPublication appends an ordered cancellation-related
// invocation observation. It is intentionally separate from broker calls so
// a late terminal event remains accounted for even when it is suppressed from
// public turns.
func (r *browserConversationRun) ObserveInvocationPublication(source string, invocationID, state any, terminal bool) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	r.appendInvocationObservationLocked(source, BrowserConversationCancellationEvidence{InvocationID: invocationID, FinalState: state})
	if len(r.result.InvocationObservations) > 0 {
		r.result.InvocationObservations[len(r.result.InvocationObservations)-1].Terminal = terminal
	}
	return nil
}

func (r *browserConversationRun) appendInvocationObservationLocked(source string, evidence BrowserConversationCancellationEvidence) {
	if policy.OpaqueString(evidence.InvocationID) == "" {
		return
	}
	r.result.InvocationObservations = append(r.result.InvocationObservations, BrowserConversationInvocationObservation{
		Sequence: r.takeSequenceLocked(), Source: source, InvocationID: policy.CloneOpaque(evidence.InvocationID),
		State: policy.CloneOpaque(evidence.FinalState), Terminal: policy.InvocationStateTerminal(evidence.FinalState),
	})
}

func validateBrowserConversationCancellationObservation(current, evidence BrowserConversationCancellationEvidence) error {
	state := policy.OpaqueString(evidence.FinalState)
	if state == policy.InvocationCompleted && (evidence.Interrupted || evidence.Requested || current.Interrupted || current.Requested) {
		return policy.ObservationError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
	}
	if state != "" && !policy.InvocationStateTerminal(evidence.FinalState) {
		return policy.ObservationError("cancellation.final_state", "must be a terminal invocation state")
	}
	if evidence.LateEventsSuppressed < 0 {
		return policy.ObservationError("cancellation.late_events_suppressed", "must not be negative")
	}
	if policy.OpaqueString(current.InvocationID) != "" && policy.OpaqueString(evidence.InvocationID) != "" && !policy.OpaqueEqual(current.InvocationID, evidence.InvocationID) {
		return policy.ObservationError("cancellation.invocation_id", "cannot change after the invocation is identified")
	}
	if policy.OpaqueString(current.FinalState) != "" && state != "" && !policy.OpaqueEqual(current.FinalState, evidence.FinalState) {
		return policy.ObservationError("cancellation.final_state", "cannot change after a terminal disposition is recorded")
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
func (r *browserConversationRun) RecordLifecycle(evidence BrowserConversationLifecycleEvidence) error {
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
		return policy.ObservationError("lifecycle.detach_count", "must not be negative")
	}
	r.hasLifecycle = true
	r.result.Lifecycle = evidence
	return nil
}

// RecordMechanicalEvaluation records the authoritative fact checks once.
func (r *browserConversationRun) RecordMechanicalEvaluation(evaluation BrowserConversationMechanicalEvaluation) error {
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
	r.result.Mechanical = policy.CloneMechanicalEvaluation(evaluation)
	return nil
}

// RecordValidator records structured validator output once.
func (r *browserConversationRun) RecordValidator(verdict BrowserConversationValidatorVerdict) error {
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
		return policy.ObservationError("validator.version", "must be %q", BrowserConversationValidatorVersion)
	}
	if verdict.Status == "" {
		return policy.ObservationError("validator.status", "is required")
	}
	if !policy.ValidatorStatusValid(verdict.Status) {
		return policy.ObservationError("validator.status", "is unsupported")
	}
	r.hasValidator = true
	r.result.Validator = policy.CloneValidatorVerdict(verdict)
	return nil
}
