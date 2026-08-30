package grok

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// wireEvent is the JSON shape sent/received over the Grok WebSocket.
// The "type" field determines the event kind; remaining fields are
// event-specific payload. This structure matches the OpenAI Realtime API
// wire format that Grok follows.
type wireEvent struct {
	Type  string                     `json:"type"`
	Extra map[string]json.RawMessage `json:"-"`
}

const (
	grokSessionEventResponseAudioDelta           models.SessionEventType = "response.audio.delta"
	grokSessionEventResponseAudioDone            models.SessionEventType = "response.audio.done"
	grokSessionEventResponseAudioTranscriptDelta models.SessionEventType = "response.audio_transcript.delta"
	grokSessionEventResponseAudioTranscriptDone  models.SessionEventType = "response.audio_transcript.done"
	grokSessionEventResponseTextDelta            models.SessionEventType = "response.text.delta"
	grokSessionEventResponseTextDone             models.SessionEventType = "response.text.done"
	grokMaxStatusDetailBytes                                             = 256
)

// MarshalJSON produces a flat JSON object with "type" plus any Extra fields.
func (w wireEvent) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(w.Extra)+1)
	for k, v := range w.Extra {
		m[k] = v
	}
	typeBytes, _ := json.Marshal(w.Type)
	m["type"] = typeBytes
	return json.Marshal(m)
}

// parseServerEvent converts raw WebSocket JSON into a gateway SessionEvent.
// The "type" field is extracted and the remaining payload is kept as Data.
func parseServerEvent(raw []byte) (models.SessionEvent, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return models.SessionEvent{}, fmt.Errorf("unmarshal event type: %w", err)
	}
	if envelope.Type == "" {
		return models.SessionEvent{}, fmt.Errorf("event missing type field")
	}

	eventType := models.SessionEventType(envelope.Type)

	// Keep the full payload as Data (minus the type field is unnecessary
	// to strip — consumers use the typed SessionEventType for dispatch).
	return models.SessionEvent{
		Type: eventType,
		Data: raw,
	}, nil
}

// translateInbound converts a server-sent SessionEvent into zero or more
// agent loop StreamMessages. This is the canonical inbound translation for
// the Grok provider — consumers only see generic StreamMessage types.
func translateInbound(event models.SessionEvent) []messages.StreamMessage {
	responseID := responseEventID(event.Data)
	switch event.Type {
	case models.SessionEventSessionCreated:
		// session.created from the server signals the session is established.
		// Emit SESSION.OPEN (agent loop signal) and SESSION.CREATED (carries server config).
		sessionID := extractStringField(event.Data, "session_id")
		model := extractStringField(event.Data, "model")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(sessionID, "audio_inference")},
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(sessionID, model)},
		}

	case models.SessionEventSessionUpdated:
		// session.updated confirms a session configuration update.
		sessionID := extractStringField(event.Data, "session_id")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue(sessionID)},
		}

	case models.SessionEventSessionClosed:
		sessionID := extractStringField(event.Data, "session_id")
		reason := extractStringField(event.Data, "reason")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
				sessionID,
				reason,
				providers.ErrorClassTransport,
				messages.TerminalReasonProviderClose,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNotApplicable,
			)},
		}

	case models.SessionEventResponseOutputAudioDelta, grokSessionEventResponseAudioDelta:
		audioBytes := extractAudioBytes(event.Data)
		if audioBytes == nil {
			return nil
		}
		return []messages.StreamMessage{
			{Type: messages.StreamTypeAudioDelta, ResponseID: responseID, Value: messages.NewAudioDeltaValue(audioBytes)},
		}

	case models.SessionEventResponseOutputAudioDone, grokSessionEventResponseAudioDone:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeAudioEnd, ResponseID: responseID, Value: messages.NewAudioEndValue()},
		}

	case models.SessionEventResponseOutputAudioTranscriptDelta, grokSessionEventResponseAudioTranscriptDelta:
		text := extractStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTranscriptDeltaValue(text)},
		}

	case models.SessionEventResponseOutputAudioTranscriptDone, grokSessionEventResponseAudioTranscriptDone:
		text := extractStringField(event.Data, "transcript")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTranscriptEndValue(text)},
		}
	case models.SessionEventConversationItemInputAudioTranscriptionDelta:
		text := extractStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue(text)},
		}
	case models.SessionEventConversationItemInputAudioTranscriptionCompleted:
		text := extractStringField(event.Data, "transcript")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue(text)},
		}

	case models.SessionEventResponseTextDelta, grokSessionEventResponseTextDelta:
		text := extractStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, ResponseID: responseID, Value: messages.NewTextDeltaValue(text)},
		}

	case models.SessionEventResponseTextDone, grokSessionEventResponseTextDone:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeTextEnd, ResponseID: responseID, Value: messages.NewTextEndValue()},
		}

	case models.SessionEventResponseFunctionCallArgumentsDelta:
		partial := extractStringField(event.Data, "delta")
		if partial == "" {
			return nil
		}
		return []messages.StreamMessage{
			{Type: messages.StreamTypeToolCallDelta, ResponseID: responseID, Value: messages.NewToolCallDeltaValue(partial)},
		}

	case models.SessionEventResponseFunctionCallArgumentsDone:
		callID := extractStringField(event.Data, "call_id")
		name := extractStringField(event.Data, "name")
		args := extractStringField(event.Data, "arguments")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeToolCallEnd, ResponseID: responseID, Value: messages.NewToolCallEndValue(callID, name, args)},
		}

	case models.SessionEventResponseCreated:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		}

	case models.SessionEventResponseDone:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeMessageEnd, ResponseID: responseID, Value: grokResponseDoneMessageEnd(event.Data)},
		}

	case models.SessionEventInputAudioBufferSpeechStarted:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		}

	case models.SessionEventInputAudioBufferSpeechStopped:
		return []messages.StreamMessage{
			{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
		}

	case models.SessionEventError:
		msg := extractStringField(event.Data, "message")
		if msg == "" {
			msg = "session error"
		}
		value := messages.NewErrorValueWithTerminal(
			msg,
			sessionErrorClassification(event.Data),
			messages.TerminalReasonTerminalFailure,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		)
		value.ErrorType = extractErrorDetailField(event.Data, "type")
		value.Code = extractErrorDetailField(event.Data, "code")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeError, Value: value},
		}

	default:
		// Unknown or informational events (session.updated, etc.) are silently dropped.
		return nil
	}
}

