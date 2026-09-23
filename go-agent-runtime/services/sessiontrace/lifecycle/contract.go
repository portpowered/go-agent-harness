// Package lifecycle exposes the sessiontrace continuation contract without
// exposing its mutable reducer implementation.
package lifecycle

import (
	internal "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/internal/lifecycle"
)

type ResponsePurpose = internal.ResponsePurpose
type Role = internal.Role
type Disposition = internal.Disposition
type EventKind = internal.EventKind
type Terminal = internal.Terminal
type Event = internal.Event
type RetryScheduler = internal.RetryScheduler
type Options = internal.Options
type RetryRequest = internal.RetryRequest
type Observation = internal.Observation
type ScheduledState = internal.ScheduledState
type ContinuationState = internal.ContinuationState
type Snapshot = internal.Snapshot
type Service = internal.Service

const (
	ResponsePurposeNormal              = internal.ResponsePurposeNormal
	ResponsePurposeToolAcknowledgement = internal.ResponsePurposeToolAcknowledgement
	RoleAssistant                      = internal.RoleAssistant
	RoleTool                           = internal.RoleTool
	DispositionPending                 = internal.DispositionPending
	DispositionCompleted               = internal.DispositionCompleted
	DispositionCancelled               = internal.DispositionCancelled
	EventResponseOpen                  = internal.EventResponseOpen
	EventResponseAdopt                 = internal.EventResponseAdopt
	EventResponseBelongs               = internal.EventResponseBelongs
	EventResponseOwnsEnd               = internal.EventResponseOwnsEnd
	EventResponseContent               = internal.EventResponseContent
	EventResponseContentBoundary       = internal.EventResponseContentBoundary
	EventResponseEnd                   = internal.EventResponseEnd
	EventResponseFinish                = internal.EventResponseFinish
	EventBindScheduledBoundary         = internal.EventBindScheduledBoundary
	EventBindScheduledTerminalOnly     = internal.EventBindScheduledTerminalOnly
	EventBindScheduledID               = internal.EventBindScheduledID
	EventSetScheduledOwner             = internal.EventSetScheduledOwner
	EventEnsureScheduled               = internal.EventEnsureScheduled
	EventNoteScheduledTerminal         = internal.EventNoteScheduledTerminal
	EventRememberRetry                 = internal.EventRememberRetry
	EventClaimRetry                    = internal.EventClaimRetry
	EventRetryDispatched               = internal.EventRetryDispatched
	EventScheduledDisposition          = internal.EventScheduledDisposition
	EventToolCall                      = internal.EventToolCall
	EventToolResultAccepted            = internal.EventToolResultAccepted
	EventToolResultRejected            = internal.EventToolResultRejected
	EventToolResponseComplete          = internal.EventToolResponseComplete
	EventContinuationRequested         = internal.EventContinuationRequested
	EventReset                         = internal.EventReset
	ErrClosed                          = internal.ErrClosed
	ErrMalformedSequence               = internal.ErrMalformedSequence
	ErrRetryExhausted                  = internal.ErrRetryExhausted
)
