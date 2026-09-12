package service

import (
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

func (r *reducer) ensureScheduledLocked(count int) sessiondiagnostics.Observation {
	if count < 0 {
		return sessiondiagnostics.Observation{}
	}
	for len(r.scheduled) < count {
		r.scheduled = append(r.scheduled, sessiondiagnostics.ScheduledState{Disposition: sessiondiagnostics.DispositionPending})
	}
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) bindBoundaryLocked(id string) sessiondiagnostics.Observation {
	if index, ok := r.pendingRetryIndexLocked(); ok {
		return r.bindRetryLocked(index, id)
	}
	if index, ok := r.pendingContinuationIndexLocked(); ok {
		observation := r.bindContinuationLocked(index, id)
		if observation.Accepted {
			r.bindPendingContinuationsLocked(index, id)
		}
		return observation
	}
	return r.bindNextLocked(id)
}

func (r *reducer) bindTerminalOnlyLocked(id string) sessiondiagnostics.Observation {
	if index, ok := r.pendingRetryIndexLocked(); ok {
		if !r.activeResponse {
			r.activeResponse = true
			r.activeResponseID = ""
			r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
			r.messageEndSeen = false
			r.toolTurn = false
		}
		return r.bindRetryLocked(index, id)
	}
	return r.bindNextLocked(id)
}

func (r *reducer) bindScheduledIDLocked(index int, rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{}
	}
	if id != "" {
		if existing, ok := r.scheduledResponseByID[id]; ok && existing != index {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		if !contains(r.scheduled[index].ResponseIDs, id) {
			r.scheduled[index].ResponseIDs = append(r.scheduled[index].ResponseIDs, id)
			sort.Strings(r.scheduled[index].ResponseIDs)
		}
		r.scheduledResponseByID[id] = index
	}
	r.scheduled[index].Bound = true
	if r.scheduled[index].Disposition == "" {
		r.scheduled[index].Disposition = sessiondiagnostics.DispositionPending
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) setScheduledOwnerLocked(index int, rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if id != "" {
		if existing, ok := r.scheduledResponseByID[id]; ok && existing != index {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		r.scheduledResponseByID[id] = index
		if !contains(r.scheduled[index].ResponseIDs, id) {
			r.scheduled[index].ResponseIDs = append(r.scheduled[index].ResponseIDs, id)
			sort.Strings(r.scheduled[index].ResponseIDs)
		}
	}
	r.activeScheduledIndex = index
	r.activeScheduledID = id
	r.activeScheduledSet = true
	r.logicalScheduledIndex = index
	r.logicalScheduledID = id
	r.logicalScheduledSet = true
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) bindNextLocked(rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if id != "" {
		if index, ok := r.scheduledResponseByID[id]; ok {
			return r.setScheduledOwnerLocked(index, id)
		}
	}
	for r.nextScheduledResponse < len(r.scheduled) && r.scheduled[r.nextScheduledResponse].Bound {
		r.nextScheduledResponse++
	}
	if r.nextScheduledResponse >= len(r.scheduled) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	index := r.nextScheduledResponse
	bound := r.bindScheduledIDLocked(index, id)
	if !bound.Accepted {
		return bound
	}
	owner := r.setScheduledOwnerLocked(index, id)
	if !owner.Accepted {
		return owner
	}
	r.nextScheduledResponse++
	return owner
}

func (r *reducer) bindContinuationLocked(index int, id string) sessiondiagnostics.Observation {
	if index < 0 || index >= len(r.scheduled) {
		if r.logicalScheduledSet {
			index = r.logicalScheduledIndex
		} else {
			return sessiondiagnostics.Observation{}
		}
	}
	if !r.bindScheduledIDLocked(index, id).Accepted {
		return sessiondiagnostics.Observation{ResponseID: strings.TrimSpace(id)}
	}
	return r.setScheduledOwnerLocked(index, id)
}

func (r *reducer) bindPendingContinuationsLocked(index int, id string) {
	id = strings.TrimSpace(id)
	for callID, state := range r.continuations {
		// The provider can enqueue the continuation response synchronously from
		// response.create, before the host records the explicit request event.
		// Ownership is safe once the accepted provider call and its tool response
		// are complete; completion/admission still requires ContinuationRequested.
		if !state.ResultAccepted || !state.ProviderCallObserved || !state.ToolResponseComplete || state.ContinuationResponseID != "" {
			continue
		}
		owner, ok := r.scheduledIndexForLocked(state.ResponseID)
		if !ok || owner != index {
			continue
		}
		state.ContinuationScheduledIndex = index
		state.ContinuationScheduledSet = true
		if id != "" {
			state.ContinuationResponseID = id
		}
		r.continuations[callID] = state
	}
}

func (r *reducer) bindRetryLocked(index int, id string) sessiondiagnostics.Observation {
	if index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || !r.scheduled[index].RetryPending {
		return sessiondiagnostics.Observation{ResponseID: strings.TrimSpace(id)}
	}
	owner := r.bindScheduledIDLocked(index, id)
	if !owner.Accepted {
		return owner
	}
	owner = r.setScheduledOwnerLocked(index, id)
	if !owner.Accepted {
		return owner
	}
	r.scheduled[index].RetryPending = false
	r.scheduled[index].TerminalFailure = false
	r.scheduled[index].TerminalStatus = ""
	r.scheduled[index].TerminalErrorCode = ""
	r.scheduled[index].TerminalStatusDetails = ""
	for callID, state := range r.continuations {
		if !state.ContinuationScheduledSet || state.ContinuationScheduledIndex != index {
			continue
		}
		state.ContinuationResponseID = strings.TrimSpace(id)
		clearContinuationTerminal(&state)
		r.continuations[callID] = state
	}
	return owner
}

func (r *reducer) pendingRetryIndexLocked() (int, bool) {
	for index := range r.scheduled {
		if r.scheduled[index].Bound && r.scheduled[index].RetryPending {
			return index, true
		}
	}
	return 0, false
}

func (r *reducer) pendingContinuationIndexLocked() (int, bool) {
	index := -1
	for _, state := range r.continuations {
		if !state.ResultAccepted || !state.ProviderCallObserved || !state.ToolResponseComplete || state.ContinuationResponseID != "" {
			continue
		}
		owner, ok := r.scheduledIndexForLocked(state.ResponseID)
		if !ok || (index >= 0 && owner >= index) {
			continue
		}
		index = owner
	}
	return index, index >= 0
}

func (r *reducer) scheduledIndexForLocked(rawID string) (int, bool) {
	id := strings.TrimSpace(rawID)
	if id != "" {
		index, ok := r.scheduledResponseByID[id]
		return index, ok && index >= 0 && index < len(r.scheduled)
	}
	if r.logicalScheduledSet {
		return r.logicalScheduledIndex, true
	}
	if r.activeScheduledSet {
		return r.activeScheduledIndex, true
	}
	return 0, false
}

func (r *reducer) noteScheduledTerminalLocked(rawID string, terminal *sessiondiagnostics.Terminal) sessiondiagnostics.Observation {
	if terminal == nil || !providerFailure(terminal) {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok || index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || r.scheduled[index].Disposition != sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}
	}
	r.scheduled[index].TerminalFailure = true
	r.scheduled[index].TerminalStatus = normalize(terminal.Status)
	r.scheduled[index].TerminalErrorCode = sanitize(providerErrorCode(terminal))
	r.scheduled[index].TerminalStatusDetails = sanitize(terminal.StatusDetails)
	if r.scheduled[index].TerminalStatusDetails == "" {
		r.scheduled[index].TerminalStatusDetails = sanitize(providerErrorMessage(terminal))
	}
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) rememberRetryLocked(responseID, lifecycleID string, terminal *sessiondiagnostics.Terminal) sessiondiagnostics.Observation {
	r.retryCandidateSet = false
	r.retryCandidateID = ""
	delay, eligible := retryDecision(terminal)
	if !eligible {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(lifecycleID)
	if !ok {
		return sessiondiagnostics.Observation{}
	}
	r.retryCandidateIndex = index
	r.retryCandidateSet = true
	r.retryCandidateID = strings.TrimSpace(responseID)
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Delay: delay}}
}

func (r *reducer) claimRetryLocked(rawID string, terminal *sessiondiagnostics.Terminal) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	delay, eligible := retryDecision(terminal)
	if !eligible {
		return sessiondiagnostics.Observation{}, 0, false, nil
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok && r.retryCandidateSet && strings.TrimSpace(rawID) == r.retryCandidateID {
		index, ok = r.retryCandidateIndex, true
	}
	if !ok || index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{}, 0, false, nil
	}
	lifecycle := &r.scheduled[index]
	if !lifecycle.Bound || lifecycle.Disposition != sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}, 0, false, nil
	}
	if lifecycle.RetryUsed {
		return sessiondiagnostics.Observation{ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Exhausted: true}}, 0, false, sessiondiagnostics.ErrRetryExhausted
	}
	lifecycle.RetryUsed = true
	lifecycle.RetryPending = true
	lifecycle.TerminalFailure = false
	lifecycle.TerminalStatus = ""
	lifecycle.TerminalErrorCode = ""
	lifecycle.TerminalStatusDetails = ""
	r.retryCandidateSet = false
	r.retryCandidateID = ""
	for callID, state := range r.continuations {
		if state.ContinuationScheduledSet && state.ContinuationScheduledIndex == index {
			clearContinuationTerminal(&state)
			r.continuations[callID] = state
		}
	}
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Accepted: true, Delay: delay}}, delay, true, nil
}

