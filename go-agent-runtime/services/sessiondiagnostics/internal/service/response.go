package service

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

func (r *reducer) openResponseLocked(rawID string, purpose sessiondiagnostics.ResponsePurpose) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if observation, handled := r.existingResponseOpenLocked(id); handled {
		return observation
	}
	r.activeResponse = true
	r.activeResponseID = id
	r.activePurpose = purpose
	r.messageEndSeen = false
	r.responseContentSeen = false
	r.toolTurn = false
	r.adoptUnscheduledContinuationIDLocked(id, purpose)
	return sessiondiagnostics.Observation{Accepted: true, NewResponse: true, ResponseID: id}
}

func (r *reducer) existingResponseOpenLocked(id string) (sessiondiagnostics.Observation, bool) {
	if r.responseIDKnownLocked(id) {
		return sessiondiagnostics.Observation{ResponseID: id}, true
	}
	if !r.activeResponse {
		return sessiondiagnostics.Observation{}, false
	}
	return r.activeResponseOpenLocked(id)
}

func (r *reducer) responseIDKnownLocked(id string) bool {
	if id == "" {
		return false
	}
	_, completed := r.completedIDs[id]
	_, retired := r.retiredIDs[id]
	_, stale := r.staleIDs[id]
	return completed || retired || stale
}

func (r *reducer) activeResponseOpenLocked(id string) (sessiondiagnostics.Observation, bool) {
	if r.activeResponseID == id {
		return sessiondiagnostics.Observation{ResponseID: id}, true
	}
	if r.activeResponseID != "" && id == "" {
		return sessiondiagnostics.Observation{ResponseID: r.activeResponseID}, true
	}
	if r.activeResponseID == "" && id != "" {
		// A provider may expose an untagged response boundary before its late
		// response ID. Treat the tagged open as adoption of that same active
		// lifecycle, not as a replacement response.
		return r.adoptResponseLocked(id), true
	}
	if !r.canReplaceActiveResponseLocked() {
		// A foreign open cannot steal an active response. The current
		// response must first cross its explicit terminal/continuation
		// boundary (or be finished by the host).
		return sessiondiagnostics.Observation{ResponseID: id}, true
	}
	r.retiredIDs[r.activeResponseID] = struct{}{}
	return sessiondiagnostics.Observation{}, false
}

func (r *reducer) canReplaceActiveResponseLocked() bool {
	if r.messageEndSeen {
		return true
	}
	// Active-response scheduling may dispatch the next input before the prior
	// response reaches MESSAGE.END. The newly appended, still-unbound slot is
	// an explicit host boundary and permits that response to replace the
	// provisional active response. A merely allocated schedule without an
	// already-bound active owner is not enough to guess ownership.
	if r.activeScheduledSet && r.hasPendingScheduledBoundaryLocked() {
		return true
	}
	if _, ok := r.pendingContinuationIndexLocked(); ok {
		return true
	}
	for _, state := range r.continuations {
		if state.ResultAccepted && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" && !state.ContinuationComplete {
			return true
		}
	}
	_, ok := r.pendingRetryIndexLocked()
	return ok
}

func (r *reducer) adoptUnscheduledContinuationIDLocked(id string, purpose sessiondiagnostics.ResponsePurpose) {
	if id == "" || purpose == sessiondiagnostics.ResponsePurposeToolAcknowledgement {
		return
	}
	// A continuation response can arrive before the adapter has bound the
	// preceding tool response to a scheduled slot. The accepted result and
	// completed tool response are still an unambiguous ownership chain, so the
	// new response ID must be recorded on that continuation even when scheduled
	// state exists elsewhere in the reducer.
	for callID, state := range r.continuations {
		if state.ResultAccepted && state.ContinuationRequested && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" {
			if state.ContinuationScheduledSet {
				continue
			}
			if _, scheduled := r.scheduledIndexForLocked(state.ResponseID); scheduled {
				continue
			}
			if _, pendingSlot := r.nextUnboundScheduledIndexLocked(); pendingSlot {
				continue
			}
			state.ContinuationResponseID = id
			r.continuations[callID] = state
		}
	}
}

