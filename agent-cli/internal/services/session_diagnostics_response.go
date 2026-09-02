package services

import (
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type scheduledAudioResponseDisposition uint8

const (
	scheduledAudioResponsePending scheduledAudioResponseDisposition = iota
	scheduledAudioResponseCompleted
	scheduledAudioResponseCancelled
)

// scheduledAudioResponseLifecycle tracks one dispatched scheduled input's
// logical response. A single input may own an intermediate tool-call response
// and a later continuation response, so provider response IDs are indexed back
// to this one lifecycle rather than being counted as independent turns.
type scheduledAudioResponseLifecycle struct {
	bound       bool
	disposition scheduledAudioResponseDisposition
	// retryUsed and retryPending are scoped to this dispatched logical input.
	// A replacement response must never advance nextScheduledResponse or earn
	// a second retry allowance.
	retryUsed    bool
	retryPending bool
	// terminalFailure retains the last provider-authored non-success terminal
	// for this logical scheduled input. It is cleared when a replacement
	// response opens, and is used to stop a run after retry exhaustion while
	// preserving bounded provider context for the typed incomplete error.
	terminalFailure       bool
	terminalStatus        string
	terminalErrorCode     string
	terminalStatusDetails string
}

func (o *sessionProgressObserver) ensureScheduledResponseState() {
	if o == nil {
		return
	}
	if o.scheduledResponseByID == nil {
		o.scheduledResponseByID = make(map[string]int)
	}
}

// pendingScheduledContinuationIndex identifies the scheduled logical turn
// that owns an accepted tool result whose follow-on response is about to
// begin. Tool continuations must not consume the next scheduled input slot.
func (o *sessionProgressObserver) pendingScheduledContinuationIndex() (int, bool) {
	if o == nil {
		return 0, false
	}
	continuationIndex := -1
	o.toolStateMu.Lock()
	for _, state := range o.toolContinuations {
		if state == nil || !state.resultAccepted || !state.continuationRequested || !state.providerCallObserved || !state.toolResponseComplete || state.continuationResponseID != "" {
			continue
		}
		providerResponseID := strings.TrimSpace(state.responseID)
		index, ok := o.scheduledResponseIndexForContinuation(providerResponseID)
		if !ok || (continuationIndex >= 0 && index >= continuationIndex) {
			continue
		}
		continuationIndex = index
	}
	o.toolStateMu.Unlock()
	return continuationIndex, continuationIndex >= 0
}

// pendingScheduledRateLimitRetryIndex identifies the scheduled logical turn
// whose first eligible rate-limit terminal is waiting for its replacement
// response. It is checked before ordinary continuation ownership so a retry
// response cannot consume the next scheduled lifecycle.
func (o *sessionProgressObserver) pendingScheduledRateLimitRetryIndex() (int, bool) {
	if o == nil {
		return 0, false
	}
	for index := range o.scheduledResponses {
		if o.scheduledResponses[index].bound && o.scheduledResponses[index].retryPending {
			return index, true
		}
	}
	return 0, false
}

// noteScheduledResponseTerminal retains bounded provider failure metadata on
// the owning logical scheduled input. A retrying terminal is still a pending
// lifecycle and is cleared by bindScheduledRateLimitRetry when its replacement
// opens; a second failure remains here for honest terminal reporting.
func (o *sessionProgressObserver) noteScheduledResponseTerminal(id string, terminal *messages.MessageEndValue) {
	if o == nil || !scheduledResponseIsProviderFailure(terminal) {
		return
	}
	index, ok := o.scheduledResponseIndex(id)
	if !ok || index < 0 || index >= len(o.scheduledResponses) {
		return
	}
	lifecycle := &o.scheduledResponses[index]
	if !lifecycle.bound || lifecycle.disposition != scheduledAudioResponsePending {
		return
	}
	lifecycle.terminalFailure = true
	lifecycle.terminalStatus = normalizeTerminalStatus(terminal.Status)
	lifecycle.terminalErrorCode = sanitizeContinuationDetail(providerTerminalErrorCode(terminal))
	lifecycle.terminalStatusDetails = sanitizeContinuationDetail(terminal.StatusDetails)
	if lifecycle.terminalStatusDetails == "" {
		lifecycle.terminalStatusDetails = sanitizeContinuationDetail(providerTerminalErrorMessage(terminal))
	}
}

func scheduledResponseIsProviderFailure(terminal *messages.MessageEndValue) bool {
	if terminal == nil || isLocalResponseCancellation(terminal) || terminal.TerminalReason == messages.TerminalReasonCancellation {
		return false
	}
	status := normalizeTerminalStatus(terminal.Status)
	switch status {
	case "", "completed":
		return terminal.TerminalReason == messages.TerminalReasonTerminalFailure
	case "cancelled", "canceled":
		return false
	default:
		return true
	}
}

func (o *sessionProgressObserver) hasTerminalScheduledResponseFailure() bool {
	if o == nil {
		return false
	}
	for _, lifecycle := range o.scheduledResponses {
		if lifecycle.bound && lifecycle.disposition == scheduledAudioResponsePending && lifecycle.terminalFailure && !lifecycle.retryPending {
			return true
		}
	}
	return false
}

// scheduledAudioFailureMetadata returns the first pending scheduled lifecycle
// with a provider-authored terminal failure. Scheduled inputs are dispatched in
// order, so the index order is deterministic even when a provider delivers a
// late terminal during shutdown.
func (o *sessionProgressObserver) scheduledAudioFailureMetadata() (string, string, string) {
	if o == nil {
		return "", "", ""
	}
	for _, lifecycle := range o.scheduledResponses {
		if !lifecycle.bound || lifecycle.disposition != scheduledAudioResponsePending || !lifecycle.terminalFailure || lifecycle.retryPending {
			continue
		}
		return lifecycle.terminalStatus, lifecycle.terminalErrorCode, lifecycle.terminalStatusDetails
	}
	return "", "", ""
}

// bindScheduledResponseBoundary associates a newly opened provider response
// with its logical scheduled owner. A pending retry takes precedence over a
// tool continuation and both take precedence over the next input slot.
func (o *sessionProgressObserver) bindScheduledResponseBoundary(id string) {
	if o == nil {
		return
	}
	if retryIndex, retry := o.pendingScheduledRateLimitRetryIndex(); retry {
		o.bindScheduledRateLimitRetry(retryIndex, id)
		return
	}
	if continuationIndex, continuation := o.pendingScheduledContinuationIndex(); continuation {
		if o.bindScheduledContinuation(continuationIndex, id) {
			o.bindPendingToolContinuations(continuationIndex, id)
		}
		return
	}
	o.bindNextScheduledResponse(id)
}

// bindScheduledTerminalOnly handles a legacy terminal-only boundary. Such a
// boundary cannot prove tool-continuation ownership, but a pending retry still
// has an explicit logical owner and must be preferred over the next slot. The
// retry path starts a fresh observed response; the ordinary path preserves the
// output ledger accumulated before the terminal-only event.
func (o *sessionProgressObserver) bindScheduledTerminalOnly(id string) {
	if o == nil {
		return
	}
	if retryIndex, retry := o.pendingScheduledRateLimitRetryIndex(); retry {
		o.beginObservedResponse("")
		o.bindScheduledRateLimitRetry(retryIndex, id)
		return
	}
	o.bindNextScheduledResponse(id)
}

func (o *sessionProgressObserver) rememberRateLimitRetryCandidate(responseID, lifecycleID string, terminal *messages.MessageEndValue) {
	if o == nil {
		return
	}
	o.retryCandidateSet = false
	o.retryCandidateID = ""
	if _, eligible := rateLimitRetryDecision(terminal); !eligible {
		return
	}
	index, ok := o.scheduledResponseIndex(lifecycleID)
	if !ok {
		return
	}
	o.retryCandidateIndex = index
	o.retryCandidateSet = true
	o.retryCandidateID = strings.TrimSpace(responseID)
}

func (o *sessionProgressObserver) scheduledResponseIndexForContinuation(id string) (int, bool) {
	if o == nil {
		return 0, false
	}
	o.ensureScheduledResponseState()
	id = strings.TrimSpace(id)
	if id != "" {
		index, ok := o.scheduledResponseByID[id]
		return index, ok && index >= 0 && index < len(o.scheduledResponses)
	}
	if o.logicalScheduledResponseSet {
		return o.logicalScheduledResponseIndex, true
	}
	if o.activeScheduledResponseSet {
		return o.activeScheduledResponseIndex, true
	}
	return 0, false
}

func (o *sessionProgressObserver) bindScheduledResponseID(index int, id string) bool {
	if o == nil || index < 0 || index >= len(o.scheduledResponses) {
		return false
	}
	o.ensureScheduledResponseState()
	id = strings.TrimSpace(id)
	if id != "" {
		if existing, ok := o.scheduledResponseByID[id]; ok && existing != index {
			// A provider response ID is an ownership key, not a reusable slot
			// label. Refuse to transfer it to a later scheduled lifecycle.
			return false
		}
	}
	o.scheduledResponses[index].bound = true
	if id != "" {
		o.scheduledResponseByID[id] = index
	}
	return true
}

func (o *sessionProgressObserver) setActiveScheduledResponseWithID(index int, id string) bool {
	if o == nil || index < 0 || index >= len(o.scheduledResponses) {
		return false
	}
	id = strings.TrimSpace(id)
	if id != "" {
		if existing, ok := o.scheduledResponseByID[id]; ok && existing != index {
			return false
		}
	}
	o.activeScheduledResponseIndex = index
	o.activeScheduledResponseID = id
	o.activeScheduledResponseSet = true
	o.logicalScheduledResponseIndex = index
	o.logicalScheduledResponseID = id
	o.logicalScheduledResponseSet = true
	return true
}

// bindNextScheduledResponse associates a new provider response boundary with
// the next dispatched input. It is called only for a new logical response;
// continuation responses use bindScheduledContinuation instead.
func (o *sessionProgressObserver) bindNextScheduledResponse(id string) (int, bool) {
	if o == nil {
		return 0, false
	}
	o.ensureScheduledResponseState()
	id = strings.TrimSpace(id)
	if id != "" {
		if index, ok := o.scheduledResponseByID[id]; ok {
			if o.setActiveScheduledResponseWithID(index, id) {
				return index, true
			}
			return 0, false
		}
	}
	for o.nextScheduledResponse < len(o.scheduledResponses) && o.scheduledResponses[o.nextScheduledResponse].bound {
		o.nextScheduledResponse++
	}
	if o.nextScheduledResponse >= len(o.scheduledResponses) {
		return 0, false
	}
	index := o.nextScheduledResponse
	if !o.bindScheduledResponseID(index, id) || !o.setActiveScheduledResponseWithID(index, id) {
		return 0, false
	}
	o.nextScheduledResponse++
	return index, true
}

func (o *sessionProgressObserver) bindScheduledContinuation(index int, id string) bool {
	if o == nil {
		return false
	}
	if index < 0 || index >= len(o.scheduledResponses) {
		if o.logicalScheduledResponseSet {
			index = o.logicalScheduledResponseIndex
		} else {
			return false
		}
	}
	if !o.bindScheduledResponseID(index, id) || !o.setActiveScheduledResponseWithID(index, id) {
		return false
	}
	return true
}

// bindScheduledRateLimitRetry binds a replacement response to the lifecycle
// that armed the retry. Unlike bindNextScheduledResponse it does not advance
// the next scheduled slot. A failed continuation may have retained provider
// terminal state, so clear only that response's terminal ledger while keeping
// the accepted tool result and its one continuation request intact.
func (o *sessionProgressObserver) bindScheduledRateLimitRetry(index int, id string) bool {
	if o == nil || index < 0 || index >= len(o.scheduledResponses) {
		return false
	}
	lifecycle := &o.scheduledResponses[index]
	if !lifecycle.bound || !lifecycle.retryPending {
		return false
	}
	if !o.bindScheduledResponseID(index, id) || !o.setActiveScheduledResponseWithID(index, id) {
		return false
	}
	lifecycle.retryPending = false
	lifecycle.terminalFailure = false
	lifecycle.terminalStatus = ""
	lifecycle.terminalErrorCode = ""
	lifecycle.terminalStatusDetails = ""
	o.toolStateMu.Lock()
	for _, state := range o.toolContinuations {
		if state == nil || !state.continuationScheduledSet || state.continuationScheduledIndex != index {
			continue
		}
		state.continuationResponseID = strings.TrimSpace(id)
		state.continuationTerminalSeen = false
		state.continuationStatus = ""
		state.continuationErrorCode = ""
		state.continuationStatusDetails = ""
		state.continuationTerminalReason = ""
		state.continuationOutputObserved = false
		state.continuationFailureObserved = false
		state.continuationComplete = false
	}
	o.toolStateMu.Unlock()
	return true
}

// claimScheduledRateLimitRetry consumes the one retry allowance for the
// scheduled lifecycle identified by responseID. The lifecycle remains
// pending until a replacement response is admitted or the run terminates.
func (o *sessionProgressObserver) claimScheduledRateLimitRetry(responseID string, terminal *messages.MessageEndValue) (time.Duration, bool) {
	if o == nil {
		return 0, false
	}
	delay, eligible := rateLimitRetryDecision(terminal)
	if !eligible {
		return 0, false
	}
	index, ok := o.scheduledResponseIndex(responseID)
	if !ok && o.retryCandidateSet && strings.TrimSpace(responseID) == o.retryCandidateID {
		index = o.retryCandidateIndex
		ok = index >= 0 && index < len(o.scheduledResponses)
	}
	if !ok || index < 0 || index >= len(o.scheduledResponses) {
		return 0, false
	}
	lifecycle := &o.scheduledResponses[index]
	if !lifecycle.bound || lifecycle.disposition != scheduledAudioResponsePending || lifecycle.retryUsed {
		return 0, false
	}
	lifecycle.retryUsed = true
	lifecycle.retryPending = true
	o.retryCandidateSet = false
	o.retryCandidateID = ""
	// The failed continuation is deliberately not terminal while its retry is
	// pending. Keep the accepted result and request ownership, but clear the
	// failed response's admission ledger so the replacement can complete it.
	o.toolStateMu.Lock()
	for _, state := range o.toolContinuations {
		if state == nil || !state.continuationScheduledSet || state.continuationScheduledIndex != index {
			continue
		}
		state.continuationTerminalSeen = false
		state.continuationStatus = ""
		state.continuationErrorCode = ""
		state.continuationStatusDetails = ""
		state.continuationTerminalReason = ""
		state.continuationOutputObserved = false
		state.continuationFailureObserved = false
		state.continuationComplete = false
	}
	o.toolStateMu.Unlock()
	return delay, true
}

// bindPendingToolContinuations attaches only the accepted calls whose
// originating provider response belongs to index. A tool result is emitted by
// the loop as a separate RoleTool stream, so response-wide fallback here would
// allow a later tool envelope to steal the continuation from its owner.
func (o *sessionProgressObserver) bindPendingToolContinuations(index int, id string) {
	if o == nil {
		return
	}
	id = strings.TrimSpace(id)
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	for _, state := range o.toolContinuations {
		if state == nil || !state.resultAccepted || !state.continuationRequested || !state.providerCallObserved || !state.toolResponseComplete || state.continuationResponseID != "" {
			continue
		}
		ownerIndex, ok := o.scheduledResponseIndexForContinuation(state.responseID)
		if ok && ownerIndex == index {
			state.continuationScheduledIndex = index
			state.continuationScheduledSet = true
			if id != "" {
				state.continuationResponseID = id
			}
		}
	}
}

func (o *sessionProgressObserver) scheduledResponseIndex(id string) (int, bool) {
	if o == nil {
		return 0, false
	}
	o.ensureScheduledResponseState()
	id = strings.TrimSpace(id)
	if id != "" {
		if index, ok := o.scheduledResponseByID[id]; ok {
			return index, true
		}
		// An identified terminal with no matching owner cannot fall back to
		// whichever lifecycle happens to be active. It may be a new terminal-only
		// response, in which case noteScheduledResponseDisposition will bind it
		// explicitly to the next slot.
		return 0, false
	}
	if o.activeScheduledResponseSet {
		return o.activeScheduledResponseIndex, true
	}
	if o.logicalScheduledResponseSet {
		return o.logicalScheduledResponseIndex, true
	}
	return 0, false
}

// noteScheduledResponseDisposition records one terminal disposition for a
// dispatched logical scheduled input. Cancellation counts as resolved
// lifecycle work but never advances the ordinary completed-turn counter.
func (o *sessionProgressObserver) noteScheduledResponseDisposition(id string, disposition scheduledAudioResponseDisposition) {
	if o == nil || disposition == scheduledAudioResponsePending {
		return
	}
	index, ok := o.scheduledResponseIndex(id)
	if !ok {
		if !o.canBindUnidentifiedScheduledResponse(id) {
			return
		}
		_, ok = o.bindNextScheduledResponse(id)
		if !ok {
			return
		}
		index, ok = o.scheduledResponseIndex(id)
		if !ok {
			return
		}
	}
	if index < 0 || index >= len(o.scheduledResponses) {
		return
	}
	lifecycle := &o.scheduledResponses[index]
	if !lifecycle.bound {
		return
	}
	if !o.scheduledResponseOwnerMatches(index, id) {
		// A late terminal from an earlier provider response can still resolve to
		// this logical lifecycle through the historical ID map. It must not
		// complete a newer continuation that now owns that lifecycle.
		return
	}
	if lifecycle.disposition != scheduledAudioResponsePending {
		// A cancelled provider response and its later continuation share one
		// scheduled lifecycle. The continuation terminal reaches this method
		// with the same logical index after the cancellation already resolved the
		// lifecycle, so clear the temporary active mapping even though the
		// terminal disposition itself is not duplicated.
		o.clearScheduledResponseOwner(index, id)
		return
	}
	lifecycle.disposition = disposition
	lifecycle.retryPending = false
	lifecycle.retryUsed = false
	lifecycle.terminalFailure = false
	lifecycle.terminalStatus = ""
	lifecycle.terminalErrorCode = ""
	lifecycle.terminalStatusDetails = ""
	o.completedScheduled++
	o.clearScheduledResponseOwner(index, id)
}

func (o *sessionProgressObserver) canBindUnidentifiedScheduledResponse(id string) bool {
	if o == nil || strings.TrimSpace(id) == "" {
		return true
	}
	id = strings.TrimSpace(id)
	if o.activeScheduledResponseSet && o.activeScheduledResponseID != "" && o.activeScheduledResponseID != id {
		return false
	}
	return !o.logicalScheduledResponseSet || o.logicalScheduledResponseID == "" || o.logicalScheduledResponseID == id
}

func (o *sessionProgressObserver) scheduledResponseOwnerMatches(index int, id string) bool {
	if o == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return o.activeScheduledResponseSet && o.activeScheduledResponseIndex == index && o.activeScheduledResponseID == ""
	}
	if o.activeScheduledResponseSet && o.activeScheduledResponseIndex == index {
		return o.activeScheduledResponseID == id
	}
	if o.logicalScheduledResponseSet && o.logicalScheduledResponseIndex == index {
		return o.logicalScheduledResponseID == id
	}
	return true
}

