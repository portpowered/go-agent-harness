package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// BrowserConversationTurnDirection identifies which side of the shared
// conversation produced an observed turn.
type BrowserConversationTurnDirection string

const (
	BrowserConversationCustomerTurn  BrowserConversationTurnDirection = "customer"
	BrowserConversationAssistantTurn BrowserConversationTurnDirection = "assistant"
)

// BrowserConversationBrokerOperation identifies the public browser operation
// observed alongside a conversational turn.
type BrowserConversationBrokerOperation string

const (
	BrowserConversationListTools        BrowserConversationBrokerOperation = "webmcp_list_tools"
	BrowserConversationInvoke           BrowserConversationBrokerOperation = "webmcp_invoke"
	BrowserConversationCancel           BrowserConversationBrokerOperation = "webmcp_cancel"
	BrowserConversationSelectPage       BrowserConversationBrokerOperation = "webmcp_select_page"
	BrowserConversationWaitReady        BrowserConversationBrokerOperation = "webmcp_wait_ready"
	BrowserConversationCustomerNavigate BrowserConversationBrokerOperation = "customer_navigation"
	// BrowserConversationNavigate is a concise alias for customer-owned
	// navigation observations.
	BrowserConversationNavigate = BrowserConversationCustomerNavigate
)

// BrowserConversationOraclePhase identifies where an independent page-state
// snapshot belongs in the evidence timeline.
type BrowserConversationOraclePhase string

const (
	BrowserConversationOracleBefore      BrowserConversationOraclePhase = "before"
	BrowserConversationOracleAfter       BrowserConversationOraclePhase = "after"
	BrowserConversationOraclePostSession BrowserConversationOraclePhase = "post_session"
)

// BrowserConversationLifecycleOutcome describes the session/process side of a
// run without conflating it with browser page state.
type BrowserConversationLifecycleOutcome string

const (
	BrowserConversationLifecycleNotStarted BrowserConversationLifecycleOutcome = "not_started"
	BrowserConversationLifecycleRunning    BrowserConversationLifecycleOutcome = "running"
	BrowserConversationLifecycleCompleted  BrowserConversationLifecycleOutcome = "completed"
	BrowserConversationLifecycleFailed     BrowserConversationLifecycleOutcome = "failed"
	BrowserConversationLifecycleCanceled   BrowserConversationLifecycleOutcome = "canceled"
	BrowserConversationLifecycleTimedOut   BrowserConversationLifecycleOutcome = "timed_out"
)

// BrowserConversationValidatorStatus is the structured validator-agent
// disposition. Mechanical evidence remains authoritative over this opinion.
type BrowserConversationValidatorStatus string

const (
	BrowserConversationValidatorPass   BrowserConversationValidatorStatus = "pass"
	BrowserConversationValidatorFail   BrowserConversationValidatorStatus = "fail"
	BrowserConversationValidatorNotRun BrowserConversationValidatorStatus = "not_run"
)

// BrowserConversationTurn is an observed customer or assistant turn. The
// expected customer utterance is retained beside the observed transcript so
// ASR or orchestration mismatches remain attributable.
type BrowserConversationTurn struct {
	Sequence     uint64                           `json:"sequence"`
	StepID       string                           `json:"step_id"`
	Direction    BrowserConversationTurnDirection `json:"direction"`
	ExpectedText string                           `json:"expected_text,omitempty"`
	ObservedText string                           `json:"observed_text"`
	Complete     bool                             `json:"complete"`
}

