package service

import (
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

func (r *reducer) applyLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
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
	case sessiondiagnostics.EventSyncLegacy:
		return r.syncLegacyLocked(event.Legacy), nil, 0, false
	case sessiondiagnostics.EventReset:
		r.resetLocked()
		return sessiondiagnostics.Observation{Accepted: true}, nil, 0, false
	default:
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
}

func (r *reducer) applyResponseLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	switch event.Kind {
	case sessiondiagnostics.EventResponseOpen:
		return r.openResponseLocked(event.ResponseID, event.Purpose), nil, 0, false
	case sessiondiagnostics.EventResponseAdopt:
		return r.adoptResponseLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventResponseBelongs:
		belongs := r.responseBelongsLocked(event.ResponseID)
		return sessiondiagnostics.Observation{Accepted: belongs, OwnsResponse: belongs}, nil, 0, false
	case sessiondiagnostics.EventResponseOwnsEnd:
		owns := r.ownsResponseEndLocked(event.ResponseID)
		return sessiondiagnostics.Observation{Accepted: owns, OwnsResponse: owns}, nil, 0, false
	case sessiondiagnostics.EventResponseContent:
		r.messageEndSeen = false
		return sessiondiagnostics.Observation{Accepted: true}, nil, 0, false
	case sessiondiagnostics.EventResponseEnd:
		return r.endResponseLocked(event)
	case sessiondiagnostics.EventResponseFinish:
		return r.finishResponseLocked(event.ResponseID), nil, 0, false
	default:
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
}

func (r *reducer) applyScheduledLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	switch event.Kind {
	case sessiondiagnostics.EventBindScheduledBoundary:
		return r.bindBoundaryLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventBindScheduledTerminalOnly:
		return r.bindTerminalOnlyLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventBindScheduledID:
		return r.bindScheduledIDLocked(event.Index, event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventSetScheduledOwner:
		return r.setScheduledOwnerLocked(event.Index, event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventEnsureScheduled:
		return r.ensureScheduledLocked(event.Count), nil, 0, false
	case sessiondiagnostics.EventNoteScheduledTerminal:
		return r.noteScheduledTerminalLocked(event.ResponseID, event.Terminal), nil, 0, false
	case sessiondiagnostics.EventRememberRetry:
		lifecycleID := event.LifecycleID
		if strings.TrimSpace(lifecycleID) == "" {
			lifecycleID = event.ResponseID
		}
		return r.rememberRetryLocked(event.ResponseID, lifecycleID, event.Terminal), nil, 0, false
	case sessiondiagnostics.EventClaimRetry:
		return r.claimRetryLocked(event.ResponseID, event.Terminal)
	case sessiondiagnostics.EventScheduledDisposition:
		return r.noteDispositionLocked(event.ResponseID, event.Disposition), nil, 0, false
	default:
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
}

func (r *reducer) applyToolLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	switch event.Kind {
	case sessiondiagnostics.EventToolCall:
		return r.toolCallLocked(event), nil, 0, false
	case sessiondiagnostics.EventToolResultAccepted:
		return r.toolResultAcceptedLocked(event.CallID), nil, 0, false
	case sessiondiagnostics.EventContinuationRequested:
		return r.continuationRequestedLocked(event.CallID)
	default:
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
}
