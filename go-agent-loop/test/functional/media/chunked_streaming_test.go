package media

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestChunkedStreamingResponse validates that multiple media DELTA chunks
// from the inferencer stream are correctly delivered for every generated
// media kind. The result carries the full concatenated bytes (what Infer
// returns), while the per-kind chunks drive the chunked InferStream emission.
func TestChunkedStreamingResponse(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		chunks [][]byte
		entry  func(want []byte, chunks [][]byte) inferenceEntry
		start  messages.StreamMessageType
		delta  messages.StreamMessageType
		end    messages.StreamMessageType
	}{
		{
			name:   "audio",
			prompt: "generate audio",
			chunks: [][]byte{{0x52, 0x49, 0x46, 0x46}, {0x00, 0x00, 0x00, 0x00}},
			entry: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.AudioPart{Bytes: want, MediaType: "audio/pcm"}), audioChunks: chunks}
			},
			start: messages.StreamTypeAudioStart, delta: messages.StreamTypeAudioDelta, end: messages.StreamTypeAudioEnd,
		},
		{
			name:   "image",
			prompt: "generate an image",
			chunks: [][]byte{{0x89, 0x50, 0x4e, 0x47}, {0x0d, 0x0a, 0x1a, 0x0a}},
			entry: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.ImagePart{Bytes: want, MediaType: "image/png"}), imageChunks: chunks}
			},
			start: messages.StreamTypeImageStart, delta: messages.StreamTypeImageDelta, end: messages.StreamTypeImageEnd,
		},
		{
			name:   "video",
			prompt: "generate video",
			chunks: [][]byte{{0x00, 0x00, 0x00, 0x18}, {0x66, 0x74, 0x79, 0x70}},
			entry: func(want []byte, chunks [][]byte) inferenceEntry {
				return inferenceEntry{result: mediaResult(messages.VideoPart{Bytes: want, MediaType: "video/mp4"}), videoChunks: chunks}
			},
			start: messages.StreamTypeVideoStart, delta: messages.StreamTypeVideoDelta, end: messages.StreamTypeVideoEnd,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var want []byte
			for _, chunk := range test.chunks {
				want = append(want, chunk...)
			}
			inf := &MockInferencer{}
			inf.entries = []inferenceEntry{test.entry(want, test.chunks)}

			s := NewScenario(t, inf, NewMockToolExecutor())
			s.ExecuteStreamingText(test.prompt)

			// Both DELTA chunks must appear in the delta buffer.
			AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
				{Type: test.start, Role: messages.RoleAssistant},
				{Type: test.delta, Role: messages.RoleAssistant},
				{Type: test.delta, Role: messages.RoleAssistant},
				{Type: test.end, Role: messages.RoleAssistant},
			})
		})
	}
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
