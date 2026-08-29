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
	BrowserConversationListTools  BrowserConversationBrokerOperation = "webmcp_list_tools"
	BrowserConversationInvoke     BrowserConversationBrokerOperation = "webmcp_invoke"
	BrowserConversationCancel     BrowserConversationBrokerOperation = "webmcp_cancel"
	BrowserConversationSelectPage BrowserConversationBrokerOperation = "webmcp_select_page"
	BrowserConversationWaitReady  BrowserConversationBrokerOperation = "webmcp_wait_ready"
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
	Sequence     uint64                             `json:"sequence"`
	StepID       string                             `json:"step_id,omitempty"`
	Operation    BrowserConversationBrokerOperation `json:"operation"`
	ToolRef      webmcp.ToolRef                     `json:"tool_ref,omitempty"`
	ToolName     string                             `json:"tool_name,omitempty"`
	InvocationID webmcp.InvocationID                `json:"invocation_id,omitempty"`
	InputJSON    string                             `json:"input_json"`
	State        webmcp.InvocationState             `json:"state,omitempty"`
	Terminal     bool                               `json:"terminal"`
	Output       json.RawMessage                    `json:"output,omitempty"`
	ErrorCode    string                             `json:"error_code,omitempty"`
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
	Interrupted  bool                   `json:"interrupted"`
	Requested    bool                   `json:"requested"`
	InvocationID webmcp.InvocationID    `json:"invocation_id,omitempty"`
	FinalState   webmcp.InvocationState `json:"final_state,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
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
	BrowserClosed             bool                                `json:"browser_closed"`
	TargetClosed              bool                                `json:"target_closed"`
	ExternalTabAlive          bool                                `json:"external_tab_alive"`
	ExternalTabResponsive     bool                                `json:"external_tab_responsive"`
	ExternalTabAllowsMutation bool                                `json:"external_tab_allows_mutation"`
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

// RecordCancellation records interruption/cancel facts once.
func (r *BrowserConversationRun) RecordCancellation(evidence BrowserConversationCancellationEvidence) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if r.hasCancellation {
		return ErrBrowserConversationDuplicateObservation
	}
	if evidence.FinalState == webmcp.InvocationCompleted && (evidence.Interrupted || evidence.Requested) {
		return browserConversationObservationError("cancellation.final_state", "a canceled or interrupted invocation cannot be completed")
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
	case BrowserConversationListTools, BrowserConversationInvoke, BrowserConversationCancel, BrowserConversationSelectPage, BrowserConversationWaitReady:
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
