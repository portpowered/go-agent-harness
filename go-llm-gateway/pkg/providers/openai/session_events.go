package openai

// This file owns OpenAI Realtime event parsing and inbound/outbound translation, including audio decoding and nested event-field helpers.
import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const conversationItemCreateEvent = models.SessionEventType("conversation.item.create")

func parseRealtimeServerEvent(raw []byte) (models.SessionEvent, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return models.SessionEvent{}, fmt.Errorf("unmarshal event type: %w", err)
	}
	if envelope.Type == "" {
		return models.SessionEvent{}, fmt.Errorf("event missing type field")
	}
	return models.SessionEvent{Type: models.SessionEventType(envelope.Type), Data: raw}, nil
}

func realtimeInboundMessages(event models.SessionEvent) []messages.StreamMessage {
	switch event.Type {
	case models.SessionEventSessionCreated:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		model := firstStringField(event.Data, "model", "session.model")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(sessionID, "audio_inference")},
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(sessionID, model)},
		}
	case models.SessionEventSessionUpdated:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue(sessionID)},
		}
	case models.SessionEventInputAudioBufferSpeechStarted:
		return []messages.StreamMessage{{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()}}
	case models.SessionEventInputAudioBufferSpeechStopped:
		return []messages.StreamMessage{{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()}}
	case models.SessionEventSessionClosed:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		reason := firstStringField(event.Data, "reason", "session.reason")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
				sessionID,
				reason,
				string(messages.TerminalReasonProviderClose),
				messages.TerminalReasonProviderClose,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNotApplicable,
			)},
		}
	case models.SessionEventResponseCreated:
		return []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}}
	case models.SessionEventResponseDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}}
	case models.SessionEventResponseOutputItemAdded:
		itemType := firstStringField(event.Data, "item.type")
		if itemType != "function_call" {
			return nil
		}
		callID := firstStringField(event.Data, "item.call_id", "item.id")
		name := firstStringField(event.Data, "item.name")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallStart,
			ToolCallId: callID,
			Value:      messages.NewToolCallStartValue(callID, name),
		}}
	case models.SessionEventResponseTextDelta:
		text := firstStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}}
	case models.SessionEventResponseTextDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}}
	case models.SessionEventResponseOutputAudioDelta:
		audioBytes := realtimeAudioBytes(event.Data)
		if audioBytes == nil {
			return nil
		}
		return []messages.StreamMessage{{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValueWithMediaType(audioBytes, realtimeAudioMediaType(event.Data)),
		}}
	case models.SessionEventResponseOutputAudioDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()}}
	case models.SessionEventResponseOutputAudioTranscriptDelta:
		text := firstStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue(text)}}
	case models.SessionEventResponseOutputAudioTranscriptDone:
		text := firstStringField(event.Data, "transcript")
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue(text)}}
	case models.SessionEventConversationItemInputAudioTranscriptionDelta:
		text := firstStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue(text)}}
	case models.SessionEventConversationItemInputAudioTranscriptionCompleted:
		text := firstStringField(event.Data, "transcript")
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue(text)}}
	case models.SessionEventResponseFunctionCallArgumentsDelta:
		partial := firstStringField(event.Data, "delta")
		if partial == "" {
			return nil
		}
		callID := firstStringField(event.Data, "call_id", "item.call_id", "item_id")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallDelta,
			ToolCallId: callID,
			Value:      messages.NewToolCallDeltaValue(partial),
		}}
	case models.SessionEventResponseFunctionCallArgumentsDone:
		callID := firstStringField(event.Data, "call_id", "item.call_id", "item_id")
		name := firstStringField(event.Data, "name", "item.name")
		args := firstStringField(event.Data, "arguments")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallEnd,
			ToolCallId: callID,
			Value:      messages.NewToolCallEndValue(callID, name, args),
		}}
	case models.SessionEventError:
		msg := firstStringField(event.Data, "message", "error.message")
		if msg == "" {
			msg = "session error"
		}
		value := messages.NewErrorValueWithTerminal(
			msg,
			providers.SessionErrorClassification(
				firstStringField(event.Data, "error.type"),
				firstStringField(event.Data, "error.code"),
			),
			messages.TerminalReasonTerminalFailure,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		)
		value.ErrorType = firstStringField(event.Data, "error.type")
		value.Code = firstStringField(event.Data, "error.code")
		value.Param = firstStringField(event.Data, "error.param")
		value.EventID = firstStringField(event.Data, "error.event_id")
		return []messages.StreamMessage{{
			Type:  messages.StreamTypeError,
			Value: value,
		}}
	default:
		return nil
	}
}