// BrowserConversationBrokerCall is one ordered browser operation observation.
// InputJSON is deliberately a string: invalid model output must be preserved
// verbatim for later validity measurement rather than repaired or omitted.
type BrowserConversationBrokerCall struct {
	Sequence           uint64                             `json:"sequence"`
	StepID             string                             `json:"step_id,omitempty"`
	Operation          BrowserConversationBrokerOperation `json:"operation"`
	ToolRef            webmcp.ToolRef                     `json:"tool_ref,omitempty"`
	ToolName           string                             `json:"tool_name,omitempty"`
	InvocationID       webmcp.InvocationID                `json:"invocation_id,omitempty"`
	InputJSON          string                             `json:"input_json"`
	State              webmcp.InvocationState             `json:"state,omitempty"`
	Terminal           bool                               `json:"terminal"`
	Output             json.RawMessage                    `json:"output,omitempty"`
	ErrorCode          string                             `json:"error_code,omitempty"`
	Generation         uint64                             `json:"generation,omitempty"`
	PreviousGeneration uint64                             `json:"previous_generation,omitempty"`
	ToolRefs           []webmcp.ToolRef                   `json:"tool_refs,omitempty"`
}

// BrowserConversationRecoveryEvidence records the ordered facts needed to
// prove customer-navigation recovery. A stale reference is retained exactly
// as attempted; it is never replaced with the fresh reference in-place.
type BrowserConversationRecoveryEvidence struct {
	StepID                   string              `json:"step_id"`
	FromPageID               string              `json:"from_page_id"`
	ToPageID                 string              `json:"to_page_id"`
	NavigationObserved       bool                `json:"navigation_observed"`
	PreviousGeneration       uint64              `json:"previous_generation,omitempty"`
	CurrentGeneration        uint64              `json:"current_generation,omitempty"`
	StaleToolRef             webmcp.ToolRef      `json:"stale_tool_ref,omitempty"`
	StaleInvocationID        webmcp.InvocationID `json:"stale_invocation_id,omitempty"`
	StaleGeneration          uint64              `json:"stale_generation,omitempty"`
	StaleErrorCode           string              `json:"stale_error_code,omitempty"`
	StaleRejected            bool                `json:"stale_rejected"`
	ToolsRelisted            bool                `json:"tools_relisted"`
	RelistedToolRefs         []webmcp.ToolRef    `json:"relisted_tool_refs,omitempty"`
	RelistedGeneration       uint64              `json:"relisted_generation,omitempty"`
	FreshToolRef             webmcp.ToolRef      `json:"fresh_tool_ref,omitempty"`
	FreshGeneration          uint64              `json:"fresh_generation,omitempty"`
	RetryInvocationID        webmcp.InvocationID `json:"retry_invocation_id,omitempty"`
	FreshInvocationCompleted bool                `json:"fresh_invocation_completed"`
	Passed                   bool                `json:"passed"`
}

// BrowserConversationCorrectionEvidence preserves both the original and
// correcting customer intents alongside their independently observed state
// transitions. The invocation and assistant fields are evidence, not claims
// inferred from either transcript.
type BrowserConversationCorrectionEvidence struct {
	StepID                        string              `json:"step_id"`
	TargetStepID                  string              `json:"target_step_id"`
	TargetUtterance               string              `json:"target_utterance"`
	CorrectionUtterance           string              `json:"correction_utterance"`
	OriginalBefore                json.RawMessage     `json:"original_before,omitempty"`
	OriginalAfter                 json.RawMessage     `json:"original_after,omitempty"`
	CorrectionBefore              json.RawMessage     `json:"correction_before,omitempty"`
	CorrectionAfter               json.RawMessage     `json:"correction_after,omitempty"`
	OriginalInvocationID          webmcp.InvocationID `json:"original_invocation_id,omitempty"`
	CorrectionInvocationID        webmcp.InvocationID `json:"correction_invocation_id,omitempty"`
	OriginalToolName              string              `json:"original_tool_name,omitempty"`
	CorrectionToolName            string              `json:"correction_tool_name,omitempty"`
	OriginalInvocationCompleted   bool                `json:"original_invocation_completed"`
	CorrectionInvocationCompleted bool                `json:"correction_invocation_completed"`
	OriginalAssistantText         string              `json:"original_assistant_text,omitempty"`
	CorrectionAssistantText       string              `json:"correction_assistant_text,omitempty"`
	Passed                        bool                `json:"passed"`
}

