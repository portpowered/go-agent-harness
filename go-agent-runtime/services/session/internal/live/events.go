package live

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func drainLiveEvents(events <-chan session.LiveEvent, sink session.LiveEventSink, ctx context.Context, sinkErr *error, handle session.LiveHandle) {
	if events == nil {
		return
	}
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if sink == nil || *sinkErr != nil {
				continue
			}
			if err := sink.Publish(ctx, event); err != nil {
				*sinkErr = fmt.Errorf("publish live event: %w", err)
				handle.Cancel(*sinkErr)
			}
		default:
			return
		}
	}
}
func eventFromMessage(sessionID string, msg messages.StreamMessage) session.LiveEvent {
	observed := msg
	event := session.LiveEvent{
		Kind: string(msg.Type), SessionID: sessionID,
		Role: msg.Role, ResponseID: msg.ResponseID, ToolCallID: msg.ToolCallId,
		Message: &observed,
	}
	applyMessagePayload(&event, msg)
	applyMessageSessionID(&event, sessionID, msg)
	return event
}
func applyMessagePayload(event *session.LiveEvent, msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeTextDelta {
		applyTextDelta(event, msg)
		return
	}
	if msg.Type == messages.StreamTypeReasoningDelta {
		event.Text = reasoningDeltaText(msg)
		return
	}
	if msg.Type == messages.StreamTypeTranscriptDelta {
		applyTranscriptDelta(event, msg)
		return
	}
	if msg.Type == messages.StreamTypeTranscriptEnd {
		applyTranscriptEnd(event, msg)
		return
	}
	if msg.Type == messages.StreamTypeToolCallStart {
		event.ToolCallID = toolCallStartID(msg)
		return
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		event.ToolCallID = toolCallEndID(msg)
		return
	}
	if msg.Type == messages.StreamTypeSessionClose {
		applySessionClose(event, msg)
		return
	}
	if msg.Type == messages.StreamTypeError {
		applyError(event, msg)
	}
}
func applyTextDelta(event *session.LiveEvent, msg messages.StreamMessage) {
	event.Kind = string(session.LiveEventText)
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		event.Text = value.Content
	}
}

func reasoningDeltaText(msg messages.StreamMessage) string {
	value, ok := msg.Value.(*messages.ReasoningDeltaValue)
	if !ok || value == nil {
		return ""
	}
	return value.Content
}
func applyTranscriptDelta(event *session.LiveEvent, msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.TranscriptDeltaValue)
	if !ok || value == nil {
		return
	}
	event.Text = value.Text
	event.ItemID = value.ItemID
}
func applyTranscriptEnd(event *session.LiveEvent, msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.TranscriptEndValue)
	if !ok || value == nil {
		return
	}
	event.Text = value.FullText
	event.ItemID = value.ItemID
}
func toolCallStartID(msg messages.StreamMessage) string {
	value, ok := msg.Value.(*messages.ToolCallStartValue)
	if !ok || value == nil {
		return ""
	}
	return value.ToolCallID
}
func toolCallEndID(msg messages.StreamMessage) string {
	value, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil {
		return ""
	}
	return value.ToolCallID
}
func applySessionClose(event *session.LiveEvent, msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return
	}
	event.Reason = value.Reason
	copy := *value
	event.Terminal = &copy
}
func applyError(event *session.LiveEvent, msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.ErrorValue)
	if !ok || value == nil {
		return
	}
	event.Error = value.Err
	if event.Error == nil && value.Message != "" {
		event.Error = errors.New(value.Message)
	}
	event.Critical = value.IsTerminal()
}
func applyMessageSessionID(event *session.LiveEvent, sessionID string, msg messages.StreamMessage) {
	if value, ok := msg.Value.(*messages.SessionOpenValue); ok && value != nil {
		event.SessionID = value.SessionID
	}
	if event.SessionID == "" {
		event.SessionID = sessionID
	}
}

func (h *handle) observeOutput(msg messages.StreamMessage) {
	if h == nil || !liveOutputMessage(msg) {
		return
	}
	h.mu.Lock()
	h.outputObserved = true
	h.mu.Unlock()
}