func realtimeOutboundEvents(msg messages.StreamMessage) ([]models.SessionEvent, bool) {
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		v, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || v == nil {
			return nil, false
		}
		return []models.SessionEvent{models.NewAudioBufferAppendEvent(base64.StdEncoding.EncodeToString(v.Content))}, true
	case messages.StreamTypeResponseCancel:
		// RESPONSE.CANCEL is the provider-facing boundary for barge-in. Keep it
		// as a single wire event so cancellation is ordered before the
		// interrupting audio append reaches the Realtime session.
		return []models.SessionEvent{models.NewResponseCancelEvent()}, true
	case messages.StreamTypeMessageEnd:
		// End-of-turn: commit the input audio buffer and explicitly request a
		// response so finite client-side audio sources (file --audio-in)
		// elicit output even without server-side VAD.
		return []models.SessionEvent{
			models.NewAudioBufferCommitEvent(),
			models.NewResponseCreateEvent(),
		}, true
	case messages.StreamTypeResponseCreate:
		// Tool-result delivery and response creation are separate boundaries.
		// The result item is queued first; this control event starts exactly one
		// grounded continuation without committing a new user-audio turn.
		// Tool results are delivered as conversation items without a response
		// request. Audio-only turns have no user text event to trigger the
		// continuation, so the model runner sends this explicit control event.
		if v, ok := msg.Value.(*messages.ResponseCreateValue); !ok || v == nil {
			return nil, false
		}
		return []models.SessionEvent{models.NewResponseCreateEvent()}, true
	case messages.StreamTypeTextDelta:
		v, ok := msg.Value.(*messages.TextDeltaValue)
		if !ok || v == nil {
			return nil, false
		}
		data, _ := json.Marshal(map[string]any{
			"item": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": v.Content},
				},
			},
		})
		return []models.SessionEvent{
			{Type: conversationItemCreateEvent, Data: data},
			models.NewResponseCreateEvent(),
		}, true
	case messages.StreamTypeSessionUpdate:
		v, ok := msg.Value.(*messages.SessionUpdateValue)
		if !ok || v == nil {
			return nil, false
		}
		update := map[string]any{}
		if v.Model != "" {
			update["model"] = v.Model
		}
		if v.Instructions != "" {
			update["instructions"] = v.Instructions
		}
		if len(v.Modalities) > 0 {
			update["output_modalities"] = append([]string(nil), v.Modalities...)
		}
		if len(v.Tools) > 0 {
			update["tools"] = realtimeToolsToParams(v.Tools)
		}
		data, err := json.Marshal(map[string]any{"session": update})
		if err != nil {
			return nil, false
		}
		return []models.SessionEvent{models.NewSessionUpdateEvent(data)}, true
	case messages.StreamTypeToolCallEnd:
		// Tool result: send as conversation.item.create with a
		// function_call_output item so the model observes what its tool
		// returned. Mirrors the Grok provider translation (grok/events.go)
		// and deliberately appends no response.create.
		v, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || v == nil {
			return nil, false
		}
		data, _ := json.Marshal(map[string]any{
			"item": map[string]any{
				"type":    "function_call_output",
				"call_id": v.ToolCallID,
				"output":  v.Arguments,
			},
		})
		return []models.SessionEvent{
			{Type: conversationItemCreateEvent, Data: data},
		}, true
	default:
		return nil, false
	}
}

func realtimeAudioBytes(data json.RawMessage) []byte {
	encoded := firstStringField(data, "delta")
	if encoded == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	return decoded
}

func realtimeAudioMediaType(data json.RawMessage) string {
	format := firstStringField(data, "format", "format.type", "audio_format", "response.audio.output.format.type", "response.output_audio_format")
	switch format {
	case "pcm16":
		return "audio/pcm"
	case "g711_ulaw":
		return "audio/g711-ulaw"
	case "g711_alaw":
		return "audio/g711-alaw"
	default:
		return format
	}
}

func firstStringField(data json.RawMessage, paths ...string) string {
	for _, path := range paths {
		if value := stringField(data, strings.Split(path, ".")); value != "" {
			return value
		}
	}
	return ""
}

func stringField(data json.RawMessage, path []string) string {
	if len(data) == 0 || len(path) == 0 {
		return ""
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(data, &current); err != nil {
		return ""
	}
	for i, part := range path {
		raw, ok := current[part]
		if !ok {
			return ""
		}
		if i == len(path)-1 {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return ""
			}
			return value
		}
		current = map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &current); err != nil {
			return ""
		}
	}
	return ""
}
