package wire

import (
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestStreamMessageCodecRoundTripsRecordingFamilies(t *testing.T) {
	service := NewService()
	values := []messages.StreamMessage{
		{
			Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant,
			ResponseID: "response-1", GlobalIndex: 7,
			Value: messages.NewTextDeltaValue("recorded text"),
		},
		{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValueWithTerminal("session-1", "completed", "completed", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete),
		},
		{
			Type: messages.StreamTypeImageDelta, ResponseID: "response-2",
			Value: messages.NewImageDeltaValue([]byte{1, 2, 3}),
		},
		{
			Type: messages.StreamTypeAudioDelta, ResponseID: "response-3",
			Value: messages.NewAudioDeltaValue([]byte{4, 5, 6}),
		},
		{
			Type: messages.StreamTypeToolCallStart, ToolCallId: "call-1",
			Value: messages.NewToolCallStartValue("call-1", "lookup"),
		},
	}
	for _, original := range values {
		encoded, err := service.EncodeStreamMessage(original)
		if err != nil {
			t.Fatalf("encode %s: %v", original.Type, err)
		}
		decoded, err := service.DecodeStreamMessage(encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", original.Type, err)
		}
		if !reflect.DeepEqual(decoded, original) {
			t.Errorf("roundtrip %s = %#v, want %#v", original.Type, decoded, original)
		}
	}
}

func TestStreamMessageCodecRestoresEverySupportedValueType(t *testing.T) {
	service := NewService()
	values := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: &messages.MessageStartValue{}},
		{Type: messages.StreamTypeMessageEnd, Value: &messages.MessageEndValue{}},
		{Type: messages.StreamTypeTextStart, Value: &messages.TextStartValue{}},
		{Type: messages.StreamTypeTextEnd, Value: &messages.TextEndValue{}},
		{Type: messages.StreamTypeToolCallDelta, Value: &messages.ToolCallDeltaValue{}},
		{Type: messages.StreamTypeToolCallEnd, Value: &messages.ToolCallEndValue{}},
		{Type: messages.StreamTypeReasoningStart, Value: &messages.ReasoningStartValue{}},
		{Type: messages.StreamTypeReasoningDelta, Value: &messages.ReasoningDeltaValue{}},
		{Type: messages.StreamTypeReasoningEnd, Value: &messages.ReasoningEndValue{}},
		{Type: messages.StreamTypeSystemFullMessage, Value: messages.NewInferenceResultValue("tool", messages.Message{})},
		{Type: messages.StreamTypePong, Value: &messages.PongValue{}},
		{Type: messages.StreamTypeSessionOpen, Value: &messages.SessionOpenValue{}},
		{Type: messages.StreamTypeSessionCreated, Value: &messages.SessionCreatedValue{}},
		{Type: messages.StreamTypeSessionUpdated, Value: &messages.SessionUpdatedValue{}},
		{Type: messages.StreamTypeSessionUpdate, Value: &messages.SessionUpdateValue{}},
		{Type: messages.StreamTypeResponseCancel, Value: &messages.ResponseCancelValue{}},
		{Type: messages.StreamTypeResponseCreate, Value: &messages.ResponseCreateValue{}},
		{Type: messages.StreamTypeUsageInfo, Value: &messages.UsageInfoValue{}},
		{Type: messages.StreamTypeError, Value: &messages.ErrorValue{}},
		{Type: messages.StreamTypeRefusal, Value: &messages.RefusalValue{}},
		{Type: messages.StreamTypeImageStart, Value: &messages.ImageStartValue{}},
		{Type: messages.StreamTypeImageEnd, Value: &messages.ImageEndValue{}},
		{Type: messages.StreamTypeVideoStart, Value: &messages.VideoStartValue{}},
		{Type: messages.StreamTypeVideoDelta, Value: &messages.VideoDeltaValue{}},
		{Type: messages.StreamTypeVideoEnd, Value: &messages.VideoEndValue{}},
		{Type: messages.StreamTypeFileStart, Value: &messages.FileStartValue{}},
		{Type: messages.StreamTypeFileDelta, Value: &messages.FileDeltaValue{}},
		{Type: messages.StreamTypeFileEnd, Value: &messages.FileEndValue{}},
		{Type: messages.StreamTypeEmbeddingStart, Value: &messages.EmbeddingStartValue{}},
		{Type: messages.StreamTypeEmbeddingDelta, Value: &messages.EmbeddingDeltaValue{}},
		{Type: messages.StreamTypeEmbeddingEnd, Value: &messages.EmbeddingEndValue{}},
		{Type: messages.StreamTypeLoopEnd, Value: &messages.LoopEndValue{}},
		{Type: messages.StreamTypeAudioStart, Value: &messages.AudioStartValue{}},
		{Type: messages.StreamTypeAudioEnd, Value: &messages.AudioEndValue{}},
		{Type: messages.StreamTypeVADSpeechStarted, Value: &messages.VADSpeechStartedValue{}},
		{Type: messages.StreamTypeVADSpeechStopped, Value: &messages.VADSpeechStoppedValue{}},
		{Type: messages.StreamTypeTranscriptStart, Value: &messages.TranscriptStartValue{}},
		{Type: messages.StreamTypeTranscriptDelta, Value: &messages.TranscriptDeltaValue{}},
		{Type: messages.StreamTypeTranscriptEnd, Value: &messages.TranscriptEndValue{}},
		{Type: messages.StreamTypeInputItemAdded, Value: &messages.InputItemAddedValue{}},
	}
	for _, original := range values {
		encoded, err := service.EncodeStreamMessage(original)
		if err != nil {
			t.Fatalf("encode %s: %v", original.Type, err)
		}
		decoded, err := service.DecodeStreamMessage(encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", original.Type, err)
		}
		if !reflect.DeepEqual(decoded, original) {
			t.Errorf("roundtrip %s = %#v, want %#v", original.Type, decoded, original)
		}
	}
}

func TestStreamMessageCodecRejectsMalformedAndUnknownMessages(t *testing.T) {
	service := NewService()
	for _, data := range [][]byte{[]byte("{"), []byte(`{"type":"future.message","value":{}}`)} {
		if _, err := service.DecodeStreamMessage(data); err == nil {
			t.Errorf("decode %q unexpectedly succeeded", data)
		}
	}
}
