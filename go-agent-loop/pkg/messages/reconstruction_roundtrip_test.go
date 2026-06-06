package messages

import (
	"bytes"
	"reflect"
	"testing"
)

// TestReconstructModelMessageFromDeltas_RoundTripAllFields verifies that every
// field on the Message struct that ReconstructModelMessageFromDeltas can populate
// survives the full streaming round-trip (encode to stream deltas → reconstruct).
//
// If a new exported field is added to Message, this test will fail in the
// reflect-based guard at the bottom, forcing the developer to either:
//   - add reconstruction support and update this test, or
//   - explicitly mark the field as not-reconstructed.
//
// The three conversion paths that must handle every Message field:
//   - responseToMessage()              (go-llm-gateway, non-streaming)
//   - streamSSEToGateway()             (go-llm-gateway, streaming → deltas)
//   - ReconstructModelMessageFromDeltas (go-agent-loop, deltas → Message)
func TestReconstructModelMessageFromDeltas_RoundTripAllFields(t *testing.T) {
	// Build deltas that exercise every content type the reconstruction handles.
	deltas := []StreamMessage{
		// Text
		{Type: StreamTypeTextDelta, Value: NewTextDeltaValue("hello ")},
		{Type: StreamTypeTextDelta, Value: NewTextDeltaValue("world")},

		// Reasoning
		{Type: StreamTypeReasoningStart, Value: NewReasoningStartValue()},
		{Type: StreamTypeReasoningDelta, Value: NewReasoningDeltaValue("think hard")},
		{Type: StreamTypeReasoningEnd, Value: NewReasoningEndValue()},

		// Tool call
		{Type: StreamTypeToolCallStart, Value: NewToolCallStartValue("tc-1", "my_tool")},
		{Type: StreamTypeToolCallEnd, Value: NewToolCallEndValue("tc-1", "my_tool", `{"key":"val"}`)},

		// Refusal
		{Type: StreamTypeRefusal, Value: NewRefusalValue("I cannot do that")},

		// Audio
		{Type: StreamTypeAudioDelta, Value: NewAudioDeltaValue([]byte{0x01, 0x02})},
		{Type: StreamTypeAudioDelta, Value: NewAudioDeltaValue([]byte{0x03})},

		// Image
		{Type: StreamTypeImageStart, Value: NewImageStartValue("image/png")},
		{Type: StreamTypeImageDelta, Value: NewImageDeltaValue([]byte{0xAA, 0xBB})},

		// Video
		{Type: StreamTypeVideoStart, Value: NewVideoStartValue("video/mp4")},
		{Type: StreamTypeVideoDelta, Value: NewVideoDeltaValue([]byte{0xCC})},

		// File
		{Type: StreamTypeFileStart, Value: NewFileStartValue("application/pdf", "doc.pdf")},
		{Type: StreamTypeFileDelta, Value: NewFileDeltaValue([]byte{0xDD})},

		// Embedding
		{Type: StreamTypeEmbeddingStart, Value: NewEmbeddingStartValue("application/vnd.safetensors")},
		{Type: StreamTypeEmbeddingDelta, Value: NewEmbeddingDeltaValue([]byte{0xEE})},
	}

	msg := ReconstructModelMessageFromDeltas(deltas)

	// --- Assert each reconstructed field ---

	// Role
	if msg.Role != RoleAssistant {
		t.Errorf("Role: got %q, want %q", msg.Role, RoleAssistant)
	}

	// Refusal
	if msg.Refusal != "I cannot do that" {
		t.Errorf("Refusal: got %q, want %q", msg.Refusal, "I cannot do that")
	}

	// ToolCalls
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "tc-1" || tc.Name != "my_tool" || tc.Arguments != `{"key":"val"}` {
		t.Errorf("ToolCalls[0]: got %+v", tc)
	}

	// ContentParts — expect: text, reasoning, audio, image, video, file, embedding (7 parts)
	if len(msg.ContentParts) != 7 {
		t.Fatalf("ContentParts: got %d parts, want 7; parts: %v", len(msg.ContentParts), msg.ContentParts)
	}

	// Text
	if tp, ok := msg.ContentParts[0].(TextPart); !ok || tp.Text != "hello world" {
		t.Errorf("ContentParts[0] text: got %+v", msg.ContentParts[0])
	}

	// Reasoning
	if rp, ok := msg.ContentParts[1].(ReasoningPart); !ok || rp.Reasoning != "<thinking>\nthink hard\n</thinking>" {
		t.Errorf("ContentParts[1] reasoning: got %+v", msg.ContentParts[1])
	}

	// Audio
	if ap, ok := msg.ContentParts[2].(AudioPart); !ok || !bytes.Equal(ap.Bytes, []byte{0x01, 0x02, 0x03}) {
		t.Errorf("ContentParts[2] audio: got %+v", msg.ContentParts[2])
	}

	// Image
	if ip, ok := msg.ContentParts[3].(ImagePart); !ok || ip.MediaType != "image/png" || !bytes.Equal(ip.Bytes, []byte{0xAA, 0xBB}) {
		t.Errorf("ContentParts[3] image: got %+v", msg.ContentParts[3])
	}

	// Video
	if vp, ok := msg.ContentParts[4].(VideoPart); !ok || vp.MediaType != "video/mp4" || !bytes.Equal(vp.Bytes, []byte{0xCC}) {
		t.Errorf("ContentParts[4] video: got %+v", msg.ContentParts[4])
	}

	// File
	if fp, ok := msg.ContentParts[5].(FilePart); !ok || fp.MediaType != "application/pdf" || fp.Name != "doc.pdf" || !bytes.Equal(fp.Bytes, []byte{0xDD}) {
		t.Errorf("ContentParts[5] file: got %+v", msg.ContentParts[5])
	}

	// Embedding
	if ep, ok := msg.ContentParts[6].(EmbeddingPart); !ok || ep.MediaType != "application/vnd.safetensors" || !bytes.Equal(ep.Bytes, []byte{0xEE}) {
		t.Errorf("ContentParts[6] embedding: got %+v", msg.ContentParts[6])
	}
}

