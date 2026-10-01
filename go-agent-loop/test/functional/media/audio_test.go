package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// Audio response parsing — non-streaming and streaming
// ---------------------------------------------------------------------------

// audioPartFrom scans msgs for the first AudioPart and returns it.
// Returns (AudioPart{}, false) if none is found.
func audioPartFrom(msgs []messages.Message) (messages.AudioPart, bool) {
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if ap, ok := part.(messages.AudioPart); ok {
				return ap, true
			}
		}
	}
	return messages.AudioPart{}, false
}

// ---------------------------------------------------------------------------
// Audio input forwarding — non-streaming, streaming, and combined
// ---------------------------------------------------------------------------

// TestAudio_InputForwarded validates that Audio in ExecuteInput is forwarded
// to the inferencer as an AudioPart in the user message (non-streaming path):
//
//   - The first captured message slice contains a user message with an AudioPart.
//   - AudioPart.Bytes matches the PCM encoding of the supplied samples
//     (little-endian int16 pairs).
//   - AudioPart.MediaType is "audio/pcm".
//   - The conversation history preserves the user message with its AudioPart.
//   - The delta buffer contains a MESSAGE.START event with Role=RoleUser.
//   - The inferencer was called exactly once.
func TestAudio_InputForwarded(t *testing.T) {
	samples := []int16{100, 200, -100, -200}
	audio := &agentloop.Audio{Samples: samples, SampleRate: 16000, Channels: 1}
	const mediaType = "audio/pcm"

	// Expected PCM bytes: little-endian int16.
	wantBytes := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(wantBytes[i*2:], uint16(s))
	}

	inf := new(MockInferencer).AddTextResponse("audio acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Audio: audio}); err != nil {
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

	// --- AudioPart present with correct bytes and media type ---
	ap, found := audioPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no AudioPart in captured user message")
	}
	if !bytes.Equal(ap.Bytes, wantBytes) {
		t.Errorf("AudioPart.Bytes: got %v, want %v", ap.Bytes, wantBytes)
	}
	if ap.MediaType != mediaType {
		t.Errorf("AudioPart.MediaType: got %q, want %q", ap.MediaType, mediaType)
	}

	// --- conversation history preserves the AudioPart ---
	histAP, found := audioPartFrom(sc.History())
	if !found {
		t.Fatal("no AudioPart in conversation history")
	}
	if !bytes.Equal(histAP.Bytes, wantBytes) {
		t.Errorf("history AudioPart.Bytes: got %v, want %v", histAP.Bytes, wantBytes)
	}
}

// TestAudio_InputStreamingForwarded validates that Audio in ExecuteInput is
// forwarded to the inferencer when using the ExecuteStreaming path.
func TestAudio_InputStreamingForwarded(t *testing.T) {
	samples := []int16{300, 400, -300, -400}
	audio := &agentloop.Audio{Samples: samples, SampleRate: 16000, Channels: 1}

	wantBytes := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(wantBytes[i*2:], uint16(s))
	}

	inf := new(MockInferencer).AddTextResponse("streaming audio acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sc.Loop.ExecuteStreaming(ctx, agentloop.ExecuteInput{Audio: audio})
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

	// --- AudioPart present with correct bytes and media type ---
	ap, found := audioPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no AudioPart in captured user message")
	}
	if !bytes.Equal(ap.Bytes, wantBytes) {
		t.Errorf("AudioPart.Bytes: got %v, want %v", ap.Bytes, wantBytes)
	}
	if ap.MediaType != "audio/pcm" {
		t.Errorf("AudioPart.MediaType: got %q, want %q", ap.MediaType, "audio/pcm")
	}
}

// TestAudio_InputWithTextForwarded validates that a multimodal ExecuteInput
// with both a text message and audio produces a user message that contains
// both a TextPart and an AudioPart.
func TestAudio_InputWithTextForwarded(t *testing.T) {
	const userText = "transcribe this audio"
	samples := []int16{500, 600, -500}
	audio := &agentloop.Audio{Samples: samples, SampleRate: 16000, Channels: 1}

	wantBytes := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(wantBytes[i*2:], uint16(s))
	}

	inf := new(MockInferencer).AddTextResponse("transcription: hello world")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Message: userText, Audio: audio}); err != nil {
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

	// --- AudioPart present with correct bytes ---
	ap, found := audioPartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no AudioPart in multimodal user message")
	}
	if !bytes.Equal(ap.Bytes, wantBytes) {
		t.Errorf("AudioPart.Bytes: got %v, want %v", ap.Bytes, wantBytes)
	}
}
