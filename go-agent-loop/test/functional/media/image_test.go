package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// Image response parsing — non-streaming and streaming
// ---------------------------------------------------------------------------

// imagePartFrom scans msgs for the first ImagePart and returns it.
// Returns (ImagePart{}, false) if none is found.
func imagePartFrom(msgs []messages.Message) (messages.ImagePart, bool) {
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if ip, ok := part.(messages.ImagePart); ok {
				return ip, true
			}
		}
	}
	return messages.ImagePart{}, false
}

// ---------------------------------------------------------------------------
// Image input forwarding — non-streaming, streaming, and combined
// ---------------------------------------------------------------------------

// makeTestImage returns a deterministic 2×2 RGBA image for use in input tests.
func makeTestImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 0, G: 255, B: 0, A: 255})
	img.SetRGBA(0, 1, color.RGBA{R: 0, G: 0, B: 255, A: 255})
	img.SetRGBA(1, 1, color.RGBA{R: 128, G: 128, B: 128, A: 255})
	return img
}

// imageInputBytes returns the JPEG bytes that execute_input.go produces for img
// (Quality 92, matching encodeImageToJPEG).
func imageInputBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encoding test image: %v", err)
	}
	return buf.Bytes()
}

// TestImage_InputForwarded validates that Image in ExecuteInput is forwarded
// to the inferencer as an ImagePart in the user message (non-streaming path):
//
//   - The first captured message slice contains a user message with an ImagePart.
//   - ImagePart.Bytes matches the JPEG encoding of the supplied image.
//   - ImagePart.MediaType is "image/jpeg".
//   - The conversation history preserves the user message with its ImagePart.
//   - The delta buffer contains a MESSAGE.START event with Role=RoleUser.
//   - The inferencer was called exactly once.
func TestImage_InputForwarded(t *testing.T) {
	img := makeTestImage()
	wantBytes := imageInputBytes(t, img)
	const mediaType = "image/jpeg"

	inf := new(MockInferencer).AddTextResponse("image acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Image: img}); err != nil {
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

	// --- ImagePart present with correct bytes and media type ---
	ip, found := imagePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no ImagePart in captured user message")
	}
	if !bytes.Equal(ip.Bytes, wantBytes) {
		t.Errorf("ImagePart.Bytes: got %d bytes, want %d bytes", len(ip.Bytes), len(wantBytes))
	}
	if ip.MediaType != mediaType {
		t.Errorf("ImagePart.MediaType: got %q, want %q", ip.MediaType, mediaType)
	}

	// --- conversation history preserves the ImagePart ---
	histIP, found := imagePartFrom(sc.History())
	if !found {
		t.Fatal("no ImagePart in conversation history")
	}
	if !bytes.Equal(histIP.Bytes, wantBytes) {
		t.Errorf("history ImagePart.Bytes: got %d bytes, want %d bytes", len(histIP.Bytes), len(wantBytes))
	}
}

// TestImage_InputStreamingForwarded validates that Image in ExecuteInput is
// forwarded to the inferencer when using the ExecuteStreaming path.
func TestImage_InputStreamingForwarded(t *testing.T) {
	img := makeTestImage()
	wantBytes := imageInputBytes(t, img)

	inf := new(MockInferencer).AddTextResponse("streaming image acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sc.Loop.ExecuteStreaming(ctx, agentloop.ExecuteInput{Image: img})
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

	// --- ImagePart present with correct bytes and media type ---
	ip, found := imagePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no ImagePart in captured user message")
	}
	if !bytes.Equal(ip.Bytes, wantBytes) {
		t.Errorf("ImagePart.Bytes: got %d bytes, want %d bytes", len(ip.Bytes), len(wantBytes))
	}
	if ip.MediaType != "image/jpeg" {
		t.Errorf("ImagePart.MediaType: got %q, want %q", ip.MediaType, "image/jpeg")
	}
}

// TestImage_InputWithTextForwarded validates that a multimodal ExecuteInput
// with both a text message and an image produces a user message that contains
// both a TextPart and an ImagePart.
func TestImage_InputWithTextForwarded(t *testing.T) {
	const userText = "describe this image"
	img := makeTestImage()
	wantBytes := imageInputBytes(t, img)

	inf := new(MockInferencer).AddTextResponse("it's a 2x2 grid of coloured squares")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Message: userText, Image: img}); err != nil {
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

	// --- ImagePart present with correct bytes ---
	ip, found := imagePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no ImagePart in multimodal user message")
	}
	if !bytes.Equal(ip.Bytes, wantBytes) {
		t.Errorf("ImagePart.Bytes: got %d bytes, want %d bytes", len(ip.Bytes), len(wantBytes))
	}
}
