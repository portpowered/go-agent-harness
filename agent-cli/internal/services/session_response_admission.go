// This file contains the response-admission boundary used by the session
// progress observer. A provider MESSAGE.END only completes a turn after the
// current response has emitted non-empty assistant output and any tool
// continuation lifecycle has reached its terminal state.
package services

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// resetResponseOutputLocked starts a new provider response's output ledger.
// The lock is held by every caller.
func (o *sessionProgressObserver) resetResponseOutputLocked() {
	o.responseOutputTextBytes = 0
	o.responseOutputAudioBytes = 0
	o.responseActionableTool = false
}

// beginResponseContentLocked handles providers that omit MESSAGE.START for a
// later response. A content boundary after MESSAGE.END starts a fresh output
// ledger, while multiple content modalities in one response remain combined.
// The lock is held by every caller.
func (o *sessionProgressObserver) beginResponseContentLocked() {
	if o.messageEndSeen {
		o.resetResponseOutputLocked()
	}
	o.messageEndSeen = false
}

func (o *sessionProgressObserver) responseHasAdmissibleOutput() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.responseOutputTextBytes > 0 || o.responseOutputAudioBytes > 0 || o.responseActionableTool
}

func (o *sessionProgressObserver) setAssistantResponseDone(done bool) {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.assistantResponseDone = done
	o.toolStateMu.Unlock()
}

func assistantResponseDelta(msg messages.StreamMessage) bool {
	// Provider model deltas commonly omit Role. Explicit user and tool deltas
	// are not assistant output and must not admit a response on their own.
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func (o *sessionProgressObserver) toolResultsEnabledForObservation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.toolResultsEnabled
}

// observeProviderMessageEnd advances the provider response state. The first
// MESSAGE.END after a tool call closes the provider's function-call response;
// only a later non-tool MESSAGE.END can complete an accepted continuation.
// outputPresent is the current response's output-admission result. The bool
// return reports whether this boundary is one new, terminal assistant response
// and should therefore count as a completed turn.
func (o *sessionProgressObserver) observeProviderMessageEnd(role messages.Role, outputPresent bool) bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	toolTurn := o.toolCallInTurn
	duplicateEnd := o.messageEndSeen
	o.messageEndSeen = true
	continuationChanged := false
	if toolTurn {
		for _, state := range o.toolContinuations {
			if state != nil && state.providerCallObserved && !state.toolResponseComplete {
				state.toolResponseComplete = true
			}
		}
	} else if role != messages.RoleTool {
		for _, state := range o.toolContinuations {
			if state == nil || !state.toolResponseComplete || state.continuationComplete {
				continue
			}
			state.continuationTerminalSeen = true
			if outputPresent && state.resultAccepted && state.continuationRequested {
				state.continuationComplete = true
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

// lastMessageEndAdmitted reports whether the most recently observed
// MESSAGE.END crossed the shared response-admission boundary. It is consumed
// by stop/close policies after the observer has processed that same event.
func (o *sessionProgressObserver) lastMessageEndAdmitted() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.messageEndAdmitted
}

// assistantResponseCompleted reports whether a non-tool assistant response
// reached MESSAGE.END without another tool call still in the turn.
func (o *sessionProgressObserver) assistantResponseCompleted() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.assistantResponseDone
}

func (o *sessionProgressObserver) providerToolCallObserved() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.providerToolCallSeen
}
