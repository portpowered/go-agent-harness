package messages

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var updateReconstructionGoldens = flag.Bool("update", false, "update reconstruction golden files")

// The fixtures are intentionally embedded so ordinary test runs only compare
// against the committed files. The -update flag is the only path that writes
// them back to the worktree.
//
//go:embed testdata/reconstruction/golden/*.json testdata/reconstruction/fuzz-seeds.json
var reconstructionFixtures embed.FS

type reconstructionGolden struct {
	Name     string                        `json:"name"`
	Messages []reconstructionGoldenMessage `json:"messages"`
}

type reconstructionGoldenMessage struct {
	Role         Role                           `json:"role"`
	ContentParts []reconstructionGoldenPart     `json:"content_parts"`
	Refusal      string                         `json:"refusal"`
	ToolCalls    []reconstructionGoldenToolCall `json:"tool_calls"`
	ToolCallID   string                         `json:"tool_call_id"`
}

type reconstructionGoldenToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type reconstructionGoldenPart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Bytes     []byte `json:"bytes,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

type reconstructionFuzzSeed struct {
	Name        string `json:"name"`
	Controls    string `json:"controls"`
	Text        string `json:"text"`
	Payload     string `json:"payload"`
	Tools       int    `json:"tools"`
	Interrupted bool   `json:"interrupted"`
}

func TestReconstructMessages_S3Goldens(t *testing.T) {
	tests := []struct {
		name   string
		deltas []StreamMessage
		check  func(*testing.T, []Message)
	}{
		{
			name: "text",
			deltas: []StreamMessage{
				modelDelta(StreamTypeMessageStart, NewMessageStartValue()),
				modelDelta(StreamTypeTextStart, NewTextStartValue()),
				modelDelta(StreamTypeTextDelta, NewTextDeltaValue("Hello, ")),
				modelDelta(StreamTypeTextDelta, NewTextDeltaValue("世界\n")),
				modelDelta(StreamTypeTextEnd, NewTextEndValue()),
				modelDelta(StreamTypeMessageEnd, NewMessageEndValue(TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7})),
			},
			check: func(t *testing.T, messages []Message) {
				t.Helper()
				if len(messages) != 1 {
					t.Fatalf("got %d messages, want 1", len(messages))
				}
				assertModelText(t, messages[0], "Hello, 世界\n")
			},
		},
		{
			name: "tool-call",
			deltas: []StreamMessage{
				modelDelta(StreamTypeMessageStart, NewMessageStartValue()),
				modelDelta(StreamTypeTextStart, NewTextStartValue()),
				modelDelta(StreamTypeTextDelta, NewTextDeltaValue("I will inspect the file.")),
				modelDelta(StreamTypeToolCallStart, NewToolCallStartValue("call-42", "read_file")),
				modelDelta(StreamTypeToolCallDelta, NewToolCallDeltaValue(`{"path":"report.txt"}`)),
				// Empty ID and name exercise the stream-state fallback used by
				// providers that only include them in TOOLCALL.START.
				modelDelta(StreamTypeToolCallEnd, NewToolCallEndValue("", "", `{"path":"report.txt","line":7}`)),
				modelDelta(StreamTypeMessageEnd, NewMessageEndValue(TokenUsage{})),
			},
			check: func(t *testing.T, messages []Message) {
				t.Helper()
				if len(messages) != 1 {
					t.Fatalf("got %d messages, want 1", len(messages))
				}
				msg := messages[0]
				assertModelText(t, msg, "I will inspect the file.")
				if len(msg.ToolCalls) != 1 {
					t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
				}
				want := ToolCall{ID: "call-42", Name: "read_file", Arguments: `{"path":"report.txt","line":7}`}
				if !reflect.DeepEqual(msg.ToolCalls[0], want) {
					t.Fatalf("got tool call %+v, want %+v", msg.ToolCalls[0], want)
				}
			},
		},
		{
			name: "interrupted",
			deltas: []StreamMessage{
				modelDelta(StreamTypeMessageStart, NewMessageStartValue()),
				modelDelta(StreamTypeTextStart, NewTextStartValue()),
				modelDelta(StreamTypeTextDelta, NewTextDeltaValue("partial output before interruption")),
			},
			check: func(t *testing.T, messages []Message) {
				t.Helper()
				if len(messages) != 1 {
					t.Fatalf("got %d messages, want 1", len(messages))
				}
				assertModelText(t, messages[0], "partial output before interruption")
			},
		},
		{
			name:   "empty",
			deltas: nil,
			check: func(t *testing.T, messages []Message) {
				t.Helper()
				if len(messages) != 1 {
					t.Fatalf("got %d messages, want 1", len(messages))
				}
				msg := messages[0]
				if msg.Role != RoleAssistant {
					t.Errorf("got role %q, want %q", msg.Role, RoleAssistant)
				}
				if msg.ContentParts != nil {
					t.Errorf("got content parts %#v, want nil for an empty stream", msg.ContentParts)
				}
				if msg.ToolCalls != nil {
					t.Errorf("got tool calls %#v, want nil for an empty stream", msg.ToolCalls)
				}
				if msg.Refusal != "" {
					t.Errorf("got refusal %q, want empty", msg.Refusal)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := []Message{ReconstructModelMessageFromDeltas(tc.deltas)}
			tc.check(t, got)
			assertReconstructionGolden(t, tc.name, got)
		})
	}
}

func TestReconstructToolMessages_S3Golden(t *testing.T) {
	const (
		firstTool  = "tool-first"
		secondTool = "tool-second"
	)

	// The first batch is deliberately discarded by the second MESSAGE.START.
	// Orphaned deltas exercise the safe no-state branches before the real tools.
	deltas := []StreamMessage{
		{Type: StreamTypeMessageStart, Value: NewMessageStartValue()},
		{Type: StreamTypeTextStart, ToolCallId: "discarded", Value: NewTextStartValue()},
		{Type: StreamTypeTextDelta, ToolCallId: "discarded", Value: NewTextDeltaValue("discarded")},
		{Type: StreamTypeMessageStart, Value: NewMessageStartValue()},
		{Type: StreamTypeTextDelta, ToolCallId: "orphan", Value: NewTextDeltaValue("ignored")},
		{Type: StreamTypeImageDelta, ToolCallId: "orphan", Value: NewImageDeltaValue([]byte{0x01})},
		{Type: StreamTypeAudioDelta, ToolCallId: "orphan", Value: NewAudioDeltaValue([]byte{0x02})},
		{Type: StreamTypeVideoDelta, ToolCallId: "orphan", Value: NewVideoDeltaValue([]byte{0x03})},
		{Type: StreamTypeFileDelta, ToolCallId: "orphan", Value: NewFileDeltaValue([]byte{0x04})},
		{Type: StreamTypeEmbeddingDelta, ToolCallId: "orphan", Value: NewEmbeddingDeltaValue([]byte{0x05})},
		{Type: StreamTypeTextStart, ToolCallId: firstTool, Value: NewTextStartValue()},
		{Type: StreamTypeTextDelta, ToolCallId: firstTool, Value: NewTextDeltaValue("tool output")},
		{Type: StreamTypeImageStart, ToolCallId: firstTool, Value: NewImageStartValue("image/png")},
		{Type: StreamTypeImageDelta, ToolCallId: firstTool, Value: NewImageDeltaValue([]byte{0x10, 0x00})},
		{Type: StreamTypeAudioStart, ToolCallId: firstTool, Value: NewAudioStartValue()},
		{Type: StreamTypeAudioDelta, ToolCallId: firstTool, Value: NewAudioDeltaValue([]byte{0x20, 0x00})},
		{Type: StreamTypeVideoStart, ToolCallId: firstTool, Value: NewVideoStartValue("video/mp4")},
		{Type: StreamTypeVideoDelta, ToolCallId: firstTool, Value: NewVideoDeltaValue([]byte{0x30, 0x00})},
		{Type: StreamTypeFileStart, ToolCallId: firstTool, Value: NewFileStartValue("application/pdf", "result.pdf")},
		{Type: StreamTypeFileDelta, ToolCallId: firstTool, Value: NewFileDeltaValue([]byte{0x40, 0x00})},
		{Type: StreamTypeEmbeddingStart, ToolCallId: firstTool, Value: NewEmbeddingStartValue("application/octet-stream")},
		{Type: StreamTypeEmbeddingDelta, ToolCallId: firstTool, Value: NewEmbeddingDeltaValue([]byte{0x50, 0x00})},
		{Type: StreamTypeTextStart, ToolCallId: secondTool, Value: NewTextStartValue()},
	}

	got := ReconstructToolMessagesFromDeltas(deltas)
	if len(got) != 2 {
		t.Fatalf("got %d tool messages, want 2", len(got))
	}
	if got[0].ToolCallID != firstTool || got[1].ToolCallID != secondTool {
		t.Fatalf("got tool order [%q, %q], want [%q, %q]", got[0].ToolCallID, got[1].ToolCallID, firstTool, secondTool)
	}
	if len(got[0].ContentParts) != 6 {
		t.Fatalf("got %d content parts for first tool, want 6", len(got[0].ContentParts))
	}
	if len(got[1].ContentParts) != 1 {
		t.Fatalf("got %d content parts for empty second tool, want 1", len(got[1].ContentParts))
	}
	if empty, ok := got[1].ContentParts[0].(TextPart); !ok || empty.Text != "" {
		t.Fatalf("got %#v for empty second tool, want empty TextPart", got[1].ContentParts[0])
	}

	assertReconstructionGolden(t, "tool-results", got)
}

func TestReconstructModelMessageFromDeltas_AdditionalBranches(t *testing.T) {
	deltas := []StreamMessage{
		modelDelta(StreamTypeReasoningStart, NewReasoningStartValue()),
		modelDelta(StreamTypeReasoningDelta, NewReasoningDeltaValue("reason")),
		modelDelta(StreamTypeReasoningEnd, NewReasoningEndValue()),
		modelDelta(StreamTypeTranscriptStart, NewTranscriptStartValue()),
		modelDelta(StreamTypeTranscriptDelta, NewTranscriptDeltaValue("interim")),
		modelDelta(StreamTypeTranscriptEnd, NewTranscriptEndValue("final transcript")),
		modelDelta(StreamTypeAudioDelta, NewAudioDeltaValueWithMediaType([]byte{0x01, 0x02}, "audio/wav")),
		modelDelta(StreamTypeVADSpeechStarted, NewVADSpeechStartedValue()),
		modelDelta(StreamTypeVADSpeechStopped, NewVADSpeechStoppedValue()),
	}

	msg := ReconstructModelMessageFromDeltas(deltas)
	if len(msg.ContentParts) != 3 {
		t.Fatalf("got %d content parts, want 3", len(msg.ContentParts))
	}
	if got, ok := msg.ContentParts[0].(ReasoningPart); !ok || got.Reasoning != "<thinking>\nreason\n</thinking>" {
		t.Errorf("reasoning part: got %#v", msg.ContentParts[0])
	}
	if got, ok := msg.ContentParts[1].(TranscriptPart); !ok || got.Text != "final transcript" {
		t.Errorf("transcript part: got %#v", msg.ContentParts[1])
	}
	if got, ok := msg.ContentParts[2].(AudioPart); !ok || got.MediaType != "audio/wav" || !bytes.Equal(got.Bytes, []byte{0x01, 0x02}) {
		t.Errorf("audio part: got %#v", msg.ContentParts[2])
	}
}

// FuzzReconstructMessages_S7RoundTrip proves that provider-controlled byte
// boundaries do not alter the reconstructed model or tool messages. The seed
// corpus lives under testdata/reconstruction so boundary cases are reviewed as
// ordinary fixtures as well as being used by go test's fuzz runner.
func FuzzReconstructMessages_S7RoundTrip(f *testing.F) {
	var seeds []reconstructionFuzzSeed
	seedBytes, err := reconstructionFixtures.ReadFile("testdata/reconstruction/fuzz-seeds.json")
	if err != nil {
		f.Fatalf("read fuzz seeds: %v", err)
	}
	if err := json.Unmarshal(seedBytes, &seeds); err != nil {
		f.Fatalf("decode fuzz seeds: %v", err)
	}
	for _, seed := range seeds {
		controls, err := base64.StdEncoding.DecodeString(seed.Controls)
		if err != nil {
			f.Fatalf("decode %q controls: %v", seed.Name, err)
		}
		payload, err := base64.StdEncoding.DecodeString(seed.Payload)
		if err != nil {
			f.Fatalf("decode %q payload: %v", seed.Name, err)
		}
		f.Add(controls, seed.Text, payload, seed.Tools, seed.Interrupted)
	}

	f.Fuzz(func(t *testing.T, controls []byte, text string, payload []byte, toolCount int, interrupted bool) {
		// Keep fuzz cases bounded while retaining the exact bytes that the
		// subject receives. Truncating the generated input is itself part of
		// the test case, not a normalization of reconstructed output.
		if len(text) > 4096 {
			text = text[:4096]
		}
		if len(payload) > 4096 {
			payload = payload[:4096]
		}
		if len(controls) > 256 {
			controls = controls[:256]
		}

		model := ReconstructModelMessageFromDeltas(fuzzModelDeltas(controls, text, payload, interrupted))
		wantModel := Message{
			Role: RoleAssistant,
			ContentParts: []ContentPart{
				NewTextPart(text),
				ImagePart{Bytes: append([]byte{}, payload...), MediaType: "image/fuzz"},
			},
			ToolCalls: []ToolCall{{ID: "fuzz-call", Name: "fuzz_tool", Arguments: `{"value":true}`}},
		}
		if !reflect.DeepEqual(model, wantModel) {
			t.Fatalf("model round trip changed message:\n got: %#v\nwant: %#v", model, wantModel)
		}

		if toolCount < 1 {
			toolCount = 1
		}
		if toolCount > 3 {
			toolCount = 3
		}
		tool := ReconstructToolMessagesFromDeltas(fuzzToolDeltas(controls, text, payload, toolCount))
		wantTool := make([]Message, toolCount)
		for i := range wantTool {
			toolID := fmt.Sprintf("fuzz-result-%d", i)
			wantToolParts := []ContentPart{
				FilePart{Bytes: append([]byte{}, payload...), MediaType: "application/octet-stream", Name: "fuzz.bin"},
			}
			if text != "" {
				wantToolParts = append([]ContentPart{NewTextPart(text)}, wantToolParts...)
			}
			wantTool[i] = Message{Role: RoleTool, ContentParts: wantToolParts, ToolCallID: toolID}
		}
		if !reflect.DeepEqual(tool, wantTool) {
			t.Fatalf("tool round trip changed message:\n got: %#v\nwant: %#v", tool, wantTool)
		}
	})
}

// The current public reconstruction result has no completion marker. Keep the
// intended assertion visible and skipped until the API can distinguish a
// positive partial result from a complete message.
func TestReconstructModelMessageFromDeltas_TruncatedStreamContract(t *testing.T) {
	deltas := []StreamMessage{
		modelDelta(StreamTypeMessageStart, NewMessageStartValue()),
		modelDelta(StreamTypeTextStart, NewTextStartValue()),
		modelDelta(StreamTypeTextDelta, NewTextDeltaValue("partial")),
	}
	got := ReconstructModelMessageFromDeltas(deltas)
	if got.TextContent() != "partial" {
		t.Fatalf("current partial payload changed: got %q", got.TextContent())
	}
	t.Skip("DEFECT: reconstruction has no partial/completion marker; add the intended partial-result assertion when the public contract exposes one")
}

// StreamMessage carries ordering metadata, but reconstruction currently ignores
// it and returns no typed reorder error. Keep the intended safety case skipped
// rather than blessing silently reordered content as a supported contract.
func TestReconstructModelMessageFromDeltas_OutOfOrderContract(t *testing.T) {
	deltas := []StreamMessage{
		{Type: StreamTypeTextDelta, GlobalIndex: 2, Value: NewTextDeltaValue("second")},
		{Type: StreamTypeTextDelta, GlobalIndex: 1, Value: NewTextDeltaValue("first")},
	}
	got := ReconstructModelMessageFromDeltas(deltas)
	if got.TextContent() != "secondfirst" {
		t.Fatalf("unexpected current observation: got %q", got.TextContent())
	}
	t.Skip("DEFECT: reconstruction ignores GlobalIndex and has no typed out-of-order error; add canonical-order or typed-error assertion when the public contract exists")
}

func modelDelta(typ StreamMessageType, value StreamMessageValue) StreamMessage {
	return StreamMessage{Type: typ, Role: RoleAssistant, Value: value}
}

func assertModelText(t *testing.T, msg Message, want string) {
	t.Helper()
	if msg.Role != RoleAssistant {
		t.Errorf("got role %q, want %q", msg.Role, RoleAssistant)
	}
	if len(msg.ContentParts) != 1 {
		t.Fatalf("got %d content parts, want 1", len(msg.ContentParts))
	}
	part, ok := msg.ContentParts[0].(TextPart)
	if !ok {
		t.Fatalf("got %T content part, want TextPart", msg.ContentParts[0])
	}
	if part.Text != want {
		t.Errorf("got text %q, want %q", part.Text, want)
	}
}

func assertReconstructionGolden(t *testing.T, name string, messages []Message) {
	t.Helper()
	want := reconstructionGolden{Name: name}
	if messages != nil {
		want.Messages = make([]reconstructionGoldenMessage, len(messages))
		for i, msg := range messages {
			golden, err := goldenMessage(msg)
			if err != nil {
				t.Fatalf("serialize message %d: %v", i, err)
			}
			want.Messages[i] = golden
		}
	}
	encoded, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	encoded = append(encoded, '\n')

	path := filepath.FromSlash("testdata/reconstruction/golden/" + name + ".json")
	if *updateReconstructionGoldens {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	fixture, err := reconstructionFixtures.ReadFile(filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(fixture, encoded) {
		t.Errorf("golden %s differs; run with -update only after reviewing the behavior change\n got:\n%s\nwant:\n%s", name, fixture, encoded)
	}
}

func goldenMessage(msg Message) (reconstructionGoldenMessage, error) {
	golden := reconstructionGoldenMessage{
		Role:       msg.Role,
		Refusal:    msg.Refusal,
		ToolCallID: msg.ToolCallID,
	}
	if msg.ContentParts != nil {
		golden.ContentParts = make([]reconstructionGoldenPart, len(msg.ContentParts))
		for i, part := range msg.ContentParts {
			converted, err := goldenPart(part)
			if err != nil {
				return reconstructionGoldenMessage{}, fmt.Errorf("content part %d: %w", i, err)
			}
			golden.ContentParts[i] = converted
		}
	}
	if msg.ToolCalls != nil {
		golden.ToolCalls = make([]reconstructionGoldenToolCall, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			golden.ToolCalls[i] = reconstructionGoldenToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		}
	}
	return golden, nil
}

func goldenPart(part ContentPart) (reconstructionGoldenPart, error) {
	switch part := part.(type) {
	case TextPart:
		return reconstructionGoldenPart{Type: "text", Text: part.Text}, nil
	case ReasoningPart:
		return reconstructionGoldenPart{Type: "reasoning", Reasoning: part.Reasoning}, nil
	case TranscriptPart:
		return reconstructionGoldenPart{Type: "transcript", Text: part.Text}, nil
	case AudioPart:
		return reconstructionGoldenPart{Type: "audio", Bytes: part.Bytes, MediaType: part.MediaType}, nil
	case ImagePart:
		return reconstructionGoldenPart{Type: "image", Bytes: part.Bytes, MediaType: part.MediaType}, nil
	case VideoPart:
		return reconstructionGoldenPart{Type: "video", Bytes: part.Bytes, MediaType: part.MediaType}, nil
	case FilePart:
		return reconstructionGoldenPart{Type: "file", Bytes: part.Bytes, MediaType: part.MediaType, Name: part.Name}, nil
	case EmbeddingPart:
		return reconstructionGoldenPart{Type: "embedding", Bytes: part.Bytes, MediaType: part.MediaType}, nil
	default:
		return reconstructionGoldenPart{}, fmt.Errorf("unsupported content part %T", part)
	}
}

func splitFuzzBytes(data, controls []byte) [][]byte {
	chunks := make([][]byte, 0, len(controls)+1)
	position := 0
	for _, control := range controls {
		remaining := len(data) - position
		if remaining == 0 {
			// Duplicate boundaries are valid provider behavior and should not
			// manufacture or remove bytes.
			chunks = append(chunks, nil)
			continue
		}
		width := int(control) % (remaining + 1)
		if width == 0 {
			chunks = append(chunks, nil)
			continue
		}
		chunk := append([]byte(nil), data[position:position+width]...)
		chunks = append(chunks, chunk)
		position += width
	}
	chunks = append(chunks, append([]byte(nil), data[position:]...))
	return chunks
}

func fuzzModelDeltas(controls []byte, text string, payload []byte, interrupted bool) []StreamMessage {
	deltas := []StreamMessage{
		modelDelta(StreamTypeMessageStart, NewMessageStartValue()),
		modelDelta(StreamTypeTextStart, NewTextStartValue()),
	}
	for _, chunk := range splitFuzzBytes([]byte(text), controls) {
		deltas = append(deltas, modelDelta(StreamTypeTextDelta, NewTextDeltaValue(string(chunk))))
	}
	if !interrupted {
		deltas = append(deltas, modelDelta(StreamTypeTextEnd, NewTextEndValue()))
	}
	deltas = append(deltas,
		modelDelta(StreamTypeToolCallStart, NewToolCallStartValue("fuzz-call", "fuzz_tool")),
		modelDelta(StreamTypeToolCallEnd, NewToolCallEndValue("fuzz-call", "fuzz_tool", `{"value":true}`)),
		modelDelta(StreamTypeImageStart, NewImageStartValue("image/fuzz")),
	)
	for _, chunk := range splitFuzzBytes(payload, controls) {
		deltas = append(deltas, modelDelta(StreamTypeImageDelta, NewImageDeltaValue(chunk)))
	}
	if !interrupted {
		deltas = append(deltas,
			modelDelta(StreamTypeImageEnd, NewImageEndValue()),
			modelDelta(StreamTypeMessageEnd, NewMessageEndValue(TokenUsage{})),
		)
	}
	return deltas
}

func fuzzToolDeltas(controls []byte, text string, payload []byte, toolCount int) []StreamMessage {
	deltas := []StreamMessage{
		{Type: StreamTypeMessageStart, Value: NewMessageStartValue()},
	}
	for i := 0; i < toolCount; i++ {
		toolID := fmt.Sprintf("fuzz-result-%d", i)
		deltas = append(deltas, StreamMessage{Type: StreamTypeTextStart, ToolCallId: toolID, Value: NewTextStartValue()})
		for _, chunk := range splitFuzzBytes([]byte(text), controls) {
			deltas = append(deltas, StreamMessage{Type: StreamTypeTextDelta, ToolCallId: toolID, Value: NewTextDeltaValue(string(chunk))})
		}
		deltas = append(deltas, StreamMessage{
			Type:       StreamTypeFileStart,
			ToolCallId: toolID,
			Value:      NewFileStartValue("application/octet-stream", "fuzz.bin"),
		})
		for _, chunk := range splitFuzzBytes(payload, controls) {
			deltas = append(deltas, StreamMessage{Type: StreamTypeFileDelta, ToolCallId: toolID, Value: NewFileDeltaValue(chunk)})
		}
	}
	return deltas
}
