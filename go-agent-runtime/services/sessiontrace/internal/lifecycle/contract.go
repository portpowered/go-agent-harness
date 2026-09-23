// Package lifecycle contains the private sessiontrace response lifecycle
// contract used by the trace observer. It is not a host-facing package.
package lifecycle

import (
	"context"
	"time"
)

type ResponsePurpose string

const (
	ResponsePurposeNormal              ResponsePurpose = ""
	ResponsePurposeToolAcknowledgement ResponsePurpose = "tool_acknowledgement"
)

type Role string

const (
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Disposition string

const (
	DispositionPending   Disposition = "pending"
	DispositionCompleted Disposition = "completed"
	DispositionCancelled Disposition = "cancelled"
)

type EventKind string

const (
	EventResponseOpen              EventKind = "response.open"
	EventResponseAdopt             EventKind = "response.adopt"
	EventResponseBelongs           EventKind = "response.belongs"
	EventResponseOwnsEnd           EventKind = "response.owns_end"
	EventResponseContent           EventKind = "response.content"
	EventResponseContentBoundary   EventKind = "response.content_boundary"
	EventResponseEnd               EventKind = "response.end"
	EventResponseFinish            EventKind = "response.finish"
	EventBindScheduledBoundary     EventKind = "scheduled.bind_boundary"
	EventBindScheduledTerminalOnly EventKind = "scheduled.bind_terminal_only"
	EventBindScheduledID           EventKind = "scheduled.bind_id"
	EventSetScheduledOwner         EventKind = "scheduled.set_owner"
	EventEnsureScheduled           EventKind = "scheduled.ensure"
	EventNoteScheduledTerminal     EventKind = "scheduled.note_terminal"
	EventRememberRetry             EventKind = "scheduled.remember_retry"
	EventClaimRetry                EventKind = "scheduled.claim_retry"
	EventRetryDispatched           EventKind = "scheduled.retry_dispatched"
	EventScheduledDisposition      EventKind = "scheduled.disposition"
	EventToolCall                  EventKind = "tool.call"
	EventToolResultAccepted        EventKind = "tool.result_accepted"
	EventToolResultRejected        EventKind = "tool.result_rejected"
	EventToolResponseComplete      EventKind = "tool.response_complete"
	EventContinuationRequested     EventKind = "tool.continuation_requested"
	EventReset                     EventKind = "lifecycle.reset"
)

type Terminal struct {
	Status               string
	ErrorCode            string
	ErrorMessage         string
	StatusDetails        string
	Reason               string
	ProviderCancellation bool
}

type Event struct {
	Kind         EventKind
	ResponseID   string
	LifecycleID  string
	Purpose      ResponsePurpose
	Role         Role
	CallID       string
	ToolName     string
	ResultStatus string
	Terminal     *Terminal
	Output       bool
	Disposition  Disposition
	Index        int
	Count        int
}

type RetryScheduler func(context.Context, time.Duration) error

type Options struct {
	RetryScheduler RetryScheduler
}

type RetryRequest struct {
	Accepted  bool
	Delay     time.Duration
	Exhausted bool
}

type Observation struct {
	Accepted             bool
	NewResponse          bool
	OwnsResponse         bool
	Candidate            bool
	Admitted             bool
	ContinuationChanged  bool
	ResponseID           string
	ScheduledIndex       int
	HasScheduledIndex    bool
	Disposition          Disposition
	PendingContinuations int
	Retry                RetryRequest
}

type ScheduledState struct {
	Bound                 bool
	ResponseIDs           []string
	Disposition           Disposition
	RetryUsed             bool
	RetryPending          bool
	TerminalFailure       bool
	TerminalStatus        string
	TerminalErrorCode     string
	TerminalStatusDetails string
}

type ContinuationState struct {
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

type Snapshot struct {
	Closed                bool
	ActiveResponse        bool
	ActiveResponseID      string
	ActivePurpose         ResponsePurpose
	CompletedResponseIDs  []string
	RetiredResponseIDs    []string
	Scheduled             []ScheduledState
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
	ContinuationStates    []ContinuationState
}

type lifecycleError string

func (e lifecycleError) Error() string { return string(e) }

const (
	ErrClosed            lifecycleError = "session diagnostics lifecycle is closed"
	ErrMalformedSequence lifecycleError = "malformed session diagnostics sequence"
	ErrRetryExhausted    lifecycleError = "session diagnostics retry budget exhausted"
)

type Service interface {
	Apply(context.Context, Event) (Observation, error)
	Snapshot() Snapshot
	Reset()
	Close() error
}