func (r *reducer) adoptResponseLocked(rawID string) sessiondiagnostics.Observation {
	if !r.activeResponse || r.activeResponseID != "" {
		return sessiondiagnostics.Observation{Accepted: true, ResponseID: r.activeResponseID}
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return sessiondiagnostics.Observation{Accepted: true}
	}
	if r.responseIDKnownLocked(id) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if r.activeScheduledSet && !r.bindScheduledIDLocked(r.activeScheduledIndex, id).Accepted {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	r.activeResponseID = id
	if r.activeScheduledSet {
		r.setScheduledOwnerLocked(r.activeScheduledIndex, id)
	}
	r.adoptContinuationResponseIDLocked(id)
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
}

func (r *reducer) adoptContinuationResponseIDLocked(id string) {
	for callID, state := range r.continuations {
		if state.ResultAccepted && state.ContinuationRequested && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" {
			state.ContinuationResponseID = id
			r.continuations[callID] = state
		}
	}
}

func (r *reducer) responseBelongsLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if !r.activeResponse {
		return id == ""
	}
	if r.activeResponseID != "" {
		return id == "" || id == r.activeResponseID
	}
	return id == ""
}

// responseContentBelongsLocked keeps content in the active, still-open
// response generation. It intentionally rejects content after the terminal
// marker; a host that observed a new generation must submit the explicit
// content-boundary event after validating its response ownership.
func (r *reducer) responseContentBelongsLocked(rawID string) bool {
	if !r.activeResponse {
		return false
	}
	if r.messageEndSeen {
		return false
	}
	id := strings.TrimSpace(rawID)
	if r.activeResponseID != "" {
		return id == r.activeResponseID || id == ""
	}
	return id == ""
}

func (r *reducer) responseGenerationBoundaryBelongsLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if !r.activeResponse {
		// A legacy provider can expose a tool-call response without a response
		// ID. Its first terminal leaves the reducer between generations while
		// continuation ownership is still pending; the adapter has already
		// validated the untagged stream boundary before submitting this event.
		return id == "" && r.messageEndSeen
	}
	if r.activeResponseID != "" {
		return id == r.activeResponseID
	}
	return id == ""
}

func (r *reducer) ownsResponseEndLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if r.activeResponse {
		if r.activeResponseID != "" {
			return id == "" || id == r.activeResponseID
		}
		if id == "" {
			return true
		}
		_, completed := r.completedIDs[id]
		_, retired := r.retiredIDs[id]
		_, stale := r.staleIDs[id]
		return !completed && !retired && !stale
	}
	if id != "" {
		_, completed := r.completedIDs[id]
		_, retired := r.retiredIDs[id]
		_, stale := r.staleIDs[id]
		return !completed && !retired && !stale
	}
	if !r.messageEndSeen {
		return true
	}
	for _, state := range r.continuations {
		if state.ProviderCallObserved && !state.ContinuationComplete {
			return true
		}
	}
	return false
}

func (r *reducer) finishResponseLocked(rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if !r.activeResponse || !r.ownsResponseEndLocked(id) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if r.activeResponseID == "" && id != "" {
		adopted := r.adoptResponseLocked(id)
		if !adopted.Accepted {
			return adopted
		}
	}
	if id == "" {
		id = r.activeResponseID
	}
	if id != "" {
		r.completedIDs[id] = struct{}{}
	}
	r.activeResponse = false
	r.activeResponseID = ""
	r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
	r.messageEndSeen = false
	r.responseContentSeen = false
	r.toolTurn = false
	if r.activeScheduledSet {
		r.clearActiveOwnerLocked(r.activeScheduledIndex, id)
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
}