// grokResponseDoneMessageEnd preserves the response terminal status for the
// shared session lifecycle observer. Grok follows the OpenAI Realtime event
// shape, but older fixtures may omit status and therefore retain the legacy
// zero-value MESSAGE.END behavior.
func grokResponseDoneMessageEnd(data json.RawMessage) *messages.MessageEndValue {
	status := strings.ToLower(strings.TrimSpace(firstGrokStringField(data, "response.status", "status")))
	value := messages.NewMessageEndValue(messages.TokenUsage{})
	value.Status = status
	value.StatusDetails = grokResponseDoneStatusDetails(data)
	value.ProviderErrorCode = grokResponseDoneErrorCode(data)
	value.ProviderErrorMessage = grokResponseDoneErrorMessage(data)
	if status == "" {
		return value
	}

	value.TerminalProvenance = messages.TerminalProvenanceProvider
	value.TerminalSource = messages.TerminalSourceProvider
	switch status {
	case "completed":
		value.TerminalReason = messages.TerminalReasonProviderAuthoredCompletion
		value.OutputState = messages.TerminalOutputComplete
	case "cancelled", "canceled":
		value.TerminalReason = messages.TerminalReasonCancellation
		value.OutputState = messages.TerminalOutputNone
	default:
		value.TerminalReason = messages.TerminalReasonTerminalFailure
		value.OutputState = messages.TerminalOutputNone
	}
	return value
}

func grokResponseDoneStatusDetails(data json.RawMessage) string {
	parts := make([]string, 0, 4)
	appendField := func(label string, paths ...string) {
		value := boundedGrokStatusDetail(firstGrokStringField(data, paths...))
		if value == "" {
			return
		}
		for _, existing := range parts {
			if existing == label+"="+value {
				return
			}
		}
		parts = append(parts, label+"="+value)
	}
	appendField("reason", "response.status_details.reason", "status_details.reason")
	appendField("type", "response.status_details.type", "status_details.type")
	appendField("code", "response.status_details.error.code", "status_details.error.code", "response.status_details.code", "status_details.code")
	appendField("message", "response.status_details.error.message", "status_details.error.message", "response.status_details.message", "status_details.message")
	return strings.Join(parts, ", ")
}

func grokResponseDoneErrorCode(data json.RawMessage) string {
	return boundedGrokStatusDetail(firstGrokStringField(
		data,
		"response.status_details.error.code",
		"status_details.error.code",
		"response.status_details.code",
		"status_details.code",
	))
}

func grokResponseDoneErrorMessage(data json.RawMessage) string {
	return boundedGrokStatusDetail(firstGrokStringField(
		data,
		"response.status_details.error.message",
		"status_details.error.message",
		"response.status_details.message",
		"status_details.message",
	))
}