// BrowserConversationOracleSnapshot is an independent fixture-state reading.
// It is not derived from assistant speech or a broker result envelope.
type BrowserConversationOracleSnapshot struct {
	Sequence   uint64                         `json:"sequence"`
	StepID     string                         `json:"step_id,omitempty"`
	PageID     string                         `json:"page_id"`
	Generation uint64                         `json:"generation,omitempty"`
	Phase      BrowserConversationOraclePhase `json:"phase"`
	State      json.RawMessage                `json:"state"`
}

// BrowserConversationCancellationEvidence records interruption and explicit
// cancellation without pretending that a canceled invocation completed.
type BrowserConversationCancellationEvidence struct {
	Interrupted             bool                   `json:"interrupted"`
	Requested               bool                   `json:"requested"`
	InvocationID            webmcp.InvocationID    `json:"invocation_id,omitempty"`
	FinalState              webmcp.InvocationState `json:"final_state,omitempty"`
	Reason                  string                 `json:"reason,omitempty"`
	InterruptedStepID       string                 `json:"interrupted_step_id,omitempty"`
	CancelStepID            string                 `json:"cancel_step_id,omitempty"`
	OverlappingAudioSent    bool                   `json:"overlapping_audio_sent"`
	ExplicitCancelAudioSent bool                   `json:"explicit_cancel_audio_sent"`
	LateEventsSuppressed    int                    `json:"late_events_suppressed,omitempty"`
}

// BrowserConversationLifecycleEvidence records process/session cleanup and
// preserves the external-tab ownership boundary. BrowserClosed and
// TargetClosed should remain false for an externally owned fixture.
type BrowserConversationLifecycleEvidence struct {
	Outcome                   BrowserConversationLifecycleOutcome `json:"outcome"`
	SessionStarted            bool                                `json:"session_started"`
	SessionTerminated         bool                                `json:"session_terminated"`
	Detached                  bool                                `json:"detached"`
	DetachCount               int                                 `json:"detach_count"`
	DetachRequired            bool                                `json:"detach_required"`
	BrowserClosed             bool                                `json:"browser_closed"`
	TargetClosed              bool                                `json:"target_closed"`
	ExternalBrowserID         webmcp.BrowserID                    `json:"external_browser_id,omitempty"`
	ExternalTargetID          webmcp.TargetID                     `json:"external_target_id,omitempty"`
	ExternalTabAlive          bool                                `json:"external_tab_alive"`
	ExternalTabResponsive     bool                                `json:"external_tab_responsive"`
	ExternalTabAllowsMutation bool                                `json:"external_tab_allows_mutation"`
	ExternalTabRead           bool                                `json:"external_tab_read"`
	ExternalTabMutation       bool                                `json:"external_tab_mutation"`
	Error                     string                              `json:"error,omitempty"`
}

// BrowserConversationValidatorCheck is one rubric item returned by the
// validator agent.
type BrowserConversationValidatorCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// BrowserConversationValidatorVerdict is structured validator output. A pass
// here never overrides a failed mechanical check.
type BrowserConversationValidatorVerdict struct {
	Version string                              `json:"version"`
	Status  BrowserConversationValidatorStatus  `json:"status"`
	Passed  bool                                `json:"passed"`
	Summary string                              `json:"summary,omitempty"`
	Checks  []BrowserConversationValidatorCheck `json:"checks,omitempty"`
}

// BrowserConversationMechanicalEvaluation contains facts computed from the
// observed evidence, separate from semantic validator prose.
type BrowserConversationMechanicalEvaluation struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// BrowserConversationResult is the joined, attributable output of one typed
// browser conversation. It has separate collections for turns, broker calls,
// independent oracle snapshots, cancellation, lifecycle, mechanical checks,
// and validator output.
type BrowserConversationResult struct {
	ScenarioID   string                                  `json:"scenario_id"`
	ScenarioName string                                  `json:"scenario_name"`
	Finalized    bool                                    `json:"finalized"`
	Turns        []BrowserConversationTurn               `json:"turns,omitempty"`
	BrokerCalls  []BrowserConversationBrokerCall         `json:"broker_calls,omitempty"`
	Oracles      []BrowserConversationOracleSnapshot     `json:"oracle_snapshots,omitempty"`
	Corrections  []BrowserConversationCorrectionEvidence `json:"corrections,omitempty"`
	Recovery     []BrowserConversationRecoveryEvidence   `json:"recovery,omitempty"`
	Cancellation BrowserConversationCancellationEvidence `json:"cancellation"`
	Lifecycle    BrowserConversationLifecycleEvidence    `json:"lifecycle"`
	Mechanical   BrowserConversationMechanicalEvaluation `json:"mechanical"`
	Validator    BrowserConversationValidatorVerdict     `json:"validator"`
}

