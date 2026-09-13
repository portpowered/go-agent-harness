package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	tools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"sort"
	"strings"
	"unicode"
)

func (o *sessionProgressObserver) setToolResultsEnabled(enabled bool) {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.toolResultsEnabled = enabled
	o.toolStateMu.Unlock()
}

func (o *sessionProgressObserver) ensureToolStateLocked() {
	if o.unresolvedToolCalls == nil {
		o.unresolvedToolCalls = make(map[string]struct{})
	}
	if o.toolResultRejections == nil {
		o.toolResultRejections = make(map[string]messages.SessionSendStatus)
	}
	if o.toolLifecycleCh == nil {
		o.toolLifecycleCh = make(chan struct{}, 1)
	}
}

func sanitizeContinuationDetail(detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	const maxDetailBytes = 256
	if len(detail) > maxDetailBytes {
		return detail[:maxDetailBytes]
	}
	return detail
}

// observeProviderToolCall records the completed provider tool-call obligation.
// Empty IDs are deliberately ignored because they cannot be correlated with a
// later result.
func (o *sessionProgressObserver) observeProviderToolCall(v *messages.ToolCallEndValue) {
	if o == nil || v == nil {
		return
	}
	o.observeProviderToolCallWithID(v.ToolCallID, v.Name)
}
func (o *sessionProgressObserver) observeProviderToolCallWithID(callID, name string) {
	o.observeProviderToolCallWithIDForResponse(callID, name, "")
}

// noteToolResultAccepted resolves exactly one provider call after the
// provider-facing session send boundary reports success. Execution completion,
// queueing, and rejected sends do not reach this method.
func (o *sessionProgressObserver) noteToolResultAccepted(callID string) {
	callID = strings.TrimSpace(callID)
	if o == nil || callID == "" {
		return
	}
	accepted := o.lifecycleEvent(sd.Event{Kind: sd.EventToolResultAccepted, CallID: callID}).Accepted
	if !accepted {
		// The provider send can complete before the first inbound response delta.
		// Establish one provisional, untagged lifecycle for that legitimate
		// handoff, then let the later tool-call event enrich it with its ID.
		active, _ := o.observedResponseProjection()
		if !active {
			o.lifecycleEvent(sd.Event{Kind: sd.EventResponseOpen})
			accepted = o.lifecycleEvent(sd.Event{Kind: sd.EventToolResultAccepted, CallID: callID}).Accepted
		}
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	lifecycleCh := o.toolLifecycleCh
	if accepted {
		delete(o.unresolvedToolCalls, callID)
		delete(o.toolResultRejections, callID)
	} else {
		// Preserve the close/termination obligation when the reducer rejects the
		// result (for example, after Close or an ownership violation).
		o.unresolvedToolCalls[callID] = struct{}{}
	}
	o.toolStateMu.Unlock()

	// One wake-up is enough even when several results are accepted before the
	// session loop selects this branch: the close predicate observes the whole
	// current set, not a count of wake-ups.
	if accepted {
		select {
		case lifecycleCh <- struct{}{}:
		default:
		}
	}
}

// noteToolContinuationRequested advances every accepted result in the
// current provider batch at the explicit response.create send boundary. The
// control event carries no call ID because one provider response may continue
// several parallel function calls; accepted results are therefore the
// correlation set. The operation is idempotent for duplicate control events.
func (o *sessionProgressObserver) noteToolContinuationRequested() {
	if o == nil {
		return
	}
	observation := o.lifecycleEvent(sd.Event{Kind: sd.EventContinuationRequested})
	if observation.Accepted {
		o.signalToolLifecycle()
	}
}

// noteToolContinuationRequestedFor is used by complete-message providers.
// SendMessage may represent a whole rich batch, so the exact call is marked
// first and any already accepted sibling is advanced by the batch-level
// method as well.
func (o *sessionProgressObserver) noteToolContinuationRequestedFor(callID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.lifecycleEvent(sd.Event{Kind: sd.EventContinuationRequested, CallID: callID})
	o.noteToolContinuationRequested()
}

// toolLifecycleEvents wakes the session close controller whenever a result or
// its continuation changes state. The channel is intentionally coalescing: the
// close predicate always reads the complete state snapshot rather than
// interpreting one wake-up as one completed call.
func (o *sessionProgressObserver) toolLifecycleEvents() <-chan struct{} {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	ch := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	return ch
}

func (o *sessionProgressObserver) signalToolLifecycle() {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	ch := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// observeBufferedProviderToolLifecycle recovers provider tool identity that
// the engine has already committed to conversation history but that could not
// reach the consumer-facing delta buffer before a terminal shutdown. It only
// observes model/assistant tool events; tool-runner result deltas are not
// provider requests and must not create a second obligation.
func (o *sessionProgressObserver) observeBufferedProviderToolLifecycle(deltas []messages.StreamMessage) {
	if o == nil {
		return
	}
	for _, msg := range deltas {
		if msg.Role != messages.RoleAssistant && msg.ActorID != messages.Model {
			continue
		}
		switch v := msg.Value.(type) {
		case *messages.ToolCallStartValue:
			if v != nil {
				o.observeProviderToolCallStartForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, msg.ResponseID)
			}
		case *messages.ToolCallDeltaValue:
			if v != nil {
				o.observeProviderToolCallStartForResponse(strings.TrimSpace(msg.ToolCallId), "", msg.ResponseID)
			}
		case *messages.ToolCallEndValue:
			if v != nil {
				o.observeProviderToolCallWithIDForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, msg.ResponseID)
			}
		}
	}
}