func boundedGrokStatusDetail(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= grokMaxStatusDetailBytes {
		return value
	}
	value = value[:grokMaxStatusDetailBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// translateOutbound converts an agent loop StreamMessage into a SessionEvent
// for transmission to the Grok server. Returns an empty SessionEvent and false
// if the message type is not applicable for outbound transmission.
func translateOutbound(msg messages.StreamMessage) (models.SessionEvent, bool) {
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		v, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || v == nil {
			return models.SessionEvent{}, false
		}
		encoded := base64.StdEncoding.EncodeToString(v.Content)
		return models.NewAudioBufferAppendEvent(encoded), true

	case messages.StreamTypeResponseCancel:
		return models.NewResponseCancelEvent(), true

	case messages.StreamTypeMessageEnd:
		// Commit audio buffer at the end of an audio message.
		return models.NewAudioBufferCommitEvent(), true

	case messages.StreamTypeResponseCreate:
		// Tool-result delivery and response creation are separate boundaries.
		// The result item is queued first; this control event starts exactly one
		// grounded continuation without committing a new user-audio turn.
		v, ok := msg.Value.(*messages.ResponseCreateValue)
		if !ok || v == nil {
			return models.SessionEvent{}, false
		}
		return models.NewResponseCreateEventWithInstructions(v.Instructions), true

	case messages.StreamTypeTextDelta:
		// Text input: send as conversation.item.create with a user message.
		v, ok := msg.Value.(*messages.TextDeltaValue)
		if !ok || v == nil {
			return models.SessionEvent{}, false
		}
		data, _ := json.Marshal(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": v.Content},
				},
			},
		})
		return models.SessionEvent{Type: "conversation.item.create", Data: data}, true

	case messages.StreamTypeSessionUpdate:
		// Outbound session update: send as session.update wire event.
		v, ok := msg.Value.(*messages.SessionUpdateValue)
		if !ok || v == nil {
			return models.SessionEvent{}, false
		}
		data, _ := json.Marshal(map[string]any{
			"session": map[string]any{
				"instructions": v.Instructions,
			},
		})
		return models.NewSessionUpdateEvent(data), true

	case messages.StreamTypeToolCallEnd:
		// Tool result: send as conversation.item.create with function_call_output.
		v, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || v == nil {
			return models.SessionEvent{}, false
		}
		data, _ := json.Marshal(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type":    "function_call_output",
				"call_id": v.ToolCallID,
				"output":  v.Arguments,
			},
		})
		return models.SessionEvent{Type: "conversation.item.create", Data: data}, true

	default:
		return models.SessionEvent{}, false
	}
}

// extractAudioBytes decodes a base64 "delta" field from a JSON payload.
func extractAudioBytes(data json.RawMessage) []byte {
	encoded := extractStringField(data, "delta")
	if encoded == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	return decoded
}

// extractStringField extracts a string field from a JSON object.
func extractStringField(data json.RawMessage, field string) string {
	if len(data) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func firstGrokStringField(data json.RawMessage, paths ...string) string {
	for _, path := range paths {
		if strings.Contains(path, ".") {
			if value := extractDottedStringField(data, path); value != "" {
				return value
			}
			continue
		}
		if value := extractStringField(data, path); value != "" {
			return value
		}
	}
	return ""
}

func responseEventID(data json.RawMessage) string {
	if id := extractStringField(data, "response_id"); id != "" {
		return id
	}
	return extractDottedStringField(data, "response.id")
}

// sessionErrorClassification refines the public classification for a Grok
// session error event from its nested wire error type/code fields, falling
// back to a generic provider rejection.
func sessionErrorClassification(data json.RawMessage) string {
	return providers.SessionErrorClassification(
		extractErrorDetailField(data, "type"),
		extractErrorDetailField(data, "code"),
	)
}

// extractErrorDetailField reads one string detail of a session error payload,
// preferring the OpenAI-style nested error object and tolerating flattened
// payloads that carry error_type or error_code directly.
func extractErrorDetailField(data json.RawMessage, field string) string {
	if value := extractDottedStringField(data, "error."+field); value != "" {
		return value
	}
	return extractStringField(data, "error_"+field)
}

// extractDottedStringField resolves a dotted key path such as "error.type"
// through nested JSON objects and returns the terminal string value.
func extractDottedStringField(data json.RawMessage, path string) string {
	parts := strings.Split(path, ".")
	current := data
	for i, part := range parts {
		if i == len(parts)-1 {
			return extractStringField(current, part)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(current, &m); err != nil {
			return ""
		}
		child, ok := m[part]
		if !ok {
			return ""
		}
		current = child
	}
	return ""
}