// BrowserScenarioResult and BrowserScenarioRun are descriptive aliases for
// callers using the shorter scenario vocabulary.
type BrowserScenarioResult = BrowserConversationResult
type BrowserScenarioRun = BrowserConversationRun

// Validate checks the joined evidence shape without validating the content
// of InputJSON. Invalid invocation input is an observation that must remain
// serializable for the later validity measurement.
func (r BrowserConversationResult) Validate() error {
	if strings.TrimSpace(r.ScenarioID) == "" {
		return browserConversationResultError("scenario_id", "is required")
	}
	if strings.TrimSpace(r.ScenarioName) == "" {
		return browserConversationResultError("scenario_name", "is required")
	}
	for index, turn := range r.Turns {
		path := fmt.Sprintf("turns[%d]", index)
		if turn.Sequence == 0 || turn.StepID == "" || strings.TrimSpace(turn.ObservedText) == "" {
			return browserConversationResultError(path, "requires sequence, step_id, and observed_text")
		}
		if turn.Direction != BrowserConversationCustomerTurn && turn.Direction != BrowserConversationAssistantTurn {
			return browserConversationResultError(path+".direction", "is unsupported")
		}
	}
	for index, call := range r.BrokerCalls {
		path := fmt.Sprintf("broker_calls[%d]", index)
		if call.Sequence == 0 || !browserConversationBrokerOperationValid(call.Operation) {
			return browserConversationResultError(path, "requires a sequence and supported operation")
		}
		if call.Terminal && !browserConversationInvocationStateTerminal(call.State) {
			return browserConversationResultError(path+".state", "terminal calls require a terminal invocation state")
		}
	}
	for index, correction := range r.Corrections {
		path := fmt.Sprintf("corrections[%d]", index)
		if strings.TrimSpace(correction.StepID) == "" || strings.TrimSpace(correction.TargetStepID) == "" {
			return browserConversationResultError(path, "requires step_id and target_step_id")
		}
		if strings.TrimSpace(correction.TargetUtterance) == "" || strings.TrimSpace(correction.CorrectionUtterance) == "" {
			return browserConversationResultError(path, "requires target and correction utterances")
		}
		for _, state := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "original_before", raw: correction.OriginalBefore},
			{name: "original_after", raw: correction.OriginalAfter},
			{name: "correction_before", raw: correction.CorrectionBefore},
			{name: "correction_after", raw: correction.CorrectionAfter},
		} {
			if len(state.raw) == 0 {
				continue
			}
			if err := validateJSONObject(path+"."+state.name, state.raw); err != nil {
				return err
			}
		}
	}
	for index, recovery := range r.Recovery {
		path := fmt.Sprintf("recovery[%d]", index)
		if strings.TrimSpace(recovery.StepID) == "" || strings.TrimSpace(recovery.FromPageID) == "" || strings.TrimSpace(recovery.ToPageID) == "" {
			return browserConversationResultError(path, "requires step_id, from_page_id, and to_page_id")
		}
		if recovery.StaleRejected && recovery.StaleErrorCode != string(webmcp.ErrorStaleToolRef) {
			return browserConversationResultError(path+".stale_error_code", "must be %q when stale_rejected is true", webmcp.ErrorStaleToolRef)
		}
		if recovery.ToolsRelisted && len(recovery.RelistedToolRefs) == 0 {
			return browserConversationResultError(path+".relisted_tool_refs", "must include the fresh catalog references when tools_relisted is true")
		}
		if recovery.FreshInvocationCompleted && recovery.FreshToolRef == "" {
			return browserConversationResultError(path+".fresh_tool_ref", "is required when fresh invocation completed")
		}
	}
	for index, snapshot := range r.Oracles {
		path := fmt.Sprintf("oracle_snapshots[%d]", index)
		if snapshot.Sequence == 0 || snapshot.PageID == "" {
			return browserConversationResultError(path, "requires sequence and page_id")
		}
		if snapshot.Phase != BrowserConversationOracleBefore && snapshot.Phase != BrowserConversationOracleAfter && snapshot.Phase != BrowserConversationOraclePostSession {
			return browserConversationResultError(path+".phase", "is unsupported")
		}
		if err := validateJSONObject(path+".state", snapshot.State); err != nil {
			return err
		}
	}
	if r.Lifecycle.DetachCount < 0 {
		return browserConversationResultError("lifecycle.detach_count", "must not be negative")
	}
	if r.Cancellation.LateEventsSuppressed < 0 {
		return browserConversationResultError("cancellation.late_events_suppressed", "must not be negative")
	}
	if r.Cancellation.FinalState != "" && !browserConversationInvocationStateTerminal(r.Cancellation.FinalState) {
		return browserConversationResultError("cancellation.final_state", "must be a terminal invocation state")
	}
	if r.Validator.Version != "" && r.Validator.Version != BrowserConversationValidatorVersion {
		return browserConversationResultError("validator.version", "must be %q", BrowserConversationValidatorVersion)
	}
	if r.Validator.Status != "" && r.Validator.Status != BrowserConversationValidatorPass && r.Validator.Status != BrowserConversationValidatorFail && r.Validator.Status != BrowserConversationValidatorNotRun {
		return browserConversationResultError("validator.status", "is unsupported")
	}
	if r.Cancellation.FinalState == webmcp.InvocationCompleted && (r.Cancellation.Interrupted || r.Cancellation.Requested) {
		return browserConversationResultError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
	}
	return nil
}

