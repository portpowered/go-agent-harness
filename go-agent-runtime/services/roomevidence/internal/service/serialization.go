package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type streamMessageJSON struct {
	Type               string          `json:"type"`
	Role               string          `json:"role,omitempty"`
	ToolCallID         string          `json:"tool_call_id,omitempty"`
	ResponseID         string          `json:"response_id,omitempty"`
	Value              json.RawMessage `json:"value,omitempty"`
	GlobalIndex        int             `json:"global_index,omitempty"`
	ActorProvidedID    string          `json:"actor_provided_id,omitempty"`
	ActorProvidedIndex int             `json:"actor_provided_index,omitempty"`
	ActorStreamID      string          `json:"actor_stream_id,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	LoopPassID         int             `json:"loop_pass_id,omitempty"`
}

func marshalStreamMessage(message messages.StreamMessage) ([]byte, error) {
	var value json.RawMessage
	if message.Value != nil {
		encoded, err := json.Marshal(message.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal stream value: %w", err)
		}
		value = encoded
	}
	return json.Marshal(streamMessageJSON{
		Type: string(message.Type), Role: string(message.Role), ToolCallID: message.ToolCallId,
		ResponseID: message.ResponseID, Value: value, GlobalIndex: message.GlobalIndex,
		ActorProvidedID: message.ActorProvidedID, ActorProvidedIndex: message.ActorProvidedIndex,
		ActorStreamID: message.ActorStreamID, ActorID: string(message.ActorID), LoopPassID: message.LoopPassID,
	})
}

func stampJSON(data []byte, clock clockState) ([]byte, error) {
	offset, unixMS := clock.now()
	return stampJSONFields(data, offset, unixMS)
}

func stampJSONAt(data []byte, clock clockState, at time.Time) ([]byte, error) {
	offset := at.Sub(clock.start)
	if offset < 0 {
		offset = 0
	}
	return stampJSONFields(data, offset, at.UTC().UnixMilli())
}

func stampJSONFields(data []byte, offset time.Duration, unixMS int64) ([]byte, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode JSONL record for wall-clock stamping: %w", err)
	}
	if value == nil {
		value = make(map[string]any, 2)
	}
	value["t_offset_ms"] = offsetMillis(offset)
	value["t_unix_ms"] = unixMS
	return json.Marshal(value)
}

func redactJSON(data []byte, secrets []string) []byte {
	if len(data) == 0 || len(secrets) == 0 {
		return data
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return []byte(redactText(string(data), secrets))
	}
	value = redactValue(value, secrets)
	encoded, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return encoded
}

func redactValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case string:
		return redactText(typed, secrets)
	case []any:
		for index := range typed {
			typed[index] = redactValue(typed[index], secrets)
		}
	case map[string]any:
		for key, nested := range typed {
			typed[key] = redactValue(nested, secrets)
		}
	}
	return value
}

func redactText(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneFields(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func formatOffset(start time.Time, at time.Time) float64 {
	if at.Before(start) {
		return 0
	}
	return offsetMillis(at.Sub(start))
}

func audioSamples(pcm []byte) ([]int16, error) {
	if len(pcm) > maxAudioFrameBytes {
		return nil, fmt.Errorf("PCM16 audio frame exceeds %d-byte bound", maxAudioFrameBytes)
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("PCM16 audio delta has odd byte length %d", len(pcm))
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	return codec.DecodePCM16WithLimit(pcm, len(pcm))
}

func normalizedFormat(format rooms.AudioFormat) rooms.AudioFormat {
	if format.SampleRate <= 0 {
		format.SampleRate = defaultSampleRate
	}
	if format.Channels <= 0 {
		format.Channels = defaultChannels
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = defaultFrameDuration
	}
	return format
}
