// This file contains the response-admission boundary used by the session
// progress observer. A provider MESSAGE.END only completes a turn after the
// current response has emitted non-empty assistant output.
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

// observeProviderMessageEnd advances the provider response state. The first
// MESSAGE.END after a tool call closes the provider's function-call response;
// only a later non-tool MESSAGE.END can complete an accepted continuation.
// outputPresent is the current response's output-admission result. The bool
// return reports whether this boundary is one new, terminal assistant
// response and should therefore count as a completed turn.
func (o *sessionProgressObserver) observeProviderMessageEnd(role messages.Role, terminal *messages.MessageEndValue, outputPresent bool) bool {
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

// hasTerminalToolContinuationFailure reports a provider-authored terminal
// response that cannot discharge an accepted continuation. It is checked by
// both text and audio stop rules so failed or empty response.done events end
// the run with a typed lifecycle error instead of hanging behind the pending
// obligation.
func (o *sessionProgressObserver) hasTerminalToolContinuationFailure() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	for _, state := range o.toolContinuations {
		if continuationTerminalFailureLocked(state) {
			return true
		}
	}
	return false
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
