// Package sessiondiagnostics owns response admission and continuation
// lifecycle state for session hosts. It deliberately has no provider, device,
// terminal, credential, or CLI dependency.
package sessiondiagnostics

import (
	"context"
	"time"
)

// ResponsePurpose identifies why a provider response was requested.
type ResponsePurpose string

const (
	ResponsePurposeNormal              ResponsePurpose = ""
	ResponsePurposeToolAcknowledgement ResponsePurpose = "tool_acknowledgement"
)

// Role identifies the producer of a terminal boundary. Hosts may use their
// own role strings; RoleTool is reserved for tool-result delivery.
type Role string

const (
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Disposition is the terminal outcome of one scheduled logical turn.
type Disposition string

const (
	DispositionPending   Disposition = "pending"
	DispositionCompleted Disposition = "completed"
	DispositionCancelled Disposition = "cancelled"
)

// EventKind is an input to Service.Apply.
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
	EventScheduledDisposition      EventKind = "scheduled.disposition"
	EventToolCall                  EventKind = "tool.call"
	EventToolResultAccepted        EventKind = "tool.result_accepted"
	EventContinuationRequested     EventKind = "tool.continuation_requested"
	EventReset                     EventKind = "lifecycle.reset"
)

// Terminal is the provider-neutral terminal metadata needed for lifecycle
// admission and typed failure reporting. The service stores a private copy.
type Terminal struct {
	Status               string
	ErrorCode            string
	ErrorMessage         string
	StatusDetails        string
	Reason               string
	ProviderCancellation bool
}

// Event is an immutable host observation. Fields not used by Kind are ignored.
type Event struct {
	Kind        EventKind
	ResponseID  string
	LifecycleID string
	Purpose     ResponsePurpose
	Role        Role
	CallID      string
	ToolName    string
	Terminal    *Terminal
	Output      bool
	Disposition Disposition
	Index       int
	Count       int
}

// RetryScheduler is injected by hosts that want the service to advance a
// bounded deterministic scheduler when a retry is claimed. The reducer never
// sleeps and never starts a goroutine itself.
type RetryScheduler func(context.Context, time.Duration) error

// Options contains construction-time dependencies. A zero value is inert and
// uses no wall clock or background worker.
type Options struct {
	RetryScheduler RetryScheduler
}

// RetryRequest describes a claimed one-time replacement response.
type RetryRequest struct {
	Accepted  bool
	Delay     time.Duration
	Exhausted bool
}

// Observation is an immutable result of one event. It contains only copied
// scalar values and no mutable reducer state.
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

// ScheduledState is a read-only view of one dispatched scheduled logical turn.
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

// ContinuationState is a read-only view of one provider tool call and its
// follow-on response.
type ContinuationState struct {
	CallID                     string
	ToolName                   string
	ResponseID                 string
	ProviderCallObserved       bool
	ResultAccepted             bool
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

// Snapshot is a deep immutable view of the reducer state.
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
	// ErrClosed identifies an event submitted after Close.
	ErrClosed lifecycleError = "session diagnostics lifecycle is closed"
	// ErrMalformedSequence identifies an event that cannot be associated with
	// the current lifecycle without guessing ownership.
	ErrMalformedSequence lifecycleError = "malformed session diagnostics sequence"
	// ErrRetryExhausted identifies a second retry claim for one scheduled turn.
	ErrRetryExhausted lifecycleError = "session diagnostics retry budget exhausted"
)

// Service owns all mutable response, scheduled-turn, retry, and continuation
// state. Implementations are safe for concurrent host observations.
type Service interface {
	Apply(context.Context, Event) (Observation, error)
	Snapshot() Snapshot
	Reset()
	Close() error
}