// BrowserConversationRun is a serialized observation collector. All mutation
// is guarded by one mutex, and Finalize publishes one immutable snapshot.
type BrowserConversationRun struct {
	mu              sync.Mutex
	scenario        BrowserConversationScenario
	steps           map[string]BrowserConversationStep
	nextSeq         uint64
	result          BrowserConversationResult
	finalized       bool
	hasCancellation bool
	hasLifecycle    bool
	hasMechanical   bool
	hasValidator    bool
	hasRecovery     bool
	hasCorrections  bool
}

// NewBrowserConversationRun validates the full scenario before creating the
// collector. No fixture, provider, process, or audio boundary is touched.
func NewBrowserConversationRun(scenario BrowserConversationScenario) (*BrowserConversationRun, error) {
	validated, err := NewBrowserConversationScenario(scenario)
	if err != nil {
		return nil, err
	}
	steps := make(map[string]BrowserConversationStep, len(validated.Steps))
	for _, step := range validated.Steps {
		steps[step.ID] = step
	}
	return &BrowserConversationRun{
		scenario: validated,
		steps:    steps,
		nextSeq:  1,
		result: BrowserConversationResult{
			ScenarioID: validated.ID, ScenarioName: validated.Name,
			Lifecycle: BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleNotStarted},
			Validator: BrowserConversationValidatorVerdict{
				Version: BrowserConversationValidatorVersion,
				Status:  BrowserConversationValidatorNotRun,
			},
		},
	}, nil
}

// NewBrowserScenarioRun is a descriptive constructor alias.
func NewBrowserScenarioRun(scenario BrowserScenario) (*BrowserConversationRun, error) {
	return NewBrowserConversationRun(scenario)
}

