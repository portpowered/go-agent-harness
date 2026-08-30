package services

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"strings"
)

func (o *sessionProgressObserver) observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	if o.streamObserver != nil {
		o.streamObserver(msg)
	}
	// Input-audio transcription belongs to the customer input stream. It must
	// remain observable, but it cannot open, reset, or complete an assistant
	// response—especially when a provider interleaves recognition with output.
	if msg.Role == messages.RoleUser && (msg.Type == messages.StreamTypeTranscriptStart || msg.Type == messages.StreamTypeTranscriptDelta || msg.Type == messages.StreamTypeTranscriptEnd) {
		if value, ok := msg.Value.(*messages.TranscriptDeltaValue); ok && value != nil {
			o.account(metrics.DirectionInput, metrics.ModalityText, len(value.Text))
		}
		return
	}
	// ToolRunner delivery is an internal bridge between the provider tool call
	// and the next provider response. It is observable to callers, but it is
	// not a provider response boundary and must not reset response output,
	// consume a scheduled slot, or become the owner of a continuation.
	if msg.Role == messages.RoleTool {
		o.accountToolRoleMessage(msg)
		return
	}
	msgResponseID := strings.TrimSpace(msg.ResponseID)
	responseLifecycleID := msgResponseID
	newResponseBoundary := false
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		// The normalized provider boundary is active from response creation
		// (MESSAGE.START) or the compatible audio-only start through its
		// terminal MESSAGE.END. Use the envelope type as the source of truth so
		// a provider with an empty value still participates in scheduling.
		newResponseBoundary = o.beginObservedResponse(msgResponseID)
		if !o.responseEventBelongsToActive(msgResponseID) {
			return
		}
		if newResponseBoundary && msg.Role != messages.RoleTool {
			o.bindScheduledResponseBoundary(msgResponseID)
		}
	case messages.StreamTypeMessageEnd:
		// A terminal event is allowed to advance state only for the active
		// response owner. A late terminal for a previous response remains
		// observable through the stream observer but cannot complete a turn.
		if !o.ownsObservedResponseEnd(msgResponseID) {
			return
		}
		if msgResponseID != "" && o.activeResponse && o.activeResponseID == "" && !o.adoptObservedResponseID(msgResponseID) {
			return
		}
		if !o.activeResponse && msgResponseID != "" {
			newResponseBoundary = o.beginObservedResponse(msgResponseID)
			if newResponseBoundary {
				o.bindScheduledResponseBoundary(msgResponseID)
			}
		} else if !o.activeResponse {
			// A legacy provider may expose only the terminal boundary. Bind it to
			// the next dispatched scheduled input when one is available; an
			// unrelated prompt/session terminal has no slot to consume.
			o.bindScheduledTerminalOnly("")
		}
		if responseLifecycleID == "" {
			responseLifecycleID = o.activeResponseID
		}
	case messages.StreamTypeSessionClose:
		// Keep the active response owner while draining already-queued provider
		// output. A transport can deliver SESSION.CLOSE before the response's
		// terminal event; clearing the owner here would make that terminal look
		// like a new response and discard its output ledger.
	default:
		if responseScopedStreamType(msg.Type) && !o.responseEventBelongsToActive(msgResponseID) {
			return
		}
	}
	if responseLifecycleID == "" {
		responseLifecycleID = o.activeResponseID
	}
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		o.sawSessionOpen = true
		o.sessionID = ""
		o.activeResponse = false
		o.activeResponseID = ""
		o.completedResponseIDs = make(map[string]struct{})
		o.retiredResponseIDs = make(map[string]struct{})
		o.resetObservedResponseState()
		if v, ok := msg.Value.(*messages.SessionOpenValue); ok && v != nil {
			o.sessionID = v.SessionID
		}
		o.sessionUpdated = false
	case messages.StreamTypeSessionUpdated:
		if !o.sawSessionOpen {
			break
		}
		updatedID := ""
		if v, ok := msg.Value.(*messages.SessionUpdatedValue); ok && v != nil {
			updatedID = v.SessionID
		}
		// Some compatible transports omit the session ID. When both sides
		// provide one, require an exact match to the current connection.
		if o.sessionID != "" && updatedID != "" && o.sessionID != updatedID {
			break
		}
		o.sessionUpdated = true
	}

	switch v := msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.sawSessionOpen = true
	case *messages.MessageStartValue:
		if newResponseBoundary {
			o.resetObservedResponseState()
		}
	case *messages.TextStartValue, *messages.AudioStartValue, *messages.ReasoningStartValue,
		*messages.ImageStartValue, *messages.VideoStartValue, *messages.FileStartValue,
		*messages.EmbeddingStartValue, *messages.TranscriptStartValue:
		// Compatible providers may omit MESSAGE.START between persistent
		// responses. Any content-start boundary is enough to distinguish a new
		// response from a duplicate MESSAGE.END for the previous one.
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
	case *messages.AudioDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityAudio, len(v.Content))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Content) > 0 {
			o.responseOutputAudioBytes += uint64(len(v.Content))
		}
		if len(v.Content) > 0 && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TextDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Content))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Content) > 0 {
			o.responseOutputTextBytes += uint64(len(v.Content))
		}
		if strings.TrimSpace(v.Content) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TranscriptDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Text))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Text) > 0 {
			o.responseOutputTextBytes += uint64(len(v.Text))
		}
		if strings.TrimSpace(v.Text) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TranscriptEndValue:
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.FullText) > 0 {
			o.responseOutputTextBytes += uint64(len(v.FullText))
		}
		if strings.TrimSpace(v.FullText) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.ToolCallStartValue:
		o.observeProviderToolCallStartForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, responseLifecycleID)
		o.toolDeltaSeen = false
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallDeltaValue:
		o.observeProviderToolCallStartForResponse(strings.TrimSpace(msg.ToolCallId), "", responseLifecycleID)
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.PartialJSON))
		o.toolDeltaSeen = true
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallEndValue:
		callID := firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId)
		o.observeProviderToolCallWithIDForResponse(callID, v.Name, responseLifecycleID)
		if !o.toolResultsEnabledForObservation() {
			o.emitToolCallRecord(v)
		}
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		// A complete, correlated tool call is provider output even when the
		// caller has no executor. Tool-enabled sessions still keep the existing
		// intermediate-call/continuation lifecycle below; this flag only
		// prevents a valid tool-only response from being mistaken for empty.
		if strings.TrimSpace(callID) != "" && strings.TrimSpace(v.Name) != "" {
			o.responseActionableTool = true
		}
		if o.toolResultsEnabled {
			o.providerToolCallSeen = true
		}
		o.toolStateMu.Unlock()
		if !o.toolDeltaSeen {
			o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.Arguments))
		}
		o.toolDeltaSeen = false
	case *messages.MessageEndValue:
		cancelled := isLocalResponseCancellation(v)
		o.noteProviderUsage(v.Usage)
		o.setAssistantResponseDone(false)
		outputPresent := o.responseHasAdmissibleOutput()
		candidate := o.observeProviderMessageEndForResponse(msg.Role, v, responseLifecycleID, outputPresent)
		o.noteScheduledResponseTerminal(responseLifecycleID, v)
		o.rememberRateLimitRetryCandidate(msgResponseID, responseLifecycleID, v)
		// A cancelled response can have output queued before the cancellation
		// boundary. It is still an interrupted lifecycle disposition, never a
		// normal assistant turn, and must not satisfy output admission.
		admitted := !cancelled && candidate && outputPresent && messageEndCanAdmit(v)
		if admitted && o.turnAdmission != nil {
			admitted = o.turnAdmission(msg)
		}
		o.setAssistantResponseDone(admitted)
		o.toolStateMu.Lock()
		o.messageEndAdmitted = admitted
		o.toolStateMu.Unlock()
		if admitted {
			o.noteScheduledResponseDisposition(responseLifecycleID, scheduledAudioResponseCompleted)
			o.completeTurn()
			if o.admittedTurnObserver != nil {
				o.admittedTurnObserver(msg)
			}
		} else if cancelled {
			o.noteScheduledResponseDisposition(responseLifecycleID, scheduledAudioResponseCancelled)
		}
		o.finishObservedResponse(responseLifecycleID)
	case *messages.ErrorValue:
		o.captureFailureFromError(v)
	case *messages.SessionCloseValue:
		o.captureFailureFromClose(v)
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
	if status != "" && status != "completed" {
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
func (o *sessionProgressObserver) accountToolRoleMessage(msg messages.StreamMessage) {
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
