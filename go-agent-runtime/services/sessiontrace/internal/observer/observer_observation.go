package observer

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"strings"
)

func (o *observerState) observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	unlockProviderBoundary := o.lockProviderBoundary()
	defer unlockProviderBoundary()
	o.observeProviderBoundary(msg)
	if o.streamObserver != nil {
		o.streamObserver(msg)
	}
	o.observeProviderEvent(msg)
	if o.observeNonResponseMessage(msg) {
		return
	}
	responseLifecycleID, newResponseBoundary, acknowledgementResponse, ok := o.prepareObservedResponse(msg)
	if !ok {
		return
	}
	o.observeSessionLifecycleBoundary(msg)
	o.observeValue(msg, responseLifecycleID, newResponseBoundary, acknowledgementResponse)
}

func (o *observerState) observeNonResponseMessage(msg messages.StreamMessage) bool {
	// Input-audio transcription belongs to the customer input stream. It must
	// remain observable, but it cannot open, reset, or complete an assistant
	// response—especially when a provider interleaves recognition with output.
	if msg.Role == messages.RoleUser && (msg.Type == messages.StreamTypeTranscriptStart || msg.Type == messages.StreamTypeTranscriptDelta || msg.Type == messages.StreamTypeTranscriptEnd) {
		if value, ok := msg.Value.(*messages.TranscriptDeltaValue); ok && value != nil {
			o.account(metrics.DirectionInput, metrics.ModalityText, len(value.Text))
		}
		return true
	}
	// ToolRunner delivery is an internal bridge between the provider tool call
	// and the next provider response. It is observable to callers, but it is
	// not a provider response boundary and must not reset response output,
	// consume a scheduled slot, or become the owner of a continuation.
	if msg.Role == messages.RoleTool {
		o.accountToolRoleMessage(msg)
		return true
	}
	return false
}

func (o *observerState) prepareObservedResponse(msg messages.StreamMessage) (string, bool, bool, bool) {
	msgResponseID := strings.TrimSpace(msg.ResponseID)
	responseLifecycleID := msgResponseID
	acknowledgementResponse := msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement
	if msg.Type == messages.StreamTypeMessageStart || msg.Type == messages.StreamTypeAudioStart {
		newResponseBoundary, ok := o.prepareResponseStart(msgResponseID, msg.ResponsePurpose, msg.Role, acknowledgementResponse)
		if !ok {
			return "", false, false, false
		}
		return responseLifecycleID, newResponseBoundary, acknowledgementResponse, true
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		newResponseBoundary, responseLifecycleID, ok := o.prepareResponseEnd(msgResponseID, msg.ResponsePurpose, acknowledgementResponse)
		if !ok {
			return "", false, false, false
		}
		return responseLifecycleID, newResponseBoundary, acknowledgementResponse, true
	}
	if msg.Type == messages.StreamTypeSessionClose {
		// Keep the active response owner while draining already-queued provider
		// output. A transport can deliver SESSION.CLOSE before the response's
		// terminal event; clearing the owner here would make that terminal look
		// like a new response and discard its output ledger.
	}
	if msg.Type != messages.StreamTypeSessionClose && responseScopedStreamType(msg.Type) && !o.responseEventBelongsToActive(msgResponseID) {
		return "", false, false, false
	}
	if responseLifecycleID == "" {
		_, responseLifecycleID = o.observedResponseProjection()
	}
	return responseLifecycleID, false, acknowledgementResponse, true
}

func (o *observerState) prepareResponseStart(responseID string, purpose messages.ResponsePurpose, role messages.Role, acknowledgement bool) (bool, bool) {
	newBoundary := o.beginObservedResponseForPurpose(responseID, purpose)
	if !o.responseEventBelongsToActive(responseID) {
		return false, false
	}
	if newBoundary && role != messages.RoleTool && !acknowledgement {
		o.bindScheduledResponseBoundary(responseID)
	}
	return newBoundary, true
}