// clearScheduledResponseOwner clears only the owner that supplied the
// disposition. Historical IDs remain mapped to the lifecycle, but they cannot
// erase a newer active/logical response ID for that same lifecycle.
func (o *sessionProgressObserver) clearScheduledResponseOwner(index int, id string) {
	if o == nil {
		return
	}
	id = strings.TrimSpace(id)
	o.clearActiveScheduledResponseOwner(index, id)
	if o.logicalScheduledResponseSet && o.logicalScheduledResponseIndex == index && o.logicalScheduledResponseID == id {
		o.logicalScheduledResponseSet = false
		o.logicalScheduledResponseID = ""
	}
}

func (o *sessionProgressObserver) clearActiveScheduledResponseOwner(index int, id string) {
	if o == nil {
		return
	}
	id = strings.TrimSpace(id)
	if o.activeScheduledResponseSet && o.activeScheduledResponseIndex == index && o.activeScheduledResponseID == id {
		o.activeScheduledResponseSet = false
		o.activeScheduledResponseID = ""
	}
}

func isLocalResponseCancellation(value *messages.MessageEndValue) bool {
	return value != nil && value.TerminalReason == messages.TerminalReasonPartialOutput && value.TerminalProvenance == messages.TerminalProvenanceLoop
}

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
	return o.beginObservedResponseForPurpose(id, "")
}

