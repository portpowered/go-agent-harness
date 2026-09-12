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
	r.toolTurn = false
	r.adoptUnscheduledContinuationIDLocked(id, purpose)
	return sessiondiagnostics.Observation{Accepted: true, NewResponse: true, ResponseID: id}
}

func (r *reducer) existingResponseOpenLocked(id string) (sessiondiagnostics.Observation, bool) {
	if id != "" {
		if _, ok := r.completedIDs[id]; ok {
			return sessiondiagnostics.Observation{ResponseID: id}, true
		}
		if _, ok := r.retiredIDs[id]; ok {
			return sessiondiagnostics.Observation{ResponseID: id}, true
		}
	}
	if !r.activeResponse {
		return sessiondiagnostics.Observation{}, false
	}
	if r.activeResponseID == id {
		return sessiondiagnostics.Observation{ResponseID: id}, true
	}
	if r.activeResponseID != "" && id == "" {
		return sessiondiagnostics.Observation{ResponseID: r.activeResponseID}, true
	}
	if r.activeResponseID != "" && id != "" {
		r.retiredIDs[r.activeResponseID] = struct{}{}
	}
	return sessiondiagnostics.Observation{}, false
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
	if _, ok := r.completedIDs[id]; ok {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if _, ok := r.retiredIDs[id]; ok {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if r.activeScheduledSet && !r.bindScheduledIDLocked(r.activeScheduledIndex, id).Accepted {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	r.activeResponseID = id
	if r.activeScheduledSet {
		r.setScheduledOwnerLocked(r.activeScheduledIndex, id)
	}
	for callID, state := range r.continuations {
		if state.ResultAccepted && state.ContinuationRequested && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" {
			state.ContinuationResponseID = id
			r.continuations[callID] = state
		}
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
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
		return !completed && !retired
	}
	if id != "" {
		_, completed := r.completedIDs[id]
		_, retired := r.retiredIDs[id]
		return !completed && !retired
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
	if id == "" {
		id = r.activeResponseID
	}
	if id != "" {
		r.completedIDs[id] = struct{}{}
	}
	r.activeResponse = false
	r.activeResponseID = ""
	if r.activeScheduledSet {
		r.clearActiveOwnerLocked(r.activeScheduledIndex, id)
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
}
