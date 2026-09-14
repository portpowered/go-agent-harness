package observer

import (
	"context"
	"errors"
	"sort"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// sessionSIGINTCancellationOnly reports whether every known cause in err is
// a consequence of stopping the run for SIGINT. It intentionally does not
// treat context.DeadlineExceeded as suppressible: a timeout can be an
// independent failure even when a signal is observed nearby.
func sessionSIGINTCancellationOnly(err error, intent *sessiontrace.CancellationIntent) bool {
	return intent != nil && intent.SIGINTReceived() && sessionSIGINTErrorOnly(err)
}

func sessionSIGINTErrorOnly(err error) bool {
	if err == nil {
		return true
	}

	// SessionAudioInputError includes a kind sentinel in its Unwrap result.
	// That sentinel describes the cancelled boundary, not an independent
	// failure; inspect its underlying error instead.
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

	return errors.Is(err, context.Canceled) ||
		errors.Is(err, session.ErrLiveAudioResponseIncomplete) ||
		errors.Is(err, session.ErrLiveScheduledAudioIncomplete) ||
		errors.Is(err, session.ErrLiveUnresolvedToolResults) ||
		errors.Is(err, session.ErrLiveToolContinuationIncomplete) ||
		errors.Is(err, session.ErrLiveImageContinuationIncomplete)
}

// sessionSIGINTCleanForObserver adds the observer's typed stream failure
// state to the error-tree check. A provider ERROR or failure-shaped close is
// independent evidence and must survive an otherwise nearby SIGINT.
func sessionSIGINTCleanForObserver(err error, intent *sessiontrace.CancellationIntent, observer *observerState) bool {
	if !sessionSIGINTCancellationOnly(err, intent) {
		return false
	}
	return sessionSIGINTObserverFailureOnly(observer)
}

func sessionSIGINTObserverFailureOnly(observer *observerState) bool {
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

func (o *observerState) pendingImageContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil, nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range states {
		if state.ToolName != tools.ReadImageToolID || !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		ids = append(ids, state.CallID)
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	sort.Strings(ids)
	return ids, statuses, codes, details
}

func (o *observerState) pendingNonImageToolContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil, nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range states {
		if state.ToolName == tools.ReadImageToolID || !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		ids = append(ids, state.CallID)
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	sort.Strings(ids)
	return ids, statuses, codes, details
}

func (o *observerState) pendingContinuationMetadata() (map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil
	}
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range o.lifecycleContinuationStates() {
		if !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	return statuses, codes, details
}
