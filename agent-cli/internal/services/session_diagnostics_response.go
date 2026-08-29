package services

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (o *sessionProgressObserver) resetObservedResponseState() {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.resetResponseOutputLocked()
	o.assistantResponseDone = false
	o.assistantOutputObserved = false
	o.toolCallInTurn = false
	o.messageEndSeen = false
	o.toolStateMu.Unlock()
	o.toolDeltaSeen = false
}

// beginObservedResponse changes the active response owner only when the
// provider has opened a new response. An untagged event cannot replace an
// identified response because it carries no proof of ownership.
func (o *sessionProgressObserver) beginObservedResponse(id string) bool {
	if o == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if o.completedResponseIDs == nil {
		o.completedResponseIDs = make(map[string]struct{})
	}
	if o.retiredResponseIDs == nil {
		o.retiredResponseIDs = make(map[string]struct{})
	}
	if id != "" {
		if _, completed := o.completedResponseIDs[id]; completed {
			return false
		}
		if _, retired := o.retiredResponseIDs[id]; retired {
			return false
		}
	}
	if o.activeResponse {
		if o.activeResponseID == id {
			return false
		}
		if o.activeResponseID != "" && id == "" {
			return false
		}
		if o.activeResponseID != "" && id != "" {
			o.retiredResponseIDs[o.activeResponseID] = struct{}{}
		}
	}
	o.activeResponse = true
	o.activeResponseID = id
	o.resetObservedResponseState()
	if id != "" {
		o.toolStateMu.Lock()
		o.ensureToolStateLocked()
		for _, state := range o.toolContinuations {
			if state != nil && state.resultAccepted && state.continuationRequested && state.providerCallObserved && state.toolResponseComplete && state.continuationResponseID == "" {
				state.continuationResponseID = id
			}
		}
		o.toolStateMu.Unlock()
	}
	return true
}

