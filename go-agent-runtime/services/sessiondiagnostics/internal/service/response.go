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
	if id == "" || purpose == sessiondiagnostics.ResponsePurposeToolAcknowledgement || len(r.scheduled) != 0 {
		return
	}
	for callID, state := range r.continuations {
		if state.ResultAccepted && state.ContinuationRequested && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" {
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