func firstNonBlankToolCallID(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

// noteToolResultRejected remembers a failed provider-facing result send
// without resolving its outstanding call ID. A result can be rejected before
// the outer session consumer observes the provider's completed call delta, so
// rejection also registers the call as unresolved. It is intentionally
// idempotent; only the first rejection is retained so repeated attempts cannot
// rewrite the terminal status for a call.
func (o *sessionProgressObserver) noteToolResultRejected(callID string, outcome messages.SessionSendOutcome) {
	if o == nil || strings.TrimSpace(callID) == "" || outcome.OK() {
		return
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	if o.continuationResultAccepted(callID) {
		return
	}
	o.unresolvedToolCalls[callID] = struct{}{}
	if _, recorded := o.toolResultRejections[callID]; !recorded {
		o.toolResultRejections[callID] = outcome.Status
	}
}

func (o *sessionProgressObserver) hasUnresolvedToolCalls() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return len(o.unresolvedToolCalls) > 0
}

// lifecycleContinuationStates returns the reducer's immutable continuation
// view. The CLI adapter deliberately keeps no second continuation map or
// completion projection; all ownership, terminal and retry decisions come
// from this service snapshot.
func (o *sessionProgressObserver) lifecycleContinuationStates() []sd.ContinuationState {
	if o == nil {
		return nil
	}
	lifecycle := o.ensureLifecycle()
	if lifecycle == nil {
		return nil
	}
	return lifecycle.Snapshot().ContinuationStates
}

func (o *sessionProgressObserver) continuationState(callID string) (sd.ContinuationState, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return sd.ContinuationState{}, false
	}
	for _, state := range o.lifecycleContinuationStates() {
		if state.CallID == callID {
			return state, true
		}
	}
	return sd.ContinuationState{}, false
}

func (o *sessionProgressObserver) continuationResultAccepted(callID string) bool {
	state, ok := o.continuationState(callID)
	return ok && state.ResultAccepted
}

// hasPendingToolContinuations reports accepted results that still own the
// current turn. It intentionally includes the interval before the explicit
// continuation request so provider acceptance alone cannot release dispatch
// or close eligibility.
func (o *sessionProgressObserver) hasPendingToolContinuations() bool {
	if o == nil {
		return false
	}
	for _, state := range o.lifecycleContinuationStates() {
		if state.ResultAccepted && !state.ContinuationComplete {
			return true
		}
	}
	return false
}

