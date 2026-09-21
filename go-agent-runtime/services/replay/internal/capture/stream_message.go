package capture

import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type streamMessageJSON struct {
	Type               string          `json:"type"`
	Role               string          `json:"role,omitempty"`
	ToolCallId         string          `json:"tool_call_id,omitempty"`
	ResponseID         string          `json:"response_id,omitempty"`
	Value              json.RawMessage `json:"value,omitempty"`
	GlobalIndex        int             `json:"global_index,omitempty"`
	ActorProvidedID    string          `json:"actor_provided_id,omitempty"`
	ActorProvidedIndex int             `json:"actor_provided_index,omitempty"`
	ActorStreamID      string          `json:"actor_stream_id,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	LoopPassID         int             `json:"loop_pass_id,omitempty"`
}

// MarshalStreamMessage serializes stream messages using the session capture wire format.
func MarshalStreamMessage(message messages.StreamMessage) (json.RawMessage, error) {
	var value json.RawMessage
	if message.Value != nil {
		encoded, err := json.Marshal(message.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal value: %w", err)
		}
		value = encoded
	}
	return json.Marshal(streamMessageJSON{
		Type:               string(message.Type),
		Role:               string(message.Role),
		ToolCallId:         message.ToolCallId,
		ResponseID:         message.ResponseID,
		Value:              value,
		GlobalIndex:        message.GlobalIndex,
		ActorProvidedID:    message.ActorProvidedID,
		ActorProvidedIndex: message.ActorProvidedIndex,
		ActorStreamID:      message.ActorStreamID,
		ActorID:            string(message.ActorID),
		LoopPassID:         message.LoopPassID,
	})
}

// UnmarshalStreamMessage restores the concrete value type recorded in a capture.
func UnmarshalStreamMessage(data json.RawMessage) (messages.StreamMessage, error) {
	var raw streamMessageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return messages.StreamMessage{}, fmt.Errorf("unmarshal stream message envelope: %w", err)
	}
	message := messages.StreamMessage{
		Type:               messages.StreamMessageType(raw.Type),
		Role:               messages.Role(raw.Role),
		ToolCallId:         raw.ToolCallId,
		ResponseID:         raw.ResponseID,
		GlobalIndex:        raw.GlobalIndex,
		ActorProvidedID:    raw.ActorProvidedID,
		ActorProvidedIndex: raw.ActorProvidedIndex,
		ActorStreamID:      raw.ActorStreamID,
		ActorID:            messages.ParticipantID(raw.ActorID),
		LoopPassID:         raw.LoopPassID,
	}
	if len(raw.Value) > 0 && string(raw.Value) != "null" {
		value, err := unmarshalStreamMessageValue(message.Type, raw.Value)
		if err != nil {
			return messages.StreamMessage{}, err
		}
		message.Value = value
	}
	return message, nil
}

func unmarshalStreamMessageValue(messageType messages.StreamMessageType, data json.RawMessage) (messages.StreamMessageValue, error) {
	var value messages.StreamMessageValue
	switch messageType {
	case messages.StreamTypeMessageStart:
		value = new(messages.MessageStartValue)
	case messages.StreamTypeMessageEnd:
		value = new(messages.MessageEndValue)
	case messages.StreamTypeTextStart:
		value = new(messages.TextStartValue)
	case messages.StreamTypeTextDelta:
		value = new(messages.TextDeltaValue)
	case messages.StreamTypeTextEnd:
		value = new(messages.TextEndValue)
	case messages.StreamTypeToolCallStart:
		value = new(messages.ToolCallStartValue)
	case messages.StreamTypeToolCallDelta:
		value = new(messages.ToolCallDeltaValue)
	case messages.StreamTypeToolCallEnd:
		value = new(messages.ToolCallEndValue)
	case messages.StreamTypeReasoningStart:
		value = new(messages.ReasoningStartValue)
	case messages.StreamTypeReasoningDelta:
		value = new(messages.ReasoningDeltaValue)
	case messages.StreamTypeReasoningEnd:
		value = new(messages.ReasoningEndValue)
	case messages.StreamTypePong:
		value = new(messages.PongValue)
	case messages.StreamTypeSessionOpen:
		value = new(messages.SessionOpenValue)
	case messages.StreamTypeSessionClose:
		value = new(messages.SessionCloseValue)
	case messages.StreamTypeSessionCreated:
		value = new(messages.SessionCreatedValue)
	case messages.StreamTypeSessionUpdated:
		value = new(messages.SessionUpdatedValue)
	case messages.StreamTypeSessionUpdate:
		value = new(messages.SessionUpdateValue)
	case messages.StreamTypeResponseCancel:
		value = new(messages.ResponseCancelValue)
	case messages.StreamTypeResponseCreate:
		value = new(messages.ResponseCreateValue)
	case messages.StreamTypeUsageInfo:
		value = new(messages.UsageInfoValue)
	case messages.StreamTypeError:
		value = new(messages.ErrorValue)
	case messages.StreamTypeRefusal:
		value = new(messages.RefusalValue)
	case messages.StreamTypeImageStart:
		value = new(messages.ImageStartValue)
	case messages.StreamTypeImageDelta:
		value = new(messages.ImageDeltaValue)
	case messages.StreamTypeImageEnd:
		value = new(messages.ImageEndValue)
	case messages.StreamTypeVideoStart:
		value = new(messages.VideoStartValue)
	case messages.StreamTypeVideoDelta:
		value = new(messages.VideoDeltaValue)
	case messages.StreamTypeVideoEnd:
		value = new(messages.VideoEndValue)
	case messages.StreamTypeFileStart:
		value = new(messages.FileStartValue)
	case messages.StreamTypeFileDelta:
		value = new(messages.FileDeltaValue)
	case messages.StreamTypeFileEnd:
		value = new(messages.FileEndValue)
	case messages.StreamTypeEmbeddingStart:
		value = new(messages.EmbeddingStartValue)
	case messages.StreamTypeEmbeddingDelta:
		value = new(messages.EmbeddingDeltaValue)
	case messages.StreamTypeEmbeddingEnd:
		value = new(messages.EmbeddingEndValue)
	case messages.StreamTypeLoopEnd:
		value = new(messages.LoopEndValue)
	case messages.StreamTypeAudioStart:
		value = new(messages.AudioStartValue)
	case messages.StreamTypeAudioDelta:
		value = new(messages.AudioDeltaValue)
	case messages.StreamTypeAudioEnd:
		value = new(messages.AudioEndValue)
	case messages.StreamTypeVADSpeechStarted:
		value = new(messages.VADSpeechStartedValue)
	case messages.StreamTypeVADSpeechStopped:
		value = new(messages.VADSpeechStoppedValue)
	case messages.StreamTypeTranscriptStart:
		value = new(messages.TranscriptStartValue)
	case messages.StreamTypeTranscriptDelta:
		value = new(messages.TranscriptDeltaValue)
	case messages.StreamTypeTranscriptEnd:
		value = new(messages.TranscriptEndValue)
	case messages.StreamTypeInputItemAdded:
		value = new(messages.InputItemAddedValue)
	default:
		return nil, fmt.Errorf("unknown stream message type: %s", messageType)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return nil, fmt.Errorf("unmarshal value for type %s: %w", messageType, err)
	}
	return value, nil
}
