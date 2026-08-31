package services

import (
	"context"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionUserCancelledClassification is the terminal classification emitted
// when the CLI observed SIGINT and no independent failure caused shutdown.
const SessionUserCancelledClassification = "user_cancelled"

// SessionCancellationIntent is the run-scoped signal state owned by the CLI
// and consumed by the session service. It is deliberately not package-global:
// concurrent session commands must never share signal intent.
type SessionCancellationIntent struct {
	sigint atomic.Bool
}

// NewSessionCancellationIntent returns an unset cancellation marker for one
// session command invocation.
func NewSessionCancellationIntent() *SessionCancellationIntent {
	return &SessionCancellationIntent{}
}

// MarkSIGINT records that this run received an operator SIGINT. The operation
// is idempotent and safe to call from the signal watcher while the session is
// being torn down on another goroutine.
func (i *SessionCancellationIntent) MarkSIGINT() {
	if i != nil {
		i.sigint.Store(true)
	}
}

// SIGINTReceived reports whether this run was cancelled by an observed SIGINT.
func (i *SessionCancellationIntent) SIGINTReceived() bool {
	return i != nil && i.sigint.Load()
}

// sessionSIGINTCancellationOnly reports whether every known cause in err is
// a consequence of stopping the run for SIGINT. It intentionally does not
// treat context.DeadlineExceeded as suppressible: a timeout can be an
// independent failure even when a signal is observed nearby.
func sessionSIGINTCancellationOnly(err error, intent *SessionCancellationIntent) bool {
	return intent != nil && intent.SIGINTReceived() && sessionSIGINTErrorOnly(err)
}

func sessionSIGINTErrorOnly(err error) bool {
	if err == nil {
		return true
	}

	// SessionAudioInputError includes a kind sentinel in its Unwrap result.
	// That sentinel describes the cancelled boundary, not an independent
	// failure; inspect its underlying error instead.
	if inputErr, ok := err.(*SessionAudioInputError); ok {
		if inputErr == nil || inputErr.Err == nil {
			return false
		}
		return sessionSIGINTErrorOnly(inputErr.Err)
	}

	if unwrapper, ok := err.(interface{ Unwrap() []error }); ok {
		causes := unwrapper.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !sessionSIGINTErrorOnly(cause) {
				return false
			}
		}
		return true
	}
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		return sessionSIGINTErrorOnly(unwrapper.Unwrap())
	}

	switch err {
	case context.Canceled,
		ErrSessionAudioResponseIncomplete,
		ErrSessionAudioInputEndOfTurnLost,
		ErrSessionScheduledAudioIncomplete,
		ErrSessionUnresolvedToolResults,
		ErrSessionToolContinuationIncomplete,
		ErrSessionImageContinuationIncomplete:
		return true
	default:
		return false
	}
}

// sessionSIGINTCleanForObserver adds the observer's typed stream failure
// state to the error-tree check. A provider ERROR or failure-shaped close is
// independent evidence and must survive an otherwise nearby SIGINT.
func sessionSIGINTCleanForObserver(err error, intent *SessionCancellationIntent, observer *sessionProgressObserver) bool {
	if !sessionSIGINTCancellationOnly(err, intent) {
		return false
	}
	return sessionSIGINTObserverFailureOnly(observer)
}

func sessionSIGINTObserverFailureOnly(observer *sessionProgressObserver) bool {
	failure := observer.failureSnapshot()
	if failure == nil {
		return true
	}
	// The model runner can publish a terminal cancellation ERROR while the
	// session context is being stopped. Its loop provenance and cancellation
	// reason are explicit evidence of the same signal-driven shutdown, not a
	// provider failure. Provider-authored cancellation-shaped failures remain
	// independent and are deliberately preserved.
	return failure.terminalReason == string(messages.TerminalReasonCancellation) &&
		failure.provenance == string(messages.TerminalProvenanceLoop)
}