func (o *observerState) prepareResponseEnd(responseID string, purpose messages.ResponsePurpose, acknowledgement bool) (bool, string, bool) {
	if !o.ownsObservedResponseEnd(responseID) {
		return false, "", false
	}
	activeResponse, activeResponseID := o.observedResponseProjection()
	if responseID != "" && activeResponse && activeResponseID == "" && !o.adoptObservedResponseID(responseID) {
		return false, "", false
	}
	newBoundary := o.prepareUnownedResponseEnd(responseID, purpose, acknowledgement, activeResponse)
	if responseID == "" {
		_, responseID = o.observedResponseProjection()
	}
	return newBoundary, responseID, true
}

func (o *observerState) prepareUnownedResponseEnd(responseID string, purpose messages.ResponsePurpose, acknowledgement, active bool) bool {
	if active {
		return false
	}
	if responseID != "" {
		newBoundary := o.beginObservedResponseForPurpose(responseID, purpose)
		if newBoundary && !acknowledgement {
			o.bindScheduledResponseBoundary(responseID)
		}
		return newBoundary
	}
	if !acknowledgement {
		o.bindScheduledTerminalOnly("")
	}
	return false
}

func (o *observerState) observeValue(msg messages.StreamMessage, responseLifecycleID string, newResponseBoundary, acknowledgementResponse bool) {
	switch msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.sawSessionOpen = true
	case *messages.MessageStartValue:
		if newResponseBoundary {
			o.resetObservedResponseState()
		}
	default:
		if o.observeContentValue(msg, responseLifecycleID, newResponseBoundary) {
			return
		}
		o.observeTerminalValue(msg, responseLifecycleID, acknowledgementResponse)
	}
}

func (o *observerState) observeContentValue(msg messages.StreamMessage, responseLifecycleID string, newResponseBoundary bool) bool {
	switch v := msg.Value.(type) {
	case *messages.TextStartValue, *messages.AudioStartValue, *messages.ReasoningStartValue,
		*messages.ImageStartValue, *messages.VideoStartValue, *messages.FileStartValue,
		*messages.EmbeddingStartValue, *messages.TranscriptStartValue:
		o.observeContentStart(newResponseBoundary)
	case *messages.AudioDeltaValue:
		o.observeOutputContent(msg, metrics.ModalityAudio, len(v.Content), len(v.Content) > 0)
	case *messages.TextDeltaValue:
		o.observeOutputContent(msg, metrics.ModalityText, len(v.Content), strings.TrimSpace(v.Content) != "")
	case *messages.TranscriptDeltaValue:
		o.observeOutputContent(msg, metrics.ModalityText, len(v.Text), strings.TrimSpace(v.Text) != "")
	case *messages.TranscriptEndValue:
		o.observeOutputContent(msg, metrics.ModalityText, len(v.FullText), strings.TrimSpace(v.FullText) != "")
	case *messages.ToolCallStartValue:
		o.observeToolCallStart(msg, v, responseLifecycleID)
	case *messages.ToolCallDeltaValue:
		o.observeToolCallDelta(msg, v, responseLifecycleID)
	case *messages.ToolCallEndValue:
		o.observeToolCallEnd(msg, v, responseLifecycleID)
	default:
		return false
	}
	return true
}

func (o *observerState) observeTerminalValue(msg messages.StreamMessage, responseLifecycleID string, acknowledgement bool) {
	switch v := msg.Value.(type) {
	case *messages.MessageEndValue:
		o.observeMessageEnd(msg, v, responseLifecycleID, strings.TrimSpace(msg.ResponseID), acknowledgement)
	case *messages.ErrorValue:
		o.captureFailureFromError(v)
		if v != nil && v.IsTerminal() {
			o.disarmProviderProgress()
		}
	case *messages.SessionCloseValue:
		o.captureFailureFromClose(v)
		o.disarmProviderProgress()
	}
}

func (o *observerState) observeContentStart(newResponseBoundary bool) {
	if newResponseBoundary {
		o.resetObservedResponseState()
	}
	o.toolStateMu.Lock()
	if o.messageEndSeen || o.assistantResponseDone {
		o.assistantOutputObserved = false
	}
	o.beginResponseContentLocked()
	o.assistantResponseDone = false
	o.toolStateMu.Unlock()
}

