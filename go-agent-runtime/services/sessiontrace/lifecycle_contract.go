package sessiontrace

import (
	"context"
	"time"
)

type LifecycleResponsePurpose string

const (
	LifecycleResponsePurposeNormal              LifecycleResponsePurpose = ""
	LifecycleResponsePurposeToolAcknowledgement LifecycleResponsePurpose = "tool_acknowledgement"
)

type LifecycleRole string

const (
	LifecycleRoleAssistant LifecycleRole = "assistant"
	LifecycleRoleTool      LifecycleRole = "tool"
)

type LifecycleDisposition string

const (
	LifecycleDispositionPending   LifecycleDisposition = "pending"
	LifecycleDispositionCompleted LifecycleDisposition = "completed"
	LifecycleDispositionCancelled LifecycleDisposition = "cancelled"
)

type LifecycleEventKind string

const (
	LifecycleEventResponseOpen              LifecycleEventKind = "response.open"
	LifecycleEventResponseAdopt             LifecycleEventKind = "response.adopt"
	LifecycleEventResponseBelongs           LifecycleEventKind = "response.belongs"
	LifecycleEventResponseOwnsEnd           LifecycleEventKind = "response.owns_end"
	LifecycleEventResponseContent           LifecycleEventKind = "response.content"
	LifecycleEventResponseContentBoundary   LifecycleEventKind = "response.content_boundary"
	LifecycleEventResponseEnd               LifecycleEventKind = "response.end"
	LifecycleEventResponseFinish            LifecycleEventKind = "response.finish"
	LifecycleEventBindScheduledBoundary     LifecycleEventKind = "scheduled.bind_boundary"
	LifecycleEventBindScheduledTerminalOnly LifecycleEventKind = "scheduled.bind_terminal_only"
	LifecycleEventBindScheduledID           LifecycleEventKind = "scheduled.bind_id"
	LifecycleEventSetScheduledOwner         LifecycleEventKind = "scheduled.set_owner"
	LifecycleEventEnsureScheduled           LifecycleEventKind = "scheduled.ensure"
	LifecycleEventNoteScheduledTerminal     LifecycleEventKind = "scheduled.note_terminal"
	LifecycleEventRememberRetry             LifecycleEventKind = "scheduled.remember_retry"
	LifecycleEventClaimRetry                LifecycleEventKind = "scheduled.claim_retry"
	LifecycleEventRetryDispatched           LifecycleEventKind = "scheduled.retry_dispatched"
	LifecycleEventScheduledDisposition      LifecycleEventKind = "scheduled.disposition"
	LifecycleEventToolCall                  LifecycleEventKind = "tool.call"
	LifecycleEventToolResultAccepted        LifecycleEventKind = "tool.result_accepted"
	LifecycleEventToolResultRejected        LifecycleEventKind = "tool.result_rejected"
	LifecycleEventToolResponseComplete      LifecycleEventKind = "tool.response_complete"
	LifecycleEventContinuationRequested     LifecycleEventKind = "tool.continuation_requested"
	LifecycleEventReset                     LifecycleEventKind = "lifecycle.reset"
)

type LifecycleTerminal struct {
	Status               string
	ErrorCode            string
	ErrorMessage         string
	StatusDetails        string
	Reason               string
	ProviderCancellation bool
}

type LifecycleEvent struct {
	Kind         LifecycleEventKind
	ResponseID   string
	LifecycleID  string
	Purpose      LifecycleResponsePurpose
	Role         LifecycleRole
	CallID       string
	ToolName     string
	ResultStatus string
	Terminal     *LifecycleTerminal
	Output       bool
	Disposition  LifecycleDisposition
	Index        int
	Count        int
}

type LifecycleRetryScheduler func(context.Context, time.Duration) error

type LifecycleOptions struct {
	RetryScheduler LifecycleRetryScheduler
}

type LifecycleRetryRequest struct {
	Accepted  bool
	Delay     time.Duration
	Exhausted bool
}

type LifecycleObservation struct {
	Accepted             bool
	NewResponse          bool
	OwnsResponse         bool
	Candidate            bool
	Admitted             bool
	ContinuationChanged  bool
	ResponseID           string
	ScheduledIndex       int
	HasScheduledIndex    bool
	Disposition          LifecycleDisposition
	PendingContinuations int
	Retry                LifecycleRetryRequest
}

type LifecycleScheduledState struct {
	Bound                 bool
	ResponseIDs           []string
	Disposition           LifecycleDisposition
	RetryUsed             bool
	RetryPending          bool
	TerminalFailure       bool
	TerminalStatus        string
	TerminalErrorCode     string
	TerminalStatusDetails string
}

type LifecycleContinuationState struct {
	CallID                     string
	ToolName                   string
	ResponseID                 string
	ProviderCallObserved       bool
	ResultAccepted             bool
	ResultRejected             bool
	ResultRejectionStatus      string
	ToolResponseComplete       bool
	ContinuationRequested      bool
	ContinuationResponseID     string
	ContinuationScheduledIndex int
	ContinuationScheduledSet   bool
	ContinuationTerminalSeen   bool
	ContinuationStatus         string
	ContinuationErrorCode      string
	ContinuationStatusDetails  string
	ContinuationReason         string
	ContinuationOutput         bool
	ContinuationFailure        bool
	ContinuationComplete       bool
}

type LifecycleSnapshot struct {
	Closed                bool
	ActiveResponse        bool
	ActiveResponseID      string
	ActivePurpose         LifecycleResponsePurpose
	CompletedResponseIDs  []string
	RetiredResponseIDs    []string
	Scheduled             []LifecycleScheduledState
	ScheduledResponseByID map[string]int
	NextScheduledResponse int
	ActiveScheduledIndex  int
	ActiveScheduledID     string
	ActiveScheduledSet    bool
	LogicalScheduledIndex int
	LogicalScheduledID    string
	LogicalScheduledSet   bool
	CompletedScheduled    int
	RetryCandidateIndex   int
	RetryCandidateSet     bool
	RetryCandidateID      string
	ContinuationStates    []LifecycleContinuationState
}

type LifecycleError string

func (e LifecycleError) Error() string { return string(e) }

const (
	ErrLifecycleClosed            LifecycleError = "session diagnostics lifecycle is closed"
	ErrLifecycleMalformedSequence LifecycleError = "malformed session diagnostics sequence"
	ErrLifecycleRetryExhausted    LifecycleError = "session diagnostics retry budget exhausted"
)

type LifecycleService interface {
	Apply(context.Context, LifecycleEvent) (LifecycleObservation, error)
	Snapshot() LifecycleSnapshot
	Reset()
	Close() error
}
