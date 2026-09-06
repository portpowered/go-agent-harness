package live

import (
	"context"
	"errors"
	"fmt"
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

// liveToolContinuation records the provider-side lifecycle of one tool result.
// The model runner acknowledges a result and requests its continuation through
// callbacks on orderedSession, while provider output is observed on the delta
// consumer. Keeping the two halves together at the live boundary prevents a
// transport close or failed response from being mistaken for a clean session
// completion.
type liveToolContinuation struct {
	callID                string
	name                  string
	resultAccepted        bool
	continuationRequested bool
	toolResponseComplete  bool
	outputObserved        bool
	status                string
	code                  string
	detail                string
}

func (h *handle) observeToolLifecycle(msg messages.StreamMessage) error {
	if h == nil {
		return nil
	}
	if isProviderToolCallType(msg.Type) {
		h.observeProviderToolCall(msg)
		if msg.Type != messages.StreamTypeToolCallStart && msg.Type != messages.StreamTypeToolCallDelta {
			h.markContinuationOutput()
		}
		return nil
	}
	if isContinuationOutputType(msg.Type) {
		h.markContinuationOutput()
		return nil
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		if msg.Role == messages.RoleTool {
			h.markToolResponseComplete()
			return nil
		}
		return h.finishToolContinuations(msg)
	}
	return nil
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

// observeToolResult is called only after orderedSession has received a
// successful provider admission for a tool result. requestsContinuation is
// true for complete-message sends whose provider API combines result delivery
// and response creation; stream-only result sends receive the separate
// observeContinuationRequested callback below.
func (h *handle) observeToolResult(callID, name string, requestsContinuation bool) {
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
	state.resultAccepted = true
	if strings.TrimSpace(name) != "" {
		state.name = strings.TrimSpace(name)
	}
	if requestsContinuation {
		state.continuationRequested = true
	}
	h.toolMu.Unlock()
}

// observeContinuationRequested marks the accepted result batch that a
// RESPONSE.CREATE is asking the provider to continue. A response request with
// no accepted results is an ordinary user/audio turn and leaves this ledger
// unchanged.
func (h *handle) observeContinuationRequested() {
	if h == nil {
		return
	}
	h.toolMu.Lock()
	for _, state := range h.toolContinuations {
		if state.resultAccepted {
			state.continuationRequested = true
		}
	}
	h.toolMu.Unlock()
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
		if state.resultAccepted && !state.toolResponseComplete {
			state.toolResponseComplete = true
		}
	}
	h.toolMu.Unlock()
}

func capabilityEvent(sessionID, participantID string, value session.LiveCapabilityEvent) session.LiveEvent {
	copy := value
	return session.LiveEvent{
		Kind:          "browser." + strings.TrimSpace(value.Type),
		SessionID:     sessionID,
		ParticipantID: participantID,
		Timestamp:     value.Timestamp,
		BrowserID:     value.BrowserID,
		TargetID:      value.TargetID,
		Generation:    value.Generation,
		InvocationID:  value.InvocationID,
		State:         value.State,
		Reason:        value.Reason,
		Capability:    &copy,
		Critical:      capabilityEventCritical(value),
	}
}

func capabilityEventCritical(value session.LiveCapabilityEvent) bool {
	typeName := strings.ToLower(strings.TrimSpace(value.Type))
	state := strings.ToLower(strings.TrimSpace(value.State))
	return strings.Contains(typeName, "closed") || strings.Contains(typeName, "disconnect") ||
		strings.Contains(typeName, "error") || strings.Contains(typeName, "failed") ||
		strings.Contains(state, "error") || strings.Contains(state, "failed") ||
		strings.Contains(state, "canceled") || strings.Contains(state, "timed_out")
}