// TestReconstructModelMessageFromDeltas_FieldGuard uses reflect to verify that
// every exported field on Message is either tested above or explicitly listed
// as not-reconstructed. Adding a new field to Message without updating this
// list causes an immediate test failure.
func TestReconstructModelMessageFromDeltas_FieldGuard(t *testing.T) {
	// Fields that ReconstructModelMessageFromDeltas sets (tested above).
	reconstructedFields := map[string]bool{
		"Role":         true,
		"ContentParts": true,
		"Refusal":      true,
		"ToolCalls":    true,
	}

	// Fields that are NOT populated by model reconstruction — they are set
	// elsewhere (e.g., by the engine, by tool messages, or by callers).
	// If you add a new Message field that reconstruction SHOULD handle,
	// do NOT add it here — instead add reconstruction logic and a test.
	notReconstructedFields := map[string]bool{
		"ToolCallID":         true, // only for Role == RoleTool
		"Name":               true, // optional name for tool results
		"Index":              true, // position in conversation, set by caller
		"GlobalIndex":        true, // ordering: set by engine
		"ActorProvidedID":    true, // ordering: set by engine
		"ActorProvidedIndex": true, // ordering: set by engine
		"ActorStreamID":      true, // ordering: set by engine
		"ActorID":            true, // ordering: set by engine
	}

	msgType := reflect.TypeFor[Message]()
	for i := 0; i < msgType.NumField(); i++ {
		field := msgType.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		if !reconstructedFields[name] && !notReconstructedFields[name] {
			t.Errorf(
				"Message has exported field %q that is not accounted for in the round-trip test. "+
					"If ReconstructModelMessageFromDeltas should handle it, add reconstruction logic "+
					"and a test assertion. Otherwise, add it to notReconstructedFields in this test.",
				name,
			)
		}
	}
}
