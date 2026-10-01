package media

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// Video response parsing — non-streaming and streaming
// ---------------------------------------------------------------------------

// videoPartFrom scans msgs for the first VideoPart and returns it.
// Returns (VideoPart{}, false) if none is found.
func videoPartFrom(msgs []messages.Message) (messages.VideoPart, bool) {
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if vp, ok := part.(messages.VideoPart); ok {
				return vp, true
			}
		}
	}
	return messages.VideoPart{}, false
}

// ---------------------------------------------------------------------------
// Video input forwarding — non-streaming, streaming, and combined
// ---------------------------------------------------------------------------

// TestVideo_InputForwarded validates that Video in ExecuteInput is forwarded
// to the inferencer as a VideoPart in the user message (non-streaming path):
//
//   - The first captured message slice contains a user message with a VideoPart.
//   - VideoPart.Bytes matches the supplied video bytes.
//   - VideoPart.MediaType matches the supplied media type.
//   - The conversation history preserves the user message with its VideoPart.
//   - The inferencer was called exactly once.
func TestVideo_InputForwarded(t *testing.T) {
	videoBytes := []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70}
	const mediaType = "video/mp4"
	video := &agentloop.Video{Bytes: videoBytes, MediaType: mediaType}

	inf := new(MockInferencer).AddTextResponse("video acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Video: video}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// --- inferencer received the user message ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]
	if userMsg.Role != messages.RoleUser {
		t.Errorf("user message role: got %q, want %q", userMsg.Role, messages.RoleUser)
	}

	// --- VideoPart present with correct bytes and media type ---
	vp, found := videoPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no VideoPart in captured user message")
	}
	if !bytes.Equal(vp.Bytes, videoBytes) {
		t.Errorf("VideoPart.Bytes: got %v, want %v", vp.Bytes, videoBytes)
	}
	if vp.MediaType != mediaType {
		t.Errorf("VideoPart.MediaType: got %q, want %q", vp.MediaType, mediaType)
	}

	// --- conversation history preserves the VideoPart ---
	histVP, found := videoPartFrom(sc.History())
	if !found {
		t.Fatal("no VideoPart in conversation history")
	}
	if !bytes.Equal(histVP.Bytes, videoBytes) {
		t.Errorf("history VideoPart.Bytes: got %v, want %v", histVP.Bytes, videoBytes)
	}
}

// TestVideo_InputStreamingForwarded validates that Video in ExecuteInput is
// forwarded to the inferencer when using the ExecuteStreaming path.
func TestVideo_InputStreamingForwarded(t *testing.T) {
	videoBytes := []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70}
	const mediaType = "video/mp4"
	video := &agentloop.Video{Bytes: videoBytes, MediaType: mediaType}

	inf := new(MockInferencer).AddTextResponse("streaming video acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sc.Loop.ExecuteStreaming(ctx, agentloop.ExecuteInput{Video: video})
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	for result.EventStream.HasNext() {
		// drain event stream so kernel does not block
	}

	// --- inferencer received the user message ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]

	// --- VideoPart present with correct bytes and media type ---
	vp, found := videoPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no VideoPart in captured user message")
	}
	if !bytes.Equal(vp.Bytes, videoBytes) {
		t.Errorf("VideoPart.Bytes: got %v, want %v", vp.Bytes, videoBytes)
	}
	if vp.MediaType != mediaType {
		t.Errorf("VideoPart.MediaType: got %q, want %q", vp.MediaType, mediaType)
	}
}

// TestVideo_InputWithTextForwarded validates that a multimodal ExecuteInput
// with both a text message and video produces a user message that contains
// both a TextPart and a VideoPart.
func TestVideo_InputWithTextForwarded(t *testing.T) {
	const userText = "describe this video"
	videoBytes := []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70}
	const mediaType = "video/mp4"
	video := &agentloop.Video{Bytes: videoBytes, MediaType: mediaType}

	inf := new(MockInferencer).AddTextResponse("the video shows a test pattern")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Message: userText, Video: video}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]

	// --- TextPart present ---
	if userMsg.TextContent() != userText {
		t.Errorf("TextContent: got %q, want %q", userMsg.TextContent(), userText)
	}

	// --- VideoPart present with correct bytes ---
	vp, found := videoPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no VideoPart in multimodal user message")
	}
	if !bytes.Equal(vp.Bytes, videoBytes) {
		t.Errorf("VideoPart.Bytes: got %v, want %v", vp.Bytes, videoBytes)
	}
}