func (o *observerState) observeOutputContent(msg messages.StreamMessage, modality metrics.Modality, size int, meaningful bool) {
	o.account(metrics.DirectionOutput, modality, size)
	o.toolStateMu.Lock()
	if o.messageEndSeen {
		o.assistantOutputObserved = false
	}
	o.beginResponseContentLocked()
	if assistantResponseDelta(msg) && size > 0 {
		if modality == metrics.ModalityAudio {
			o.responseOutputAudioBytes += uint64(size)
		} else {
			o.responseOutputTextBytes += uint64(size)
		}
	}
	if meaningful && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
		o.assistantOutputObserved = true
	}
	o.toolStateMu.Unlock()
}

func (o *observerState) observeToolCallStart(msg messages.StreamMessage, value *messages.ToolCallStartValue, responseID string) {
	o.observeProviderToolCallStartForResponse(firstNonBlankToolCallID(value.ToolCallID, msg.ToolCallId), value.Name, responseID)
	o.toolDeltaSeen = false
	o.markToolCallInResponse()
}

func (o *observerState) observeToolCallDelta(msg messages.StreamMessage, value *messages.ToolCallDeltaValue, responseID string) {
	o.observeProviderToolCallStartForResponse(strings.TrimSpace(msg.ToolCallId), "", responseID)
	o.account(metrics.DirectionOutput, metrics.ModalityTool, len(value.PartialJSON))
	o.toolDeltaSeen = true
	o.markToolCallInResponse()
}

func (o *observerState) markToolCallInResponse() {
	o.toolStateMu.Lock()
	o.beginResponseContentLocked()
	o.assistantResponseDone = false
	o.toolCallInTurn = o.toolResultsEnabled
	o.toolStateMu.Unlock()
}

func (o *observerState) observeToolCallEnd(msg messages.StreamMessage, value *messages.ToolCallEndValue, responseID string) {
	callID := firstNonBlankToolCallID(value.ToolCallID, msg.ToolCallId)
	o.observeProviderToolCallWithIDForResponse(callID, value.Name, responseID)
	if !o.toolResultsEnabledForObservation() {
		o.emitToolCallRecord(value)
	}
	o.toolStateMu.Lock()
	o.beginResponseContentLocked()
	o.assistantResponseDone = false
	o.toolCallInTurn = o.toolResultsEnabled
	if strings.TrimSpace(callID) != "" && strings.TrimSpace(value.Name) != "" {
		o.responseActionableTool = true
	}
	if o.toolResultsEnabled {
		o.providerToolCallSeen = true
	}
	o.toolStateMu.Unlock()
	if !o.toolDeltaSeen {
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(value.Arguments))
	}
	o.toolDeltaSeen = false
}

func (o *observerState) observeMessageEnd(msg messages.StreamMessage, value *messages.MessageEndValue, responseID, messageID string, acknowledgement bool) {
	cancelled := isLocalResponseCancellation(value)
	o.noteProviderUsage(value.Usage)
	o.setAssistantResponseDone(false)
	outputPresent := o.responseHasAdmissibleOutput()
	toolObligation := o.responseHasToolLifecycleObligation()
	admitted := o.admitMessageEnd(msg, value, responseID, messageID, acknowledgement, cancelled, outputPresent)
	o.setAssistantResponseDone(admitted)
	o.toolStateMu.Lock()
	o.messageEndAdmitted = admitted
	o.toolStateMu.Unlock()
	if admitted {
		if !o.notifyTerminalObservation(sessionTerminalObservationFromMessageEnd(responseID, value)) {
			o.finishObservedResponse(responseID)
			return
		}
		o.noteScheduledResponseDisposition(responseID, scheduledAudioResponseCompleted)
		o.completeTurn()
		if o.admittedTurnObserver != nil {
			o.admittedTurnObserver(msg)
		}
	} else if cancelled && !acknowledgement {
		o.noteScheduledResponseDisposition(responseID, scheduledAudioResponseCancelled)
	}
	o.finishObservedResponse(responseID)
	if !acknowledgement {
		o.observeSilentProviderEmptyResponse(msg, value, outputPresent, toolObligation)
	}
	o.disarmProviderProgress()
}

