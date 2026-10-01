package media

import (
	"bytes"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// mediaResponseCase describes one generated media kind: the bytes the mock
// inferencer returns, how the kind's content part is found in messages, and
// the kind's START/DELTA/END stream types.
type mediaResponseCase struct {
	name, prompt, mediaType string
	bytes                   []byte
	chunks                  [][]byte
	add                     func(inf *MockInferencer, data []byte, mediaType string) *MockInferencer
	chunked                 func(want []byte, chunks [][]byte) inferenceEntry
	part                    func(msgs []messages.Message) (data []byte, mediaType string, ok bool)
	start, delta, end       messages.StreamMessageType
}

func mediaResponseCases() []mediaResponseCase {
	return []mediaResponseCase{
		{
			name: "audio", prompt: "generate audio", mediaType: "audio/pcm",
			bytes:  []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00},
			chunks: [][]byte{{0x52, 0x49, 0x46, 0x46}, {0x00, 0x00, 0x00, 0x00}},
			add:    (*MockInferencer).AddAudioResponse,
			chunked: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.AudioPart{Bytes: want, MediaType: "audio/pcm"}), audioChunks: chunks}
			},
			part: func(msgs []messages.Message) ([]byte, string, bool) {
				p, ok := audioPartFrom(msgs)
				return p.Bytes, p.MediaType, ok
			},
			start: messages.StreamTypeAudioStart, delta: messages.StreamTypeAudioDelta, end: messages.StreamTypeAudioEnd,
		},
		{
			name: "image", prompt: "generate an image", mediaType: "image/png",
			bytes:  []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
			chunks: [][]byte{{0x89, 0x50, 0x4e, 0x47}, {0x0d, 0x0a, 0x1a, 0x0a}},
			add:    (*MockInferencer).AddImageResponse,
			chunked: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.ImagePart{Bytes: want, MediaType: "image/png"}), imageChunks: chunks}
			},
			part: func(msgs []messages.Message) ([]byte, string, bool) {
				p, ok := imagePartFrom(msgs)
				return p.Bytes, p.MediaType, ok
			},
			start: messages.StreamTypeImageStart, delta: messages.StreamTypeImageDelta, end: messages.StreamTypeImageEnd,
		},
		{
			name: "video", prompt: "generate video", mediaType: "video/mp4",
			bytes:  []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70},
			chunks: [][]byte{{0x00, 0x00, 0x00, 0x18}, {0x66, 0x74, 0x79, 0x70}},
			add:    (*MockInferencer).AddVideoResponse,
			chunked: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.VideoPart{Bytes: want, MediaType: "video/mp4"}), videoChunks: chunks}
			},
			part: func(msgs []messages.Message) ([]byte, string, bool) {
				p, ok := videoPartFrom(msgs)
				return p.Bytes, p.MediaType, ok
			},
			start: messages.StreamTypeVideoStart, delta: messages.StreamTypeVideoDelta, end: messages.StreamTypeVideoEnd,
		},
	}
}

// TestMediaNonStreamingResponse validates that a media-only model response is
// assembled in non-streaming (Execute) mode: one assistant message carrying
// the configured bytes and media type, the kind's START/DELTA/END deltas, and
// exactly one inference call.
func TestMediaNonStreamingResponse(t *testing.T) {
	for _, test := range mediaResponseCases() {
		t.Run(test.name, func(t *testing.T) {
			inf := test.add(new(MockInferencer), test.bytes, test.mediaType)
			s := NewScenario(t, inf, NewMockToolExecutor())
			result := s.Execute(test.prompt)

			if len(result.Messages) != 2 {
				t.Fatalf("message count: got %d, want 2", len(result.Messages))
			}
			if result.Messages[1].Role != messages.RoleAssistant {
				t.Errorf("message role: got %q, want %q", result.Messages[1].Role, messages.RoleAssistant)
			}
			data, mediaType, found := test.part(result.Messages)
			if !found {
				t.Fatalf("no %s part found in response messages", test.name)
			}
			if !bytes.Equal(data, test.bytes) {
				t.Errorf("part bytes: got %v, want %v", data, test.bytes)
			}
			if mediaType != test.mediaType {
				t.Errorf("part media type: got %q, want %q", mediaType, test.mediaType)
			}
			assertMediaLifecycle(t, s, test, 1)
			if inf.CallCount() != 1 {
				t.Errorf("inference calls: got %d, want 1", inf.CallCount())
			}
		})
	}
}

// TestMediaStreamingResponse validates that a media-only response streams
// no text and publishes the kind's START/DELTA/END deltas from one call.
func TestMediaStreamingResponse(t *testing.T) {
	for _, test := range mediaResponseCases() {
		t.Run(test.name, func(t *testing.T) {
			inf := test.add(new(MockInferencer), test.bytes, test.mediaType)
			s := NewScenario(t, inf, NewMockToolExecutor())
			if streamText := s.ExecuteStreamingText(test.prompt); streamText != "" {
				t.Errorf("stream text: got %q, want empty string", streamText)
			}
			assertMediaLifecycle(t, s, test, 1)
			if inf.CallCount() != 1 {
				t.Errorf("inference calls: got %d, want 1", inf.CallCount())
			}
		})
	}
}

// TestChunkedStreamingResponse validates that multiple media DELTA chunks
// from the inferencer stream are all delivered. The result carries the full
// concatenated bytes (what Infer returns), while the per-kind chunks drive
// the chunked InferStream emission.
func TestChunkedStreamingResponse(t *testing.T) {
	for _, test := range mediaResponseCases() {
		t.Run(test.name, func(t *testing.T) {
			inf := &MockInferencer{}
			inf.entries = []inferenceEntry{test.chunked(test.bytes, test.chunks)}
			s := NewScenario(t, inf, NewMockToolExecutor())
			s.ExecuteStreamingText(test.prompt)
			assertMediaLifecycle(t, s, test, len(test.chunks))
		})
	}
}

// assertMediaLifecycle asserts the kind's START, deltas DELTAs, and END
// deltas appear in order with the assistant role.
func assertMediaLifecycle(t *testing.T, s *Scenario, test mediaResponseCase, deltas int) {
	t.Helper()
	want := []ExpectedDelta{{Type: test.start, Role: messages.RoleAssistant}}
	for range deltas {
		want = append(want, ExpectedDelta{Type: test.delta, Role: messages.RoleAssistant})
	}
	AssertDeltaContains(t, s.Deltas(), append(want, ExpectedDelta{Type: test.end, Role: messages.RoleAssistant}))
}

// mediaResult is an assistant inference result carrying one media part.
func mediaResult(part messages.ContentPart) messages.InferenceResult {
	return messages.InferenceResult{
		Message: messages.Message{
			Role:         messages.RoleAssistant,
			ContentParts: []messages.ContentPart{part},
		},
	}
}
