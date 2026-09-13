// Package sessionterminal owns terminal session policy and final accounting.
// Hosts provide bounded observations; the implementation does not discover
// configuration, environment, credentials, or terminal state.
package sessionterminal

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

const (
	// MaxDiagnosticItems bounds each collection crossing the terminal service
	// boundary. The session observer already keeps these collections small; the
	// second bound makes the public contract safe for embedded callers too.
	MaxDiagnosticItems = 4096
	// MaxDiagnosticValueBytes bounds provider/model identifiers and metadata
	// values retained in a terminal record.
	MaxDiagnosticValueBytes = 1024
	// MaxContinuationDetailBytes preserves the established compact provider
	// detail bound while keeping every terminal field finite.
	MaxContinuationDetailBytes = 256
	// MaxMetricSeries and MaxMetricBuckets bound defensive metric snapshots.
	MaxMetricSeries  = 64
	MaxMetricBuckets = 256
)

const (
	EventFailure  = "session_failure"
	EventTerminal = "session_terminal"
	EventMetrics  = "session_metrics"
)

const (
	FieldClassification     = "classification"
	FieldTerminalReason     = "terminal_reason"
	FieldTerminalProvenance = "terminal_provenance"
	FieldOutputState        = "output_state"
	FieldProvider           = "provider"
	FieldModel              = "model"
	FieldTurnsCompleted     = "turns_completed"
	FieldFailingEvent       = "failing_event"
	FieldProviderErrorType  = "provider_error_type"
	FieldProviderErrorCode  = "provider_error_code"

	FieldUnresolvedToolResultCount = "unresolved_tool_result_count"
	FieldUnresolvedToolCallIDs     = "unresolved_tool_call_ids"

	FieldPendingImageContinuationCount = "pending_image_continuation_count"
	FieldPendingImageContinuationIDs   = "pending_image_continuation_call_ids"
	FieldPendingToolContinuationCount  = "pending_tool_continuation_count"
	FieldPendingToolContinuationIDs    = "pending_tool_continuation_call_ids"

	FieldScheduledInputCount  = "scheduled_input_count"
	FieldDispatchedInputCount = "dispatched_input_count"
	FieldCompletedTurnCount   = "completed_turn_count"

	FieldPendingContinuationStatuses = "pending_continuation_statuses"
	FieldPendingContinuationCodes    = "pending_continuation_codes"
	FieldPendingContinuationDetails  = "pending_continuation_details"

	FieldCancelledBy                      = "cancelled_by"
	FieldCancelledScheduledInputCount     = "cancelled_scheduled_input_count"
	FieldCancelledToolResultCount         = "cancelled_tool_result_count"
	FieldCancelledToolResultCallIDs       = "cancelled_tool_result_call_ids"
	FieldCancelledToolContinuationCount   = "cancelled_tool_continuation_count"
	FieldCancelledToolContinuationCallIDs = "cancelled_tool_continuation_call_ids"
)

const (
	// FailureHint values are caller-owned evidence labels. They identify a
	// lifecycle obligation whose public error identity was already observed;
	// classification and field publication remain service policy.
	FailureHintUnresolvedToolResults       = "unresolved_tool_results"
	FailureHintImageContinuationIncomplete = "image_continuation_incomplete"
	FailureHintToolContinuationIncomplete  = "tool_continuation_incomplete"
	FailureHintScheduledAudioIncomplete    = "scheduled_audio_incomplete"
)

// Record is a host-neutral structured terminal event. Fields are copied by
// the service before the result is returned.
type Record struct {
	Event  string
	Fields map[string]string
}

// FailureFacts is the structured failure value captured at the stream or
// session boundary. Empty fields receive the same stable defaults as the
// historical session observer.
type FailureFacts struct {
	Classification    string
	TerminalReason    messages.TerminalReason
	Provenance        messages.TerminalProvenance
	OutputState       messages.TerminalOutputState
	ProviderErrorType string
	ProviderErrorCode string
	FailingEvent      string
}

// OutputSnapshot contains only output evidence needed to choose the public
// cancellation output state. It does not retain payload bytes.
type OutputSnapshot struct {
	SawSessionOpen           bool
	TurnsCompleted           int
	TotalOutputAudioBytes    uint64
	TotalOutputTextBytes     uint64
	ResponseOutputAudioBytes uint64
	ResponseOutputTextBytes  uint64
	AssistantOutputObserved  bool
}

// ByteSnapshot contains the drained byte counters published by the terminal
// metrics record. It intentionally contains counts, never payloads.
type ByteSnapshot struct {
	InputAudioBytes  uint64
	InputTextBytes   uint64
	OutputAudioBytes uint64
	OutputTextBytes  uint64
	OutputToolBytes  uint64
}

// ContinuationSnapshot is the bounded provider context for accepted tool
// results whose model continuation is still pending.
type ContinuationSnapshot struct {
	Statuses map[string]string
	Codes    map[string]string
	Details  map[string]string
}

// ScheduledSnapshot contains the logical scheduled-audio counters and the
// first retained provider terminal context, if one exists.
type ScheduledSnapshot struct {
	Completed      int
	Dispatched     int
	Inputs         int
	Incomplete     bool
	FailureStatus  string
	FailureCode    string
	FailureDetails string
}

// LifecycleSnapshot contains copyable obligations observed at finalization.
// FailureHints are translated from caller-specific typed sentinels at the
// host edge; they are not errors and cannot replace the original RunError.
type LifecycleSnapshot struct {
	UnresolvedToolResultCallIDs []string
	// PendingContinuationCallIDs is the complete accepted-continuation set,
	// including image and non-image calls, used by cancellation accounting.
	PendingContinuationCallIDs  []string
	PendingToolContinuationIDs  []string
	PendingImageContinuationIDs []string
	PendingContinuations        ContinuationSnapshot
	Scheduled                   ScheduledSnapshot
	FailureHints                []string
}

// TokenSnapshot carries non-negative, session-cumulative provider usage.
type TokenSnapshot struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	Seen             bool
}

// Request is the immutable-at-entry value supplied to one terminal decision.
// RunError is intentionally retained as an error so errors.Is/errors.As
// continue to reach provider, replay, cancellation, and loop causes.
type Request struct {
	RunError              error
	UserCancelled         bool
	RoomBoundCancellation bool
	RoomCancellationOnly  bool
	Provider              string
	Model                 string
	TurnsCompleted        int
	Output                OutputSnapshot
	Bytes                 ByteSnapshot
	Failure               *FailureFacts
	Lifecycle             LifecycleSnapshot
	Usage                 TokenSnapshot
	Metrics               metrics.Snapshot
}

// TokenUsageSemantics identifies how TokenSnapshot values were accumulated.
type TokenUsageSemantics string

const TokenUsageIncremental TokenUsageSemantics = "incremental"

// FinalAccounting is the production-owned terminal accounting result. Metrics
// is a complete deep-copied snapshot and can be retained by the caller.
type FinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   TokenUsageSemantics
	Metrics          metrics.Snapshot
}

// Result retains the original terminal error and returns zero or more stable
// records plus the final drained accounting snapshot.
type Result struct {
	Error      error
	Records    []Record
	Accounting *FinalAccounting
}

// Service owns terminal precedence, error classification, cancellation output
// state, metadata formatting, and final accounting. Implementations are
// stateless and are constructed through the dedicated Wire package.
type Service interface {
	Finalize(Request) Result
	CancellationOutputState(OutputSnapshot) messages.TerminalOutputState
}
