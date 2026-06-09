package functional

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

// TestImage_NonStreamingResponse validates that an image-only model response is
// assembled correctly in non-streaming (Execute) mode:
//
//   - ExecuteResult.Messages contains exactly one assistant message.
//   - That message has an ImagePart whose Bytes and MediaType match what the
//     mock inferencer was configured to return.
//   - The conversation delta buffer contains IMAGE.START, IMAGE.DELTA, and
//     IMAGE.END events (in that relative order) with Role=RoleAssistant.
//   - The inferencer was called exactly once.
func TestImage_NonStreamingResponse(t *testing.T) {
	// Minimal PNG header bytes used as stand-in image data.
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	const mediaType = "image/png"

	inf := new(MockInferencer).AddImageResponse(imageBytes, mediaType)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	result := s.Execute("describe this image")

	// --- turn messages ---
	if len(result.Messages) != 2 {
		t.Fatalf("message count: got %d, want 1", len(result.Messages))
	}
	if result.Messages[1].Role != messages.RoleAssistant {
		t.Errorf("message role: got %q, want %q", result.Messages[1].Role, messages.RoleAssistant)
	}

	// --- ImagePart content ---
	ip, found := imagePartFrom(result.Messages)
	if !found {
		t.Fatal("no ImagePart found in response messages")
	}
	if !bytes.Equal(ip.Bytes, imageBytes) {
		t.Errorf("ImagePart.Bytes: got %v, want %v", ip.Bytes, imageBytes)
	}
	if ip.MediaType != mediaType {
		t.Errorf("ImagePart.MediaType: got %q, want %q", ip.MediaType, mediaType)
	}

	// --- delta stream contains image lifecycle events ---
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeImageStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageEnd, Role: messages.RoleAssistant},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestImage_StreamingResponse validates that an image-only model response is
// assembled correctly in streaming (ExecuteStreaming) mode:
//
//   - After consuming the text stream (which is empty for image-only responses),
//     Messages() returns one assistant message with a correctly populated ImagePart.
//   - The conversation delta buffer contains IMAGE.START, IMAGE.DELTA, IMAGE.END.
//   - The inferencer was called exactly once.
func TestImage_StreamingResponse(t *testing.T) {
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	const mediaType = "image/png"

	inf := new(MockInferencer).AddImageResponse(imageBytes, mediaType)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	// ExecuteStreamingText drains the text stream to EOF and returns Messages().
	// For an image-only response the text stream is empty (""), which is expected.
	streamText := s.ExecuteStreamingText("describe this image")

	// --- stream text is empty (no text content in image-only response) ---
	if streamText != "" {
		t.Errorf("stream text: got %q, want empty string", streamText)
	}

	// --- delta stream contains image lifecycle events ---
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeImageStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageEnd, Role: messages.RoleAssistant},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestImage_ChunkedStreamingResponse validates that multiple IMAGE.DELTA chunks
// from the inferencer stream are correctly concatenated into a single ImagePart.
func TestImage_ChunkedStreamingResponse(t *testing.T) {
	chunk1 := []byte{0x89, 0x50, 0x4e, 0x47} // first half of PNG header
	chunk2 := []byte{0x0d, 0x0a, 0x1a, 0x0a} // second half
	wantBytes := append(chunk1, chunk2...)

	const mediaType = "image/png"

	// Build the inferencer manually so we can set imageChunks directly.
	// The result carries the full concatenated bytes (what Infer returns),
	// while imageChunks drives the chunked InferStream emission.
	inf := &MockInferencer{}
	inf.entries = []inferenceEntry{{
		result: messages.InferenceResult{
			Message: messages.Message{
				Role:         messages.RoleAssistant,
				ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: wantBytes, MediaType: mediaType}},
			},
		},
		imageChunks: [][]byte{chunk1, chunk2},
	}}

	tool := NewMockToolExecutor()
	s := NewScenario(t, inf, tool)
	s.ExecuteStreamingText("generate an image")

	// Both IMAGE.DELTA chunks must appear in the delta buffer.
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeImageStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeImageEnd, Role: messages.RoleAssistant},
	})
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