// Scenario returns a defensive copy of the admitted scenario.
func (r *BrowserConversationRun) Scenario() BrowserConversationScenario {
	if r == nil {
		return BrowserConversationScenario{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserConversationScenario(r.scenario)
}

// ObserveCustomerTurn appends one observed customer turn and binds its
// expected utterance from the validated scenario step.
func (r *BrowserConversationRun) ObserveCustomerTurn(stepID, observed string) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step, err := r.stepForObservationLocked(stepID, observed)
	if err != nil {
		return err
	}
	r.result.Turns = append(r.result.Turns, BrowserConversationTurn{
		Sequence: r.takeSequenceLocked(), StepID: step.ID,
		Direction: BrowserConversationCustomerTurn, ExpectedText: step.Utterance,
		ObservedText: observed, Complete: true,
	})
	return nil
}

// ObserveAssistantTurn appends one observed assistant transcript turn.
func (r *BrowserConversationRun) ObserveAssistantTurn(stepID, observed string) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step, err := r.stepForObservationLocked(stepID, observed)
	if err != nil {
		return err
	}
	r.result.Turns = append(r.result.Turns, BrowserConversationTurn{
		Sequence: r.takeSequenceLocked(), StepID: step.ID,
		Direction: BrowserConversationAssistantTurn, ObservedText: observed,
		Complete: true,
	})
	return nil
}

// ObserveTurn appends a preassembled turn, useful when the shared transcript
// collector already knows its completion state.
func (r *BrowserConversationRun) ObserveTurn(turn BrowserConversationTurn) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	step, err := r.stepForObservationLocked(turn.StepID, turn.ObservedText)
	if err != nil {
		return err
	}
	if turn.Direction != BrowserConversationCustomerTurn && turn.Direction != BrowserConversationAssistantTurn {
		return browserConversationObservationError("turn.direction", "must identify customer or assistant")
	}
	turn.Sequence = r.takeSequenceLocked()
	turn.StepID = step.ID
	if turn.Direction == BrowserConversationCustomerTurn && turn.ExpectedText == "" {
		turn.ExpectedText = step.Utterance
	}
	r.result.Turns = append(r.result.Turns, cloneBrowserConversationTurn(turn))
	return nil
}