func (o *sessionProgressObserver) beginObservedResponseForPurpose(id string, purpose messages.ResponsePurpose) bool {
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
	// Unscheduled tool sessions do not have a scheduled lifecycle to bind
	// against. Preserve the response-ID association used by the ordinary
	// continuation path so their pending calls can still observe the next
	// provider response.
	if purpose != messages.ResponsePurposeToolAcknowledgement && id != "" && len(o.scheduledResponses) == 0 {
		o.toolStateMu.Lock()
		o.ensureToolStateLocked()
		for _, state := range o.toolContinuations {
			if state == nil || !state.resultAccepted || !state.continuationRequested ||
				!state.providerCallObserved || !state.toolResponseComplete || state.continuationResponseID != "" {
				continue
			}
			state.continuationResponseID = id
		}
		o.toolStateMu.Unlock()
	}
	return true
}

// adoptObservedResponseID upgrades a legacy response that opened without an
// ID when a later terminal event supplies one. It preserves the response's
// output ledger while binding accepted tool continuations to the newly known
// owner, so a terminal-only provider ID remains useful without allowing a
// completed or retired response to reclaim the active lifecycle.
func (o *sessionProgressObserver) adoptObservedResponseID(id string) bool {
	if o == nil || !o.activeResponse || o.activeResponseID != "" {
		return true
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	if o.completedResponseIDs == nil {
		o.completedResponseIDs = make(map[string]struct{})
	}
	if o.retiredResponseIDs == nil {
		o.retiredResponseIDs = make(map[string]struct{})
	}
	if _, completed := o.completedResponseIDs[id]; completed {
		return false
	}
	if _, retired := o.retiredResponseIDs[id]; retired {
		return false
	}
	if o.activeScheduledResponseSet {
		if !o.bindScheduledResponseID(o.activeScheduledResponseIndex, id) {
			return false
		}
	}
	o.activeResponseID = id
	if o.activeScheduledResponseSet {
		o.setActiveScheduledResponseWithID(o.activeScheduledResponseIndex, id)
		o.bindPendingToolContinuations(o.activeScheduledResponseIndex, id)
	} else if len(o.scheduledResponses) == 0 {
		// Unscheduled tool sessions can first learn the provider response ID at
		// the terminal event. Keep their pending continuation associated with
		// that adopted ID just as beginObservedResponse does for identified
		// response.created events.
		o.toolStateMu.Lock()
		o.ensureToolStateLocked()
		for _, state := range o.toolContinuations {
			if state == nil || !state.resultAccepted || !state.continuationRequested ||
				!state.providerCallObserved || !state.toolResponseComplete || state.continuationResponseID != "" {
				continue
			}
			state.continuationResponseID = id
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
		if id == "" {
			return true
		}
		if o.completedResponseIDs == nil {
			o.completedResponseIDs = make(map[string]struct{})
		}
		if o.retiredResponseIDs == nil {
			o.retiredResponseIDs = make(map[string]struct{})
		}
		_, completed := o.completedResponseIDs[id]
		_, retired := o.retiredResponseIDs[id]
		return !completed && !retired
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
	if o.activeScheduledResponseSet {
		o.clearActiveScheduledResponseOwner(o.activeScheduledResponseIndex, id)
	}
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
	if normalizedResponseID := strings.TrimSpace(responseID); normalizedResponseID != "" && (state.responseID == "" || state.responseID == normalizedResponseID) {
		// Keep the first provider response identity for a call. A duplicate or
		// late event with a different identity must not move an accepted result
		// to another scheduled lifecycle.
		state.responseID = normalizedResponseID
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

func (o *sessionProgressObserver) continuationStateBelongsToObservedResponse(state *toolContinuationState, responseID string) bool {
	if continuationStateBelongsToResponse(state, responseID) {
		return true
	}
	if o == nil || state == nil || strings.TrimSpace(responseID) != "" || !state.continuationScheduledSet {
		return false
	}
	return o.activeResponse && o.activeResponseID == "" && o.activeScheduledResponseSet && o.activeScheduledResponseIndex == state.continuationScheduledIndex
}

func (o *sessionProgressObserver) continuationOwnerMatchesObservedResponse(state *toolContinuationState, responseID string) bool {
	if o == nil || state == nil {
		return false
	}
	if responseID = strings.TrimSpace(responseID); responseID != "" {
		return state.continuationResponseID == responseID
	}
	return state.continuationScheduledSet && o.activeResponse && o.activeResponseID == "" && o.activeScheduledResponseSet && o.activeScheduledResponseIndex == state.continuationScheduledIndex
}

// recordContinuationTerminalLocked records the terminal boundary for an
// accepted tool result's follow-on response. A continuation may itself emit a
// new tool call, so actionable tool output is valid continuation output even
// though the response is still a tool turn for the newly emitted call.
func recordContinuationTerminalLocked(state *toolContinuationState, terminal *messages.MessageEndValue, outputObserved bool) bool {
	if state == nil || !state.toolResponseComplete {
		return false
	}
	if !state.continuationTerminalSeen {
		state.continuationTerminalSeen = true
		if terminal != nil {
			state.continuationStatus = normalizeContinuationStatus(terminal.Status)
			state.continuationErrorCode = sanitizeContinuationDetail(providerTerminalErrorCode(terminal))
			state.continuationStatusDetails = sanitizeContinuationDetail(terminal.StatusDetails)
			if state.continuationStatusDetails == "" {
				state.continuationStatusDetails = sanitizeContinuationDetail(providerTerminalErrorMessage(terminal))
			}
			state.continuationTerminalReason = terminal.TerminalReason
		}
		state.continuationOutputObserved = outputObserved
		if continuationSupersededByServerTurnLocked(state) {
			state.continuationComplete = true
			return true
		}
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
		return true
	}
	return state.resultAccepted && state.continuationRequested
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
			if o.continuationStateBelongsToObservedResponse(state, responseID) && state.providerCallObserved && !state.toolResponseComplete {
				state.toolResponseComplete = true
			}
		}
		// A continuation response may itself emit the next tool call. Its
		// MESSAGE.END is still the predecessor continuation's terminal boundary;
		// the new call is the observable output that makes that continuation
		// complete.
		for _, state := range o.toolContinuations {
			if state == nil || !o.continuationOwnerMatchesObservedResponse(state, responseID) || state.continuationComplete {
				continue
			}
			if recordContinuationTerminalLocked(state, terminal, o.assistantOutputObserved || o.responseActionableTool) {
				continuationChanged = true
			}
		}
	} else if role != messages.RoleTool {
		for _, state := range o.toolContinuations {
			if !o.continuationStateBelongsToObservedResponse(state, responseID) || !state.toolResponseComplete || state.continuationComplete {
				continue
			}
			if duplicateEnd && !state.continuationRequested {
				// A second MESSAGE.END before the accepted result's explicit
				// response request is still a duplicate of the provider's
				// function-call response, not evidence of an empty continuation.
				continue
			}
			if recordContinuationTerminalLocked(state, terminal, o.assistantOutputObserved) {
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