// hasToolLifecycleObligation is the shared close/dispatch predicate for all
// tool kinds. An unresolved result and an accepted-but-not-terminal
// continuation are both incomplete provider work.
func (o *sessionProgressObserver) hasToolLifecycleObligation() bool {
	return o != nil && (o.hasUnresolvedToolCalls() || o.hasPendingToolContinuations())
}

// hasPendingImageContinuations is distinct from unresolved tool results. A
// read_image result can be accepted by the provider while its response.create
// continuation is still in flight.
func (o *sessionProgressObserver) hasPendingImageContinuations() bool {
	if o == nil {
		return false
	}
	for _, state := range o.lifecycleContinuationStates() {
		if state.ToolName == tools.ReadImageToolID && state.ResultAccepted && !state.ContinuationComplete {
			return true
		}
	}
	return false
}

// pendingImageContinuationCallIDs returns a deterministic snapshot of calls
// whose accepted result still lacks a terminal model continuation.
func (o *sessionProgressObserver) pendingImageContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	for _, state := range states {
		if state.ToolName == tools.ReadImageToolID && state.ResultAccepted && !state.ContinuationComplete {
			ids = append(ids, state.CallID)
		}
	}
	sort.Strings(ids)
	return ids
}

// pendingToolContinuationCallIDs returns accepted call IDs that have not yet
// reached a terminal continuation. IDs are sorted for deterministic errors and
// diagnostics.
func (o *sessionProgressObserver) pendingToolContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	for _, state := range states {
		if state.ResultAccepted && !state.ContinuationComplete {
			ids = append(ids, state.CallID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) pendingNonImageToolContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	for _, state := range states {
		if state.ToolName != tools.ReadImageToolID && state.ResultAccepted && !state.ContinuationComplete {
			ids = append(ids, state.CallID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) pendingImageContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil, nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range states {
		if state.ToolName != tools.ReadImageToolID || !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		ids = append(ids, state.CallID)
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	sort.Strings(ids)
	return ids, statuses, codes, details
}

func (o *sessionProgressObserver) pendingNonImageToolContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil, nil
	}
	states := o.lifecycleContinuationStates()
	ids := make([]string, 0, len(states))
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range states {
		if state.ToolName == tools.ReadImageToolID || !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		ids = append(ids, state.CallID)
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	sort.Strings(ids)
	return ids, statuses, codes, details
}

// pendingContinuationMetadata returns deterministic provider context for all
// accepted continuations still pending at terminal time. The diagnostic uses
// the same call-ID correlation as the typed errors.
func (o *sessionProgressObserver) pendingContinuationMetadata() (map[string]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil
	}
	statuses := make(map[string]string)
	codes := make(map[string]string)
	details := make(map[string]string)
	for _, state := range o.lifecycleContinuationStates() {
		if !state.ResultAccepted || state.ContinuationComplete {
			continue
		}
		if state.ContinuationStatus != "" {
			statuses[state.CallID] = state.ContinuationStatus
		}
		if state.ContinuationErrorCode != "" {
			codes[state.CallID] = state.ContinuationErrorCode
		}
		if state.ContinuationStatusDetails != "" {
			details[state.CallID] = state.ContinuationStatusDetails
		}
	}
	return statuses, codes, details
}

// unresolvedToolCallIDs returns a deterministic snapshot for lifecycle
// consumers and future terminal diagnostics.
func (o *sessionProgressObserver) unresolvedToolCallIDs() []string {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.unresolvedToolCalls))
	for id := range o.unresolvedToolCalls {
		ids = append(ids, id)
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) unresolvedToolResultSendStatuses() map[string]messages.SessionSendStatus {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	statuses := make(map[string]messages.SessionSendStatus, len(o.toolResultRejections))
	for id, status := range o.toolResultRejections {
		if _, outstanding := o.unresolvedToolCalls[id]; outstanding {
			statuses[id] = status
		}
	}
	return statuses
}

// scheduleAudioInputs registers caller-scheduled user audio injections.