// ObserveBrokerCall appends one broker observation without validating or
// rewriting InputJSON. Terminal state is copied exactly as observed.
func (r *BrowserConversationRun) ObserveBrokerCall(call BrowserConversationBrokerCall) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if call.Operation == "" {
		return browserConversationObservationError("broker_call.operation", "is required")
	}
	if !browserConversationBrokerOperationValid(call.Operation) {
		return browserConversationObservationError("broker_call.operation", "is unsupported")
	}
	if call.StepID != "" {
		if _, ok := r.steps[call.StepID]; !ok {
			return browserConversationObservationError("broker_call.step_id", "references unknown step %q", call.StepID)
		}
	}
	call.Sequence = r.takeSequenceLocked()
	call.InputJSON = string([]byte(call.InputJSON))
	call.Output = append(json.RawMessage(nil), call.Output...)
	r.result.BrokerCalls = append(r.result.BrokerCalls, cloneBrowserConversationBrokerCall(call))
	return nil
}

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
	for index, recovery := range evidence {
		if strings.TrimSpace(recovery.StepID) == "" || strings.TrimSpace(recovery.FromPageID) == "" || strings.TrimSpace(recovery.ToPageID) == "" {
			return browserConversationObservationError(fmt.Sprintf("recovery[%d]", index), "requires step_id, from_page_id, and to_page_id")
		}
		if recovery.StaleRejected && recovery.StaleErrorCode != string(webmcp.ErrorStaleToolRef) {
			return browserConversationObservationError(fmt.Sprintf("recovery[%d].stale_error_code", index), "must be %q when stale_rejected is true", webmcp.ErrorStaleToolRef)
		}
		if recovery.ToolsRelisted && len(recovery.RelistedToolRefs) == 0 {
			return browserConversationObservationError(fmt.Sprintf("recovery[%d].relisted_tool_refs", index), "must include the fresh catalog references when tools_relisted is true")
		}
		if recovery.FreshInvocationCompleted && recovery.FreshToolRef == "" {
			return browserConversationObservationError(fmt.Sprintf("recovery[%d].fresh_tool_ref", index), "is required when fresh invocation completed")
		}
	}
	r.hasRecovery = true
	r.result.Recovery = cloneBrowserConversationRecoveries(evidence)
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
	for index, correction := range evidence {
		path := fmt.Sprintf("corrections[%d]", index)
		if strings.TrimSpace(correction.StepID) == "" || strings.TrimSpace(correction.TargetStepID) == "" {
			return browserConversationObservationError(path, "requires step_id and target_step_id")
		}
		if strings.TrimSpace(correction.TargetUtterance) == "" || strings.TrimSpace(correction.CorrectionUtterance) == "" {
			return browserConversationObservationError(path, "requires target and correction utterances")
		}
		for _, state := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "original_before", raw: correction.OriginalBefore},
			{name: "original_after", raw: correction.OriginalAfter},
			{name: "correction_before", raw: correction.CorrectionBefore},
			{name: "correction_after", raw: correction.CorrectionAfter},
		} {
			if len(state.raw) == 0 {
				continue
			}
			if err := validateJSONObject("correction."+state.name, state.raw); err != nil {
				return err
			}
		}
	}
	r.hasCorrections = true
	r.result.Corrections = cloneBrowserConversationCorrections(evidence)
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
	if snapshot.Phase != BrowserConversationOracleBefore && snapshot.Phase != BrowserConversationOracleAfter && snapshot.Phase != BrowserConversationOraclePostSession {
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
	current := r.result.Cancellation
	if evidence.FinalState == webmcp.InvocationCompleted && (evidence.Interrupted || evidence.Requested || current.Interrupted || current.Requested) {
		return browserConversationObservationError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
	}
	if evidence.FinalState != "" && !browserConversationInvocationStateTerminal(evidence.FinalState) {
		return browserConversationObservationError("cancellation.final_state", "must be a terminal invocation state")
	}
	if evidence.LateEventsSuppressed < 0 {
		return browserConversationObservationError("cancellation.late_events_suppressed", "must not be negative")
	}
	if r.hasCancellation {
		if current.InvocationID != "" && evidence.InvocationID != "" && current.InvocationID != evidence.InvocationID {
			return browserConversationObservationError("cancellation.invocation_id", "cannot change after the invocation is identified")
		}
		if current.FinalState != "" && evidence.FinalState != "" && current.FinalState != evidence.FinalState {
			return browserConversationObservationError("cancellation.final_state", "cannot change after a terminal disposition is recorded")
		}
		current.Interrupted = current.Interrupted || evidence.Interrupted
		current.Requested = current.Requested || evidence.Requested
		if current.InvocationID == "" {
			current.InvocationID = evidence.InvocationID
		}
		if current.FinalState == "" {
			current.FinalState = evidence.FinalState
		}
		if current.Reason == "" {
			current.Reason = evidence.Reason
		}
		if current.InterruptedStepID == "" {
			current.InterruptedStepID = evidence.InterruptedStepID
		}
		if current.CancelStepID == "" {
			current.CancelStepID = evidence.CancelStepID
		}
		current.OverlappingAudioSent = current.OverlappingAudioSent || evidence.OverlappingAudioSent
		current.ExplicitCancelAudioSent = current.ExplicitCancelAudioSent || evidence.ExplicitCancelAudioSent
		if evidence.LateEventsSuppressed > 0 {
			current.LateEventsSuppressed += evidence.LateEventsSuppressed
		}
		r.result.Cancellation = current
		return nil
	}
	r.hasCancellation = true
	r.result.Cancellation = evidence
	return nil
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
	if verdict.Status != BrowserConversationValidatorPass && verdict.Status != BrowserConversationValidatorFail && verdict.Status != BrowserConversationValidatorNotRun {
		return browserConversationObservationError("validator.status", "is unsupported")
	}
	r.hasValidator = true
	r.result.Validator = cloneBrowserConversationValidatorVerdict(verdict)
	return nil
}