func liveOutputMessage(msg messages.StreamMessage) bool {
	if msg.Role == messages.RoleUser {
		return false
	}
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		return value != nil && strings.TrimSpace(value.Content) != ""
	case *messages.TranscriptDeltaValue:
		return value != nil && strings.TrimSpace(value.Text) != ""
	case *messages.TranscriptEndValue:
		return value != nil && strings.TrimSpace(value.FullText) != ""
	default:
		return liveOutputBytes(value)
	}
}

func liveOutputBytes(value any) bool {
	switch value := value.(type) {
	case *messages.AudioDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.ImageDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.VideoDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.FileDeltaValue:
		return value != nil && len(value.Content) > 0
	case *messages.EmbeddingDeltaValue:
		return value != nil && len(value.Content) > 0
	default:
		return false
	}
}
func terminalValueForMessage(msg messages.StreamMessage) *messages.SessionCloseValue {
	if msg.Type == messages.StreamTypeSessionClose {
		candidate, ok := msg.Value.(*messages.SessionCloseValue)
		if !ok || candidate == nil {
			return nil
		}
		copy := *candidate
		return &copy
	}
	if msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool {
		return nil
	}
	candidate, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || candidate == nil {
		return nil
	}
	return sessionCloseValueFromMessageEnd(candidate)
}

func sessionCloseValueFromMessageEnd(value *messages.MessageEndValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	return &messages.SessionCloseValue{
		Type:               "session_close",
		Classification:     "",
		TerminalReason:     value.TerminalReason,
		TerminalProvenance: value.TerminalProvenance,
		OutputState:        value.OutputState,
	}
}

type liveToolContinuation struct {
	callID                string
	name                  string
	resultAccepted        bool
	continuationRequested bool
	toolOutputObserved    bool
	toolResponseComplete  bool
	outputObserved        bool
	pendingTerminal       bool
	status                string
	code                  string
	detail                string
}

func (h *handle) observeToolLifecycle(msg messages.StreamMessage) (error, bool) {
	if h == nil {
		return nil, false
	}
	if isProviderToolCallType(msg.Type) {
		h.observeProviderToolCall(msg)
		if msg.Type != messages.StreamTypeToolCallStart && msg.Type != messages.StreamTypeToolCallDelta {
			h.markContinuationOutput()
		}
		return nil, false
	}
	if isContinuationOutputType(msg.Type) {
		if msg.Role == messages.RoleTool {
			h.observeToolResponseOutput(msg.ToolCallId)
			return nil, false
		}
		h.markContinuationOutput()
		return nil, false
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		if msg.Role == messages.RoleTool {
			h.markToolResponseComplete()
			return h.finishDeferredToolContinuations()
		}
		return h.finishToolContinuations(msg)
	}
	return nil, false
}

func isProviderToolCallType(kind messages.StreamMessageType) bool {
	return kind == messages.StreamTypeToolCallStart || kind == messages.StreamTypeToolCallDelta || kind == messages.StreamTypeToolCallEnd
}

func isContinuationOutputType(kind messages.StreamMessageType) bool {
	if kind == messages.StreamTypeRefusal {
		return true
	}
	name := string(kind)
	if !strings.HasSuffix(name, ".DELTA") && !strings.HasSuffix(name, ".END") {
		return false
	}
	return kind != messages.StreamTypeMessageEnd && kind != messages.StreamTypeToolCallEnd && kind != messages.StreamTypeToolCallDelta
}

func (h *handle) observeProviderToolCall(msg messages.StreamMessage) {
	callID, name := providerToolCallIdentity(msg)
	if callID == "" {
		return
	}
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	if name != "" {
		state.name = name
	}
	h.toolMu.Unlock()
}

func (h *handle) observeToolResponseOutput(callID string) {
	callID = strings.TrimSpace(callID)
	if h == nil || callID == "" {
		return
	}
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	state.toolOutputObserved = true
	h.toolMu.Unlock()
}

