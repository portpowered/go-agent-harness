package browserscenario

import "encoding/json"

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
	ToolRef            any                                `json:"tool_ref,omitempty"`
	ToolName           string                             `json:"tool_name,omitempty"`
	InvocationID       any                                `json:"invocation_id,omitempty"`
	InputJSON          string                             `json:"input_json"`
	State              any                                `json:"state,omitempty"`
	Terminal           bool                               `json:"terminal"`
	Output             json.RawMessage                    `json:"output,omitempty"`
	ErrorCode          string                             `json:"error_code,omitempty"`
	Generation         uint64                             `json:"generation,omitempty"`
	PreviousGeneration uint64                             `json:"previous_generation,omitempty"`
	ToolRefs           any                                `json:"tool_refs,omitempty"`
}

// BrowserConversationInputJSONAttempt is the immutable validity observation
// for one recorded webmcp_invoke call. InputJSON remains the exact model
// string; ValidObject is derived from it and never replaces it.
type BrowserConversationInputJSONAttempt struct {
	Sequence     uint64 `json:"sequence"`
	StepID       string `json:"step_id,omitempty"`
	InvocationID any    `json:"invocation_id,omitempty"`
	ToolRef      any    `json:"tool_ref,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	State        any    `json:"state,omitempty"`
	Terminal     bool   `json:"terminal"`
	InputJSON    string `json:"input_json"`
	ValidObject  bool   `json:"valid_object"`
}

// BrowserConversationInputJSONValidity is the exact numerator/denominator
// measurement over every observed webmcp_invoke broker call. Attempts are
// retained so an invalid model value can be reviewed without coercion,
// retry, or omission.
type BrowserConversationInputJSONValidity struct {
	ValidObjectStrings int                                   `json:"valid_object_strings"`
	TotalAttempts      int                                   `json:"total_attempts"`
	Percentage         float64                               `json:"percentage"`
	Attempts           []BrowserConversationInputJSONAttempt `json:"attempts,omitempty"`
}

// BrowserConversationRecoveryEvidence records the ordered facts needed to
// prove customer-navigation recovery. A stale reference is retained exactly
// as attempted; it is never replaced with the fresh reference in-place.
type BrowserConversationRecoveryEvidence struct {
	StepID                   string `json:"step_id"`
	FromPageID               string `json:"from_page_id"`
	ToPageID                 string `json:"to_page_id"`
	NavigationObserved       bool   `json:"navigation_observed"`
	PreviousGeneration       uint64 `json:"previous_generation,omitempty"`
	CurrentGeneration        uint64 `json:"current_generation,omitempty"`
	StaleToolRef             any    `json:"stale_tool_ref,omitempty"`
	StaleInvocationID        any    `json:"stale_invocation_id,omitempty"`
	StaleGeneration          uint64 `json:"stale_generation,omitempty"`
	StaleErrorCode           string `json:"stale_error_code,omitempty"`
	StaleRejected            bool   `json:"stale_rejected"`
	ToolsRelisted            bool   `json:"tools_relisted"`
	RelistedToolRefs         any    `json:"relisted_tool_refs,omitempty"`
	RelistedGeneration       uint64 `json:"relisted_generation,omitempty"`
	FreshToolRef             any    `json:"fresh_tool_ref,omitempty"`
	FreshGeneration          uint64 `json:"fresh_generation,omitempty"`
	RetryInvocationID        any    `json:"retry_invocation_id,omitempty"`
	FreshInvocationCompleted bool   `json:"fresh_invocation_completed"`
	Passed                   bool   `json:"passed"`
}

// BrowserConversationCorrectionEvidence preserves both the original and
// correcting customer intents alongside their independently observed state
// transitions. The invocation and assistant fields are evidence, not claims
// inferred from either transcript.
type BrowserConversationCorrectionEvidence struct {
	StepID                        string          `json:"step_id"`
	TargetStepID                  string          `json:"target_step_id"`
	TargetUtterance               string          `json:"target_utterance"`
	CorrectionUtterance           string          `json:"correction_utterance"`
	OriginalBefore                json.RawMessage `json:"original_before,omitempty"`
	OriginalAfter                 json.RawMessage `json:"original_after,omitempty"`
	CorrectionBefore              json.RawMessage `json:"correction_before,omitempty"`
	CorrectionAfter               json.RawMessage `json:"correction_after,omitempty"`
	OriginalInvocationID          any             `json:"original_invocation_id,omitempty"`
	CorrectionInvocationID        any             `json:"correction_invocation_id,omitempty"`
	OriginalToolName              string          `json:"original_tool_name,omitempty"`
	CorrectionToolName            string          `json:"correction_tool_name,omitempty"`
	OriginalInvocationCompleted   bool            `json:"original_invocation_completed"`
	CorrectionInvocationCompleted bool            `json:"correction_invocation_completed"`
	OriginalAssistantText         string          `json:"original_assistant_text,omitempty"`
	CorrectionAssistantText       string          `json:"correction_assistant_text,omitempty"`
	Passed                        bool            `json:"passed"`
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
	Interrupted             bool   `json:"interrupted"`
	Requested               bool   `json:"requested"`
	InvocationID            any    `json:"invocation_id,omitempty"`
	FinalState              any    `json:"final_state,omitempty"`
	Reason                  string `json:"reason,omitempty"`
	InterruptedStepID       string `json:"interrupted_step_id,omitempty"`
	CancelStepID            string `json:"cancel_step_id,omitempty"`
	OverlappingAudioSent    bool   `json:"overlapping_audio_sent"`
	ExplicitCancelAudioSent bool   `json:"explicit_cancel_audio_sent"`
	LateEventsSuppressed    int    `json:"late_events_suppressed,omitempty"`
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
	ExternalBrowserID         any                                 `json:"external_browser_id,omitempty"`
	ExternalTargetID          any                                 `json:"external_target_id,omitempty"`
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
	ScenarioID        string                                  `json:"scenario_id"`
	ScenarioName      string                                  `json:"scenario_name"`
	Finalized         bool                                    `json:"finalized"`
	Turns             []BrowserConversationTurn               `json:"turns,omitempty"`
	BrokerCalls       []BrowserConversationBrokerCall         `json:"broker_calls,omitempty"`
	InputJSONValidity BrowserConversationInputJSONValidity    `json:"input_json_validity"`
	Oracles           []BrowserConversationOracleSnapshot     `json:"oracle_snapshots,omitempty"`
	Corrections       []BrowserConversationCorrectionEvidence `json:"corrections,omitempty"`
	Recovery          []BrowserConversationRecoveryEvidence   `json:"recovery,omitempty"`
	Cancellation      BrowserConversationCancellationEvidence `json:"cancellation"`
	Lifecycle         BrowserConversationLifecycleEvidence    `json:"lifecycle"`
	Mechanical        BrowserConversationMechanicalEvaluation `json:"mechanical"`
	Validator         BrowserConversationValidatorVerdict     `json:"validator"`
}

// BrowserScenarioResult and BrowserScenarioRun are descriptive aliases for
// callers using the shorter scenario vocabulary.
type BrowserScenarioResult = BrowserConversationResult
type BrowserScenarioRun = BrowserConversationRun
