// Package lifecycle contains the private implementation aliases for the
// sessiontrace continuation contract.
package lifecycle

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"

type ResponsePurpose = sessiontrace.LifecycleResponsePurpose

const (
	ResponsePurposeNormal              = sessiontrace.LifecycleResponsePurposeNormal
	ResponsePurposeToolAcknowledgement = sessiontrace.LifecycleResponsePurposeToolAcknowledgement
)

type Role = sessiontrace.LifecycleRole

const (
	RoleAssistant = sessiontrace.LifecycleRoleAssistant
	RoleTool      = sessiontrace.LifecycleRoleTool
)

type Disposition = sessiontrace.LifecycleDisposition

const (
	DispositionPending   = sessiontrace.LifecycleDispositionPending
	DispositionCompleted = sessiontrace.LifecycleDispositionCompleted
	DispositionCancelled = sessiontrace.LifecycleDispositionCancelled
)

type EventKind = sessiontrace.LifecycleEventKind

const (
	EventResponseOpen              = sessiontrace.LifecycleEventResponseOpen
	EventResponseAdopt             = sessiontrace.LifecycleEventResponseAdopt
	EventResponseBelongs           = sessiontrace.LifecycleEventResponseBelongs
	EventResponseOwnsEnd           = sessiontrace.LifecycleEventResponseOwnsEnd
	EventResponseContent           = sessiontrace.LifecycleEventResponseContent
	EventResponseContentBoundary   = sessiontrace.LifecycleEventResponseContentBoundary
	EventResponseEnd               = sessiontrace.LifecycleEventResponseEnd
	EventResponseFinish            = sessiontrace.LifecycleEventResponseFinish
	EventBindScheduledBoundary     = sessiontrace.LifecycleEventBindScheduledBoundary
	EventBindScheduledTerminalOnly = sessiontrace.LifecycleEventBindScheduledTerminalOnly
	EventBindScheduledID           = sessiontrace.LifecycleEventBindScheduledID
	EventSetScheduledOwner         = sessiontrace.LifecycleEventSetScheduledOwner
	EventEnsureScheduled           = sessiontrace.LifecycleEventEnsureScheduled
	EventNoteScheduledTerminal     = sessiontrace.LifecycleEventNoteScheduledTerminal
	EventRememberRetry             = sessiontrace.LifecycleEventRememberRetry
	EventClaimRetry                = sessiontrace.LifecycleEventClaimRetry
	EventRetryDispatched           = sessiontrace.LifecycleEventRetryDispatched
	EventScheduledDisposition      = sessiontrace.LifecycleEventScheduledDisposition
	EventToolCall                  = sessiontrace.LifecycleEventToolCall
	EventToolResultAccepted        = sessiontrace.LifecycleEventToolResultAccepted
	EventToolResultRejected        = sessiontrace.LifecycleEventToolResultRejected
	EventToolResponseComplete      = sessiontrace.LifecycleEventToolResponseComplete
	EventContinuationRequested     = sessiontrace.LifecycleEventContinuationRequested
	EventReset                     = sessiontrace.LifecycleEventReset
)

type Terminal = sessiontrace.LifecycleTerminal
type Event = sessiontrace.LifecycleEvent
type RetryScheduler = sessiontrace.LifecycleRetryScheduler
type Options = sessiontrace.LifecycleOptions
type RetryRequest = sessiontrace.LifecycleRetryRequest
type Observation = sessiontrace.LifecycleObservation
type ScheduledState = sessiontrace.LifecycleScheduledState
type ContinuationState = sessiontrace.LifecycleContinuationState
type Snapshot = sessiontrace.LifecycleSnapshot
type Service = sessiontrace.LifecycleService

const (
	ErrClosed            = sessiontrace.ErrLifecycleClosed
	ErrMalformedSequence = sessiontrace.ErrLifecycleMalformedSequence
	ErrRetryExhausted    = sessiontrace.ErrLifecycleRetryExhausted
)