func (o *sessionProgressObserver) ownsObservedResponseEnd(id string) bool {
	if o == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if o.activeResponse {
		if o.activeResponseID != "" {
			// Older compatible transports omit the response ID on the terminal
			// event. With no competing identified owner, that terminal still
			// belongs to the active response; a non-empty wrong ID never does.
			return id == "" || id == o.activeResponseID
		}
		return id == ""
	}
	if id != "" {
		if o.completedResponseIDs == nil {
			o.completedResponseIDs = make(map[string]struct{})
		}
		_, completed := o.completedResponseIDs[id]
		return !completed
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	if !o.messageEndSeen {
		return true
	}
	// Tool-call responses may have a terminal boundary before their accepted
	// result and continuation. Those following untagged boundaries are still
	// part of the same legacy lifecycle; only a completed continuation makes a
	// later duplicate terminal ignorable.
	for _, state := range o.toolContinuations {
		if state != nil && state.providerCallObserved && !state.continuationComplete {
			return true
		}
	}
	return false
}

func (o *sessionProgressObserver) finishObservedResponse(id string) {
	if o == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id != "" {
		if o.completedResponseIDs == nil {
			o.completedResponseIDs = make(map[string]struct{})
		}
		o.completedResponseIDs[id] = struct{}{}
	}
	o.activeResponse = false
	o.activeResponseID = ""
}

func (o *sessionProgressObserver) responseEventBelongsToActive(id string) bool {
	if o == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if !o.activeResponse {
		return id == ""
	}
	if o.activeResponseID != "" {
		// Compatible providers may omit response_id on individual content or
		// tool events. In that case the only active identified response owns
		// the event; a non-empty different ID is still rejected.
		return id == "" || id == o.activeResponseID
	}
	return id == ""
}

func (o *sessionProgressObserver) observeProviderToolCallStartForResponse(callID, name, responseID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	if !o.toolResultsEnabled {
		return
	}
	_, accepted := o.acceptedToolCalls[callID]
	state := o.toolContinuations[callID]
	if state == nil {
		state = &toolContinuationState{}
		o.toolContinuations[callID] = state
	}
	if strings.TrimSpace(name) != "" {
		state.toolName = name
	}
	if strings.TrimSpace(responseID) != "" {
		state.responseID = strings.TrimSpace(responseID)
	}
	state.providerCallObserved = true
	state.resultAccepted = accepted
	o.providerToolCallSeen = true
	if accepted {
		return
	}
	o.unresolvedToolCalls[callID] = struct{}{}
}

func (o *sessionProgressObserver) observeProviderToolCallWithIDForResponse(callID, name, responseID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.observeProviderToolCallStartForResponse(callID, name, responseID)
}

func continuationStateBelongsToResponse(state *toolContinuationState, responseID string) bool {
	if state == nil {
		return false
	}
	owner := state.responseID
	if state.continuationResponseID != "" {
		owner = state.continuationResponseID
	}
	return owner == responseID
}

// observeProviderMessageEndForResponse applies terminal accounting only to
// tool state owned by responseID. This prevents a late terminal from an older
// response from completing a replacement response or its continuation.
func (o *sessionProgressObserver) observeProviderMessageEndForResponse(role messages.Role, terminal *messages.MessageEndValue, responseID string, outputPresent bool) bool {
	if o == nil {
		return false
	}
	responseID = strings.TrimSpace(responseID)
	o.toolStateMu.Lock()
	toolTurn := o.toolCallInTurn
	duplicateEnd := o.messageEndSeen
	o.messageEndSeen = true
	continuationChanged := false
	if toolTurn {
		for _, state := range o.toolContinuations {
			if continuationStateBelongsToResponse(state, responseID) && state.providerCallObserved && !state.toolResponseComplete {
				state.toolResponseComplete = true
			}
		}
	} else if role != messages.RoleTool {
		for _, state := range o.toolContinuations {
			if !continuationStateBelongsToResponse(state, responseID) || !state.toolResponseComplete || state.continuationComplete {
				continue
			}
			if duplicateEnd && !state.continuationRequested {
				// A second MESSAGE.END before the accepted result's explicit
				// response request is still a duplicate of the provider's
				// function-call response, not evidence of an empty continuation.
				continue
			}
			if !state.continuationTerminalSeen {
				state.continuationTerminalSeen = true
				if terminal != nil {
					state.continuationStatus = normalizeContinuationStatus(terminal.Status)
					state.continuationStatusDetails = sanitizeContinuationDetail(terminal.StatusDetails)
					state.continuationTerminalReason = terminal.TerminalReason
				}
				state.continuationOutputObserved = o.assistantOutputObserved
				status := normalizeContinuationStatus(state.continuationStatus)
				reason := state.continuationTerminalReason
				terminalFailed := !state.continuationOutputObserved || (status != "" && status != "completed") || (reason != "" && reason != messages.TerminalReasonProviderAuthoredCompletion && reason != messages.TerminalReasonLoopSynthesizedCompletion)
				if terminalFailed && state.continuationStatusDetails == "" && reason != "" && !state.continuationOutputObserved {
					state.continuationStatusDetails = "assistant continuation produced no observable output"
				}
				if terminalFailed && state.continuationStatusDetails == "" && reason != "" {
					state.continuationStatusDetails = "terminal reason=" + string(reason)
				}
				if terminalFailed {
					state.continuationFailureObserved = true
				}
			}
			if continuationCanCompleteLocked(state) {
				state.continuationComplete = true
				continuationChanged = true
			} else if state.resultAccepted && state.continuationRequested {
				// A terminal failure or empty response is a state transition too;
				// wake close controllers so they can stop on the primary failure
				// instead of waiting for a later provider close.
				continuationChanged = true
			}
		}
	}
	pending := false
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			pending = true
			break
		}
	}
	terminalAssistantResponse := outputPresent && role != messages.RoleTool && !toolTurn && len(o.unresolvedToolCalls) == 0 && !pending
	if terminalAssistantResponse {
		o.assistantResponseDone = true
	}
	o.toolCallInTurn = false
	lifecycleCh := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	if continuationChanged {
		select {
		case lifecycleCh <- struct{}{}:
		default:
		}
	}
	return terminalAssistantResponse && !duplicateEnd
}

func responseScopedStreamType(t messages.StreamMessageType) bool {
	switch t {
	case messages.StreamTypeMessageStart, messages.StreamTypeMessageEnd,
		messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart, messages.StreamTypeVideoDelta, messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart, messages.StreamTypeFileDelta, messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart, messages.StreamTypeEmbeddingDelta, messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart, messages.StreamTypeReasoningDelta, messages.StreamTypeReasoningEnd,
		messages.StreamTypeTranscriptStart, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptEnd,
		messages.StreamTypeRefusal, messages.StreamTypeUsageInfo:
		return true
	default:
		return false
	}
}