func (r *reducer) noteDispositionLocked(rawID string, disposition sessiondiagnostics.Disposition) sessiondiagnostics.Observation {
	if disposition == sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok {
		if strings.TrimSpace(rawID) != "" || !r.canBindUnidentifiedLocked(rawID) {
			return sessiondiagnostics.Observation{}
		}
		bound := r.bindNextLocked(rawID)
		if !bound.Accepted {
			return bound
		}
		index, ok = r.scheduledIndexForLocked(rawID)
	}
	if !ok || index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || !r.ownerMatchesLocked(index, rawID) {
		return sessiondiagnostics.Observation{}
	}
	lifecycle := &r.scheduled[index]
	if lifecycle.Disposition != sessiondiagnostics.DispositionPending {
		r.clearScheduledOwnerLocked(index, rawID)
		return sessiondiagnostics.Observation{ScheduledIndex: index, HasScheduledIndex: true, Disposition: lifecycle.Disposition}
	}
	lifecycle.Disposition = disposition
	lifecycle.RetryPending = false
	lifecycle.RetryUsed = false
	lifecycle.TerminalFailure = false
	lifecycle.TerminalStatus = ""
	lifecycle.TerminalErrorCode = ""
	lifecycle.TerminalStatusDetails = ""
	r.clearScheduledOwnerLocked(index, rawID)
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Disposition: disposition}
}