func (o *observerState) admitMessageEnd(msg messages.StreamMessage, value *messages.MessageEndValue, responseID, messageID string, acknowledgement, cancelled, outputPresent bool) bool {
	if acknowledgement {
		return false
	}
	candidate := o.observeProviderMessageEndForResponse(msg.Role, value, responseID, outputPresent)
	o.noteScheduledResponseTerminal(responseID, value)
	o.rememberRateLimitRetryCandidate(messageID, responseID, value)
	if cancelled || !candidate || !outputPresent || !messageEndCanAdmit(value) {
		return false
	}
	return o.turnAdmission == nil || o.turnAdmission(msg)
}

func (o *observerState) observeProviderBoundary(msg messages.StreamMessage) {
	// Server-VAD providers own these boundaries and report them inbound. Emit
	// the runtime observations before the general stream callback so room
	// evidence and package-level test gates see the same accepted boundary.
	if o.runtime == nil || !o.runtime.ObservesProviderBoundaries() || msg.Role == messages.RoleTool {
		return
	}
	//nolint:exhaustive // only provider boundary events are handled here.
	switch msg.Type {
	case messages.StreamTypeInputItemAdded:
		o.runtime.ProviderInputCommit()
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		o.runtime.ResponseCreate(msg)
	}
}

func (o *observerState) observeSessionLifecycleBoundary(msg messages.StreamMessage) {
	//nolint:exhaustive // only session lifecycle boundaries are handled here.
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		o.sawSessionOpen = true
		o.sessionID = ""
		o.lifecycleProjectionMu.Lock()
		o.activeResponse = false
		o.activeResponseID = ""
		o.completedResponseIDs = make(map[string]struct{})
		o.retiredResponseIDs = make(map[string]struct{})
		o.lifecycleProjectionMu.Unlock()
		o.resetObservedResponseState()
		if v, ok := msg.Value.(*messages.SessionOpenValue); ok && v != nil {
			o.sessionID = v.SessionID
		}
		o.sessionUpdated = false
	case messages.StreamTypeSessionUpdated:
		if !o.sawSessionOpen {
			return
		}
		updatedID := ""
		if v, ok := msg.Value.(*messages.SessionUpdatedValue); ok && v != nil {
			updatedID = v.SessionID
		}
		// Some compatible transports omit the session ID. When both sides
		// provide one, require an exact match to the current connection.
		if o.sessionID != "" && updatedID != "" && o.sessionID != updatedID {
			return
		}
		o.sessionUpdated = true
	}
}

// messageEndCanAdmit keeps provider-authored failures from earning a turn
// credit even when a provider delivered partial output before response.done.
// Empty status and legacy provider-authored completion remain admissible.
func messageEndCanAdmit(value *messages.MessageEndValue) bool {
	if value == nil {
		return false
	}
	status := normalizeTerminalStatus(value.Status)
	if status != "" && status != terminalStatusCompleted {
		return false
	}
	if value.TerminalReason != "" && value.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion && value.TerminalReason != messages.TerminalReasonLoopSynthesizedCompletion {
		return false
	}
	return true
}

// accountToolRoleMessage preserves output accounting for the ToolRunner's
// observable result while keeping that result out of provider response
// ownership and admission state. Tool results are emitted as stream output,
// so dropping them would make the diagnostic byte matrix disagree with the
// rendered session output.
func (o *observerState) accountToolRoleMessage(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	switch v := msg.Value.(type) {
	case *messages.AudioDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityAudio, len(v.Content))
	case *messages.TextDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Content))
	case *messages.TranscriptDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Text))
	case *messages.ToolCallDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.PartialJSON))
	case *messages.ToolCallEndValue:
		if !o.toolDeltaSeen {
			o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.Arguments))
		}
	}
}
