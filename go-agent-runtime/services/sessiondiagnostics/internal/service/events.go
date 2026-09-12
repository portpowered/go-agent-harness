package service

import (
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

func (r *reducer) applyLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	switch event.Kind {
	case sessiondiagnostics.EventResponseOpen,
		sessiondiagnostics.EventResponseAdopt,
		sessiondiagnostics.EventResponseBelongs,
		sessiondiagnostics.EventResponseOwnsEnd,
		sessiondiagnostics.EventResponseContent,
		sessiondiagnostics.EventResponseEnd,
		sessiondiagnostics.EventResponseFinish:
		return r.applyResponseLocked(event)
	case sessiondiagnostics.EventBindScheduledBoundary,
		sessiondiagnostics.EventBindScheduledTerminalOnly,
		sessiondiagnostics.EventBindScheduledID,
		sessiondiagnostics.EventSetScheduledOwner,
		sessiondiagnostics.EventEnsureScheduled,
		sessiondiagnostics.EventNoteScheduledTerminal,
		sessiondiagnostics.EventRememberRetry,
		sessiondiagnostics.EventClaimRetry,
		sessiondiagnostics.EventScheduledDisposition:
		return r.applyScheduledLocked(event)
	case sessiondiagnostics.EventToolCall,
		sessiondiagnostics.EventToolResultAccepted,
		sessiondiagnostics.EventContinuationRequested:
		return r.applyToolLocked(event)
	case sessiondiagnostics.EventReset:
		r.resetLocked()
		return sessiondiagnostics.Observation{Accepted: true}, 0, false, nil
	default:
		return sessiondiagnostics.Observation{}, 0, false, sessiondiagnostics.ErrMalformedSequence
	}
}

func (r *reducer) applyResponseLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	//nolint:exhaustive // this dispatcher receives only response events.
	switch event.Kind {
	case sessiondiagnostics.EventResponseOpen:
		return r.openResponseLocked(event.ResponseID, event.Purpose), 0, false, nil
	case sessiondiagnostics.EventResponseAdopt:
		return r.adoptResponseLocked(event.ResponseID), 0, false, nil
	case sessiondiagnostics.EventResponseBelongs:
		belongs := r.responseBelongsLocked(event.ResponseID)
		return sessiondiagnostics.Observation{Accepted: belongs, OwnsResponse: belongs}, 0, false, nil
	case sessiondiagnostics.EventResponseOwnsEnd:
		owns := r.ownsResponseEndLocked(event.ResponseID)
		return sessiondiagnostics.Observation{Accepted: owns, OwnsResponse: owns}, 0, false, nil
	case sessiondiagnostics.EventResponseContent:
		r.messageEndSeen = false
		return sessiondiagnostics.Observation{Accepted: true}, 0, false, nil
	case sessiondiagnostics.EventResponseEnd:
		return r.endResponseLocked(event)
	case sessiondiagnostics.EventResponseFinish:
		return r.finishResponseLocked(event.ResponseID), 0, false, nil
	default:
		return sessiondiagnostics.Observation{}, 0, false, sessiondiagnostics.ErrMalformedSequence
	}
}

func (r *reducer) applyScheduledLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	//nolint:exhaustive // this dispatcher receives only scheduled events.
	switch event.Kind {
	case sessiondiagnostics.EventBindScheduledBoundary:
		return r.bindBoundaryLocked(event.ResponseID), 0, false, nil
	case sessiondiagnostics.EventBindScheduledTerminalOnly:
		return r.bindTerminalOnlyLocked(event.ResponseID), 0, false, nil
	case sessiondiagnostics.EventBindScheduledID:
		return r.bindScheduledIDLocked(event.Index, event.ResponseID), 0, false, nil
	case sessiondiagnostics.EventSetScheduledOwner:
		return r.setScheduledOwnerLocked(event.Index, event.ResponseID), 0, false, nil
	case sessiondiagnostics.EventEnsureScheduled:
		return r.ensureScheduledLocked(event.Count), 0, false, nil
	case sessiondiagnostics.EventNoteScheduledTerminal:
		return r.noteScheduledTerminalLocked(event.ResponseID, event.Terminal), 0, false, nil
	case sessiondiagnostics.EventRememberRetry:
		lifecycleID := event.LifecycleID
		if strings.TrimSpace(lifecycleID) == "" {
			lifecycleID = event.ResponseID
		}
		return r.rememberRetryLocked(event.ResponseID, lifecycleID, event.Terminal), 0, false, nil
	case sessiondiagnostics.EventClaimRetry:
		return r.claimRetryLocked(event.ResponseID, event.Terminal)
	case sessiondiagnostics.EventScheduledDisposition:
		observation, err := r.noteDispositionLocked(event.ResponseID, event.Disposition)
		return observation, 0, false, err
	default:
		return sessiondiagnostics.Observation{}, 0, false, sessiondiagnostics.ErrMalformedSequence
	}
}

func (r *reducer) applyToolLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	//nolint:exhaustive // this dispatcher receives only tool events.
	switch event.Kind {
	case sessiondiagnostics.EventToolCall:
		return r.toolCallLocked(event), 0, false, nil
	case sessiondiagnostics.EventToolResultAccepted:
		return r.toolResultAcceptedLocked(event.CallID), 0, false, nil
	case sessiondiagnostics.EventContinuationRequested:
		return r.continuationRequestedLocked(event.CallID)
	default:
		return sessiondiagnostics.Observation{}, 0, false, sessiondiagnostics.ErrMalformedSequence
	}
}