func (r *reducer) canBindUnidentifiedLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return true
	}
	if r.activeScheduledSet && r.activeScheduledID != "" && r.activeScheduledID != id {
		return false
	}
	return !r.logicalScheduledSet || r.logicalScheduledID == "" || r.logicalScheduledID == id
}

func (r *reducer) ownerMatchesLocked(index int, rawID string) bool {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return r.activeScheduledSet && r.activeScheduledIndex == index && r.activeScheduledID == ""
	}
	if r.activeScheduledSet && r.activeScheduledIndex == index {
		return r.activeScheduledID == id
	}
	if r.logicalScheduledSet && r.logicalScheduledIndex == index {
		return r.logicalScheduledID == id
	}
	return true
}

func (r *reducer) clearScheduledOwnerLocked(index int, rawID string) {
	id := strings.TrimSpace(rawID)
	r.clearActiveOwnerLocked(index, id)
	if r.logicalScheduledSet && r.logicalScheduledIndex == index && r.logicalScheduledID == id {
		r.logicalScheduledSet = false
		r.logicalScheduledID = ""
	}
}

func (r *reducer) clearActiveOwnerLocked(index int, rawID string) {
	id := strings.TrimSpace(rawID)
	if r.activeScheduledSet && r.activeScheduledIndex == index && r.activeScheduledID == id {
		r.activeScheduledSet = false
		r.activeScheduledID = ""
	}
}