func providerToolCallIdentity(msg messages.StreamMessage) (string, string) {
	callID, name := msg.ToolCallId, ""
	switch value := msg.Value.(type) {
	case *messages.ToolCallStartValue:
		if value != nil {
			if value.ToolCallID != "" {
				callID = value.ToolCallID
			}
			name = value.Name
		}
	case *messages.ToolCallEndValue:
		if value != nil {
			if value.ToolCallID != "" {
				callID = value.ToolCallID
			}
			name = value.Name
		}
	}
	return strings.TrimSpace(callID), strings.TrimSpace(name)
}

func (h *handle) observeToolResult(callID, name string, requestsContinuation bool) {
	_ = h.beginToolResultAdmission(callID, name, requestsContinuation)
}

// beginToolResultAdmission records the result before handing it to the provider.
// A provider may publish its response synchronously from Send, so observing only
// after Send returns loses the causal link between that response and this result.
// The returned closure restores only the fields changed by this admission when
// the provider rejects the send.
func (h *handle) beginToolResultAdmission(callID, name string, requestsContinuation bool) func() {
	callID = strings.TrimSpace(callID)
	if h == nil || callID == "" {
		return func() {}
	}
	name = strings.TrimSpace(name)
	h.toolMu.Lock()
	state := h.toolContinuations[callID]
	existed := state != nil
	if state == nil {
		state = &liveToolContinuation{callID: callID}
		h.toolContinuations[callID] = state
	}
	previousAccepted := state.resultAccepted
	previousName := state.name
	previousRequested := state.continuationRequested
	state.resultAccepted = true
	if name != "" {
		state.name = name
	}
	if requestsContinuation {
		state.continuationRequested = true
	}
	h.toolMu.Unlock()
	return func() {
		h.toolMu.Lock()
		if current := h.toolContinuations[callID]; current == state {
			current.resultAccepted = previousAccepted
			current.name = previousName
			current.continuationRequested = previousRequested
			if !existed && !current.toolOutputObserved && !current.toolResponseComplete && !current.outputObserved &&
				!current.pendingTerminal && current.status == "" && current.code == "" && current.detail == "" {
				delete(h.toolContinuations, callID)
			}
		}
		h.toolMu.Unlock()
	}
}

func (h *handle) unresolvedToolResultsError() error {
	if h == nil {
		return nil
	}
	h.toolMu.Lock()
	ids := make([]string, 0, len(h.toolContinuations))
	for callID, state := range h.toolContinuations {
		if state != nil && !state.resultAccepted && strings.TrimSpace(callID) != "" {
			ids = append(ids, callID)
		}
	}
	h.toolMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	for index, id := range ids {
		ids[index] = strings.TrimSpace(id)
	}
	sort.Strings(ids)
	return &session.LiveUnresolvedToolResultsError{CallIDs: ids}
}

func (h *handle) observeContinuationRequested() {
	_ = h.beginContinuationAdmission()
}

// beginContinuationAdmission marks only results that have not already requested
// a continuation. Its rollback therefore cannot erase an earlier accepted
// continuation when a later RequestResponse call is rejected.
func (h *handle) beginContinuationAdmission() func() {
	if h == nil {
		return func() {}
	}
	h.toolMu.Lock()
	changed := make([]*liveToolContinuation, 0, len(h.toolContinuations))
	for _, state := range h.toolContinuations {
		if state.resultAccepted && !state.continuationRequested {
			state.continuationRequested = true
			changed = append(changed, state)
		}
	}
	h.toolMu.Unlock()
	return func() {
		h.toolMu.Lock()
		for _, state := range changed {
			state.continuationRequested = false
		}
		h.toolMu.Unlock()
	}
}

func (h *handle) markContinuationOutput() {
	if h == nil {
		return
	}
	h.toolMu.Lock()
	for _, state := range h.toolContinuations {
		if state.resultAccepted && state.continuationRequested {
			state.outputObserved = true
		}
	}
	h.toolMu.Unlock()
}

func (h *handle) markToolResponseComplete() {
	if h == nil {
		return
	}
	h.toolMu.Lock()
	for _, state := range h.toolContinuations {
		if state.toolOutputObserved && !state.toolResponseComplete {
			state.toolResponseComplete = true
		}
	}
	h.toolMu.Unlock()
}
