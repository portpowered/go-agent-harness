package eventcodec

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func FromMessage(sessionID string, msg messages.StreamMessage) session.LiveEvent {
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

func OutputMessage(msg messages.StreamMessage) bool {
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
func TerminalValue(msg messages.StreamMessage) *messages.SessionCloseValue {
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
