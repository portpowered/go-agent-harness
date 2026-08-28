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
