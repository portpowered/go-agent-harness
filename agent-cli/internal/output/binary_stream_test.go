package output

import (
	"bytes"
	"io"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// mockStream is a simple mock that feeds events from a slice.
type mockStream struct {
	events []messages.StreamMessage
	idx    int
	cur    messages.StreamMessage
}

func newMockStream(events []messages.StreamMessage) agentloop.Stream {
	return &mockStream{events: events, idx: -1}
}

func (s *mockStream) HasNext() bool {
	s.idx++
	if s.idx >= len(s.events) {
		return false
	}
	s.cur = s.events[s.idx]
	return true
}

func (s *mockStream) Response() agentloop.Response {
	return s.cur
}

func (s *mockStream) Err() error {
	return nil
}

func (s *mockStream) Outcome() agentloop.StreamOutcome {
	if s.idx >= len(s.events) {
		return agentloop.StreamOutcome{Status: agentloop.StreamDrained}
	}
	return agentloop.StreamOutcome{Status: agentloop.StreamOpen}
}

func (s *mockStream) Close() error {
	return nil
}

func TestWriteBinaryModalityStream_ImageOnly(t *testing.T) {
	imgChunk1 := []byte{0x89, 0x50, 0x4E, 0x47} // PNG header bytes
	imgChunk2 := []byte{0x0D, 0x0A, 0x1A, 0x0A}

	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("some text")},
		{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue("thinking...")},
		{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue(imgChunk1)},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("more text")},
		{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue(imgChunk2)},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityStream(&buf, newMockStream(events), "image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := append(imgChunk1, imgChunk2...)
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("got %x, want %x", buf.Bytes(), expected)
	}
	if n != int64(len(expected)) {
		t.Errorf("got n=%d, want %d", n, len(expected))
	}
}

func TestWriteBinaryModalityStream_AudioOnly(t *testing.T) {
	audioData := []byte{0x01, 0x02, 0x03, 0x04}

	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audioData)},
		{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue([]byte{0xFF})},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityStream(&buf, newMockStream(events), "audio")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), audioData) {
		t.Errorf("got %x, want %x", buf.Bytes(), audioData)
	}
	if n != int64(len(audioData)) {
		t.Errorf("got n=%d, want %d", n, len(audioData))
	}
}

func TestWriteBinaryModalityStream_VideoOnly(t *testing.T) {
	videoData := []byte{0xAA, 0xBB, 0xCC}

	events := []messages.StreamMessage{
		{Type: messages.StreamTypeVideoDelta, Value: messages.NewVideoDeltaValue(videoData)},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ignored")},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityStream(&buf, newMockStream(events), "video")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), videoData) {
		t.Errorf("got %x, want %x", buf.Bytes(), videoData)
	}
	if n != int64(len(videoData)) {
		t.Errorf("got n=%d, want %d", n, len(videoData))
	}
}

func TestWriteBinaryModalityStream_EmbeddingOnly(t *testing.T) {
	embData := []byte{0x10, 0x20, 0x30}

	events := []messages.StreamMessage{
		{Type: messages.StreamTypeEmbeddingDelta, Value: messages.NewEmbeddingDeltaValue(embData)},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityStream(&buf, newMockStream(events), "embedding")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), embData) {
		t.Errorf("got %x, want %x", buf.Bytes(), embData)
	}
	if n != int64(len(embData)) {
		t.Errorf("got n=%d, want %d", n, len(embData))
	}
}

func TestWriteBinaryModalityStream_NoMatchingContent(t *testing.T) {
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("only text")},
		{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue("thinking")},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityStream(&buf, newMockStream(events), "image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got %d bytes", buf.Len())
	}
}

func TestWriteBinaryModalityStream_UnsupportedModality(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteBinaryModalityStream(&buf, newMockStream(nil), "unsupported")
	if err == nil {
		t.Error("expected error for unsupported modality")
	}
}

func TestWriteBinaryModalityMessages_Image(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47}
	msgs := []messages.Message{
		{
			Role: messages.RoleAssistant,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "here is your image"},
				messages.ImagePart{Bytes: imgBytes, MediaType: "image/png"},
			},
		},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityMessages(&buf, msgs, "image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), imgBytes) {
		t.Errorf("got %x, want %x", buf.Bytes(), imgBytes)
	}
	if n != int64(len(imgBytes)) {
		t.Errorf("got n=%d, want %d", n, len(imgBytes))
	}
}

func TestWriteBinaryModalityMessages_Audio(t *testing.T) {
	audioBytes := []byte{0x01, 0x02}
	msgs := []messages.Message{
		{
			Role: messages.RoleAssistant,
			ContentParts: []messages.ContentPart{
				messages.AudioPart{Bytes: audioBytes, MediaType: "audio/wav"},
			},
		},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityMessages(&buf, msgs, "audio")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), audioBytes) {
		t.Errorf("got %x, want %x", buf.Bytes(), audioBytes)
	}
	if n != int64(len(audioBytes)) {
		t.Errorf("got n=%d, want %d", n, len(audioBytes))
	}
}

func TestWriteBinaryModalityMessages_Video(t *testing.T) {
	videoBytes := []byte{0xAA, 0xBB}
	msgs := []messages.Message{
		{
			Role: messages.RoleAssistant,
			ContentParts: []messages.ContentPart{
				messages.VideoPart{Bytes: videoBytes, MediaType: "video/mp4"},
			},
		},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityMessages(&buf, msgs, "video")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), videoBytes) {
		t.Errorf("got %x, want %x", buf.Bytes(), videoBytes)
	}
	if n != int64(len(videoBytes)) {
		t.Errorf("got n=%d, want %d", n, len(videoBytes))
	}
}

func TestWriteBinaryModalityMessages_Embedding(t *testing.T) {
	embBytes := []byte{0x10, 0x20}
	msgs := []messages.Message{
		{
			Role: messages.RoleAssistant,
			ContentParts: []messages.ContentPart{
				messages.EmbeddingPart{Bytes: embBytes, MediaType: "application/octet-stream"},
			},
		},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityMessages(&buf, msgs, "embedding")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), embBytes) {
		t.Errorf("got %x, want %x", buf.Bytes(), embBytes)
	}
	if n != int64(len(embBytes)) {
		t.Errorf("got n=%d, want %d", n, len(embBytes))
	}
}

func TestWriteBinaryModalityMessages_NoMatchingContent(t *testing.T) {
	msgs := []messages.Message{
		{
			Role: messages.RoleAssistant,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "only text"},
			},
		},
	}

	var buf bytes.Buffer
	n, err := WriteBinaryModalityMessages(&buf, msgs, "image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
}

func TestWriteBinaryModalityStream_DefaultTextUnchanged(t *testing.T) {
	// Verify that the text streaming path (WriteEventStreamToWriter) still works
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello ")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("world")},
	}

	var buf bytes.Buffer
	err := WriteEventStreamToWriter(&buf, io.Discard, newMockStream(events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "hello world" {
		t.Errorf("got %q, want %q", buf.String(), "hello world")
	}
}
