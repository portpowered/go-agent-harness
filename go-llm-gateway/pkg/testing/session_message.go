package testing

import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// streamMessageJSON is a JSON-friendly representation of StreamMessage that
// stores Value as raw bytes, enabling round-trip through the capture file.
type streamMessageJSON struct {
	Type               string          `json:"type"`
	Role               string          `json:"role,omitempty"`
	ToolCallId         string          `json:"tool_call_id,omitempty"`
	Value              json.RawMessage `json:"value,omitempty"`
	GlobalIndex        int             `json:"global_index,omitempty"`
	ActorProvidedID    string          `json:"actor_provided_id,omitempty"`
	ActorProvidedIndex int             `json:"actor_provided_index,omitempty"`
	ActorStreamID      string          `json:"actor_stream_id,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	LoopPassID         int             `json:"loop_pass_id,omitempty"`
}

// MarshalStreamMessage serializes a StreamMessage to JSON bytes suitable for
// storage in a session capture file.
func MarshalStreamMessage(msg messages.StreamMessage) (json.RawMessage, error) {
	var valueBytes json.RawMessage
	if msg.Value != nil {
		b, err := json.Marshal(msg.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal value: %w", err)
		}
		valueBytes = b
	}

	return json.Marshal(streamMessageJSON{
		Type:               string(msg.Type),
		Role:               string(msg.Role),
		ToolCallId:         msg.ToolCallId,
		Value:              valueBytes,
		GlobalIndex:        msg.GlobalIndex,
		ActorProvidedID:    msg.ActorProvidedID,
		ActorProvidedIndex: msg.ActorProvidedIndex,
		ActorStreamID:      msg.ActorStreamID,
		ActorID:            string(msg.ActorID),
		LoopPassID:         msg.LoopPassID,
	})
}

// UnmarshalStreamMessage deserializes a StreamMessage from JSON bytes,
// reconstructing the correct concrete StreamMessageValue based on Type.
func UnmarshalStreamMessage(data json.RawMessage) (messages.StreamMessage, error) {
	var raw streamMessageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return messages.StreamMessage{}, fmt.Errorf("unmarshal stream message envelope: %w", err)
	}

	msg := messages.StreamMessage{
		Type:               messages.StreamMessageType(raw.Type),
		Role:               messages.Role(raw.Role),
		ToolCallId:         raw.ToolCallId,
		GlobalIndex:        raw.GlobalIndex,
		ActorProvidedID:    raw.ActorProvidedID,
		ActorProvidedIndex: raw.ActorProvidedIndex,
		ActorStreamID:      raw.ActorStreamID,
		ActorID:            messages.ParticipantID(raw.ActorID),
		LoopPassID:         raw.LoopPassID,
	}

	if len(raw.Value) > 0 && string(raw.Value) != "null" {
		val, err := unmarshalValue(msg.Type, raw.Value)
		if err != nil {
			return messages.StreamMessage{}, err
		}
		msg.Value = val
	}

	return msg, nil
}

// unmarshalValue maps a StreamMessageType to the correct concrete
// StreamMessageValue and deserializes from JSON.
// NOTE: This switch must be updated when new StreamMessageType values are added
// to the messages package.
func unmarshalValue(t messages.StreamMessageType, data json.RawMessage) (messages.StreamMessageValue, error) {
	var v messages.StreamMessageValue
	switch t {
	case messages.StreamTypeMessageStart:
		v = new(messages.MessageStartValue)
	case messages.StreamTypeMessageEnd:
		v = new(messages.MessageEndValue)
	case messages.StreamTypeTextStart:
		v = new(messages.TextStartValue)
	case messages.StreamTypeTextDelta:
		v = new(messages.TextDeltaValue)
	case messages.StreamTypeTextEnd:
		v = new(messages.TextEndValue)
	case messages.StreamTypeToolCallStart:
		v = new(messages.ToolCallStartValue)
	case messages.StreamTypeToolCallDelta:
		v = new(messages.ToolCallDeltaValue)
	case messages.StreamTypeToolCallEnd:
		v = new(messages.ToolCallEndValue)
	case messages.StreamTypeAudioStart:
		v = new(messages.AudioStartValue)
	case messages.StreamTypeAudioDelta:
		v = new(messages.AudioDeltaValue)
	case messages.StreamTypeAudioEnd:
		v = new(messages.AudioEndValue)
	case messages.StreamTypeReasoningStart:
		v = new(messages.ReasoningStartValue)
	case messages.StreamTypeReasoningDelta:
		v = new(messages.ReasoningDeltaValue)
	case messages.StreamTypeReasoningEnd:
		v = new(messages.ReasoningEndValue)
	case messages.StreamTypeVADSpeechStarted:
		v = new(messages.VADSpeechStartedValue)
	case messages.StreamTypeVADSpeechStopped:
		v = new(messages.VADSpeechStoppedValue)
	case messages.StreamTypeTranscriptStart:
		v = new(messages.TranscriptStartValue)
	case messages.StreamTypeTranscriptDelta:
		v = new(messages.TranscriptDeltaValue)
	case messages.StreamTypeTranscriptEnd:
		v = new(messages.TranscriptEndValue)
	case messages.StreamTypePong:
		v = new(messages.PongValue)
	case messages.StreamTypeSessionOpen:
		v = new(messages.SessionOpenValue)
	case messages.StreamTypeSessionClose:
		v = new(messages.SessionCloseValue)
	case messages.StreamTypeSessionCreated:
		v = new(messages.SessionCreatedValue)
	case messages.StreamTypeSessionUpdated:
		v = new(messages.SessionUpdatedValue)
	case messages.StreamTypeSessionUpdate:
		v = new(messages.SessionUpdateValue)
	case messages.StreamTypeResponseCancel:
		v = new(messages.ResponseCancelValue)
	case messages.StreamTypeResponseCreate:
		v = new(messages.ResponseCreateValue)
	case messages.StreamTypeUsageInfo:
		v = new(messages.UsageInfoValue)
	case messages.StreamTypeError:
		v = new(messages.ErrorValue)
	case messages.StreamTypeRefusal:
		v = new(messages.RefusalValue)
	case messages.StreamTypeImageStart:
		v = new(messages.ImageStartValue)
	case messages.StreamTypeImageDelta:
		v = new(messages.ImageDeltaValue)
	case messages.StreamTypeImageEnd:
		v = new(messages.ImageEndValue)
	case messages.StreamTypeVideoStart:
		v = new(messages.VideoStartValue)
	case messages.StreamTypeVideoDelta:
		v = new(messages.VideoDeltaValue)
	case messages.StreamTypeVideoEnd:
		v = new(messages.VideoEndValue)
	case messages.StreamTypeFileStart:
		v = new(messages.FileStartValue)
	case messages.StreamTypeFileDelta:
		v = new(messages.FileDeltaValue)
	case messages.StreamTypeFileEnd:
		v = new(messages.FileEndValue)
	case messages.StreamTypeEmbeddingStart:
		v = new(messages.EmbeddingStartValue)
	case messages.StreamTypeEmbeddingDelta:
		v = new(messages.EmbeddingDeltaValue)
	case messages.StreamTypeEmbeddingEnd:
		v = new(messages.EmbeddingEndValue)
	case messages.StreamTypeLoopEnd:
		v = new(messages.LoopEndValue)
	default:
		return nil, fmt.Errorf("unknown stream message type: %s", t)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("unmarshal value for type %s: %w", t, err)
	}
	return v, nil
}