// Snapshot returns a defensive result snapshot. It does not finalize the run.
func (r *BrowserConversationRun) Snapshot() BrowserConversationResult {
	if r == nil {
		return BrowserConversationResult{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserConversationResult(r.result)
}

// Finalize publishes exactly one immutable result. Repeated calls return the
// same snapshot and do not permit late observations to alter it.
func (r *BrowserConversationRun) Finalize() (BrowserConversationResult, error) {
	if r == nil {
		return BrowserConversationResult{}, errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized {
		r.finalized = true
		r.result.Finalized = true
		r.result = cloneBrowserConversationResult(r.result)
	}
	return cloneBrowserConversationResult(r.result), nil
}

func (r *BrowserConversationRun) stepForObservationLocked(stepID, observed string) (BrowserConversationStep, error) {
	if err := r.ensureMutableLocked(); err != nil {
		return BrowserConversationStep{}, err
	}
	if strings.TrimSpace(stepID) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "is required")
	}
	step, ok := r.steps[stepID]
	if !ok {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "references unknown step %q", stepID)
	}
	if strings.TrimSpace(observed) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("observed_text", "must not be empty")
	}
	return step, nil
}

func (r *BrowserConversationRun) ensureMutableLocked() error {
	if r.finalized {
		return ErrBrowserConversationRunFinalized
	}
	return nil
}

func (r *BrowserConversationRun) takeSequenceLocked() uint64 {
	sequence := r.nextSeq
	r.nextSeq++
	return sequence
}

func browserConversationBrokerOperationValid(operation BrowserConversationBrokerOperation) bool {
	switch operation {
	case BrowserConversationListTools, BrowserConversationInvoke, BrowserConversationCancel, BrowserConversationSelectPage, BrowserConversationWaitReady, BrowserConversationCustomerNavigate:
		return true
	default:
		return false
	}
}

func browserConversationInvocationStateTerminal(state webmcp.InvocationState) bool {
	switch state {
	case webmcp.InvocationCompleted, webmcp.InvocationError, webmcp.InvocationCanceled, webmcp.InvocationTimedOut, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
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

func cloneBrowserConversationResult(result BrowserConversationResult) BrowserConversationResult {
	clone := result
	clone.Turns = append([]BrowserConversationTurn(nil), result.Turns...)
	clone.BrokerCalls = make([]BrowserConversationBrokerCall, len(result.BrokerCalls))
	for index, call := range result.BrokerCalls {
		clone.BrokerCalls[index] = cloneBrowserConversationBrokerCall(call)
	}
	clone.Oracles = make([]BrowserConversationOracleSnapshot, len(result.Oracles))
	for index, snapshot := range result.Oracles {
		clone.Oracles[index] = cloneBrowserConversationOracleSnapshot(snapshot)
	}
	clone.Corrections = cloneBrowserConversationCorrections(result.Corrections)
	clone.Recovery = cloneBrowserConversationRecoveries(result.Recovery)
	clone.Mechanical = cloneBrowserConversationMechanicalEvaluation(result.Mechanical)
	clone.Validator = cloneBrowserConversationValidatorVerdict(result.Validator)
	return clone
}

func cloneBrowserConversationTurn(turn BrowserConversationTurn) BrowserConversationTurn {
	return turn
}

func cloneBrowserConversationBrokerCall(call BrowserConversationBrokerCall) BrowserConversationBrokerCall {
	clone := call
	clone.Output = append(json.RawMessage(nil), call.Output...)
	clone.ToolRefs = append([]webmcp.ToolRef(nil), call.ToolRefs...)
	return clone
}

func cloneBrowserConversationRecoveries(recoveries []BrowserConversationRecoveryEvidence) []BrowserConversationRecoveryEvidence {
	if recoveries == nil {
		return nil
	}
	clone := make([]BrowserConversationRecoveryEvidence, len(recoveries))
	for index, recovery := range recoveries {
		clone[index] = recovery
		clone[index].RelistedToolRefs = append([]webmcp.ToolRef(nil), recovery.RelistedToolRefs...)
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
