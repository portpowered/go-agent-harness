package messages

import (
	"bytes"
	"testing"
)

func TestReconstructModelMessageFromDeltas_EmbeddingDeltas(t *testing.T) {
	chunk1 := []byte{0x01, 0x02, 0x03}
	chunk2 := []byte{0x04, 0x05, 0x06}
	mediaType := "application/vnd.safetensors"

	deltas := []StreamMessage{
		{Type: StreamTypeEmbeddingStart, Value: NewEmbeddingStartValue(mediaType)},
		{Type: StreamTypeEmbeddingDelta, Value: NewEmbeddingDeltaValue(chunk1)},
		{Type: StreamTypeEmbeddingDelta, Value: NewEmbeddingDeltaValue(chunk2)},
	}

	msg := ReconstructModelMessageFromDeltas(deltas)

	if msg.Role != RoleAssistant {
		t.Fatalf("expected role %q, got %q", RoleAssistant, msg.Role)
	}
	if len(msg.ContentParts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(msg.ContentParts))
	}
	ep, ok := msg.ContentParts[0].(EmbeddingPart)
	if !ok {
		t.Fatalf("expected EmbeddingPart, got %T", msg.ContentParts[0])
	}
	wantBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	if !bytes.Equal(ep.Bytes, wantBytes) {
		t.Errorf("expected bytes %v, got %v", wantBytes, ep.Bytes)
	}
	if ep.MediaType != mediaType {
		t.Errorf("expected media type %q, got %q", mediaType, ep.MediaType)
	}
}

func TestReconstructModelMessageFromDeltas_NoEmbeddingDeltas(t *testing.T) {
	deltas := []StreamMessage{
		{Type: StreamTypeTextDelta, Value: &TextDeltaValue{Content: "hello"}},
	}

	msg := ReconstructModelMessageFromDeltas(deltas)

	for _, part := range msg.ContentParts {
		if _, ok := part.(EmbeddingPart); ok {
			t.Fatal("unexpected EmbeddingPart in message with no embedding deltas")
		}
	}
}

func TestReconstructToolMessagesFromDeltas_EmbeddingDeltas(t *testing.T) {
	toolID := "tool-1"
	chunk1 := []byte{0xAA, 0xBB}
	chunk2 := []byte{0xCC, 0xDD}
	mediaType := "application/octet-stream"

	deltas := []StreamMessage{
		{Type: StreamTypeMessageStart, Value: &MessageStartValue{}},
		{Type: StreamTypeEmbeddingStart, ToolCallId: toolID, Value: NewEmbeddingStartValue(mediaType)},
		{Type: StreamTypeEmbeddingDelta, ToolCallId: toolID, Value: NewEmbeddingDeltaValue(chunk1)},
		{Type: StreamTypeEmbeddingDelta, ToolCallId: toolID, Value: NewEmbeddingDeltaValue(chunk2)},
	}

	msgs := ReconstructToolMessagesFromDeltas(deltas)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != RoleTool {
		t.Fatalf("expected role %q, got %q", RoleTool, msg.Role)
	}
	if msg.ToolCallID != toolID {
		t.Fatalf("expected tool call ID %q, got %q", toolID, msg.ToolCallID)
	}
	if len(msg.ContentParts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(msg.ContentParts))
	}
	ep, ok := msg.ContentParts[0].(EmbeddingPart)
	if !ok {
		t.Fatalf("expected EmbeddingPart, got %T", msg.ContentParts[0])
	}
	wantBytes := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	if !bytes.Equal(ep.Bytes, wantBytes) {
		t.Errorf("expected bytes %v, got %v", wantBytes, ep.Bytes)
	}
	if ep.MediaType != mediaType {
		t.Errorf("expected media type %q, got %q", mediaType, ep.MediaType)
	}
}

func TestReconstructModelMessageFromDeltas_EmbeddingWithOtherModalities(t *testing.T) {
	deltas := []StreamMessage{
		{Type: StreamTypeTextDelta, Value: &TextDeltaValue{Content: "text content"}},
		{Type: StreamTypeEmbeddingStart, Value: NewEmbeddingStartValue("application/vnd.safetensors")},
		{Type: StreamTypeEmbeddingDelta, Value: NewEmbeddingDeltaValue([]byte{0x01})},
		{Type: StreamTypeImageStart, Value: &ImageStartValue{MediaType: "image/png"}},
		{Type: StreamTypeImageDelta, Value: &ImageDeltaValue{Content: []byte{0xFF}}},
	}

	msg := ReconstructModelMessageFromDeltas(deltas)

	if len(msg.ContentParts) != 3 {
		t.Fatalf("expected 3 content parts, got %d", len(msg.ContentParts))
	}

	var hasText, hasImage, hasEmbedding bool
	for _, part := range msg.ContentParts {
		switch part.(type) {
		case TextPart:
			hasText = true
		case ImagePart:
			hasImage = true
		case EmbeddingPart:
			hasEmbedding = true
		}
	}
	if !hasText || !hasImage || !hasEmbedding {
		t.Errorf("expected text, image, and embedding parts; got text=%v image=%v embedding=%v", hasText, hasImage, hasEmbedding)
	}
}
