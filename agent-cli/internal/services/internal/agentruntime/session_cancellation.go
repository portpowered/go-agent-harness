package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
