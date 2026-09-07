package output

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionPresentationWriteResultModes(t *testing.T) {
	msgs := []messages.Message{
		{
			Role:    messages.RoleAssistant,
			Refusal: "not available",
			ToolCalls: []messages.ToolCall{{
				ID: "call-1", Name: "lookup", Arguments: `{"query":"status"}`,
			}},
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "answer"},
				messages.ImagePart{URL: "https://example.test/image.png", MediaType: "image/png"},
				messages.AudioPart{URL: "https://example.test/audio.wav", MediaType: "audio/wav"},
				messages.VideoPart{URL: "https://example.test/video.mp4", MediaType: "video/mp4"},
				messages.FilePart{URL: "https://example.test/report.pdf", MediaType: "application/pdf"},
				messages.ReasoningPart{Reasoning: "because"},
				messages.UsageInfoPart{Usage: messages.TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, ReasoningTokens: 1}},
			},
		},
		{Role: messages.RoleTool, ToolCallID: "call-1"},
	}

	t.Run("plain final", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{}).WriteResult(&out, &diagnostics, msgs, "answer"); err != nil {
			t.Fatalf("WriteResult: %v", err)
		}
		if got, want := out.String(), "answer\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
		if !strings.Contains(diagnostics.String(), "[REFUSAL]") {
			t.Fatalf("diagnostics = %q, want refusal", diagnostics.String())
		}
	})

	t.Run("plain streaming", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{Stream: true}).WriteResult(&out, &diagnostics, nil, "answer"); err != nil {
			t.Fatalf("WriteResult: %v", err)
		}
		if got, want := out.String(), "answer"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})

	t.Run("json", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{JSON: true, Model: "test-model"}).WriteResult(&out, &diagnostics, msgs, "ignored"); err != nil {
			t.Fatalf("WriteResult: %v", err)
		}
		for _, want := range []string{`"role": "assistant"`, `"contentParts"`, `"mediaType": "image/png"`, `"usage_info"`, `"toolCalls"`, `"toolCallId": "call-1"`} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("JSON output = %q, missing %q", out.String(), want)
			}
		}
		if !strings.Contains(diagnostics.String(), `"type":"refusal"`) {
			t.Fatalf("diagnostics = %q, want JSON refusal", diagnostics.String())
		}
	})

	t.Run("binary with content", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		binaryMessages := []messages.Message{{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{
			messages.AudioPart{Bytes: []byte{1, 2, 3}, MediaType: "audio/pcm"},
		}}}
		if err := (SessionPresentation{Modality: "audio"}).WriteResult(&out, &diagnostics, binaryMessages, "ignored"); err != nil {
			t.Fatalf("WriteResult: %v", err)
		}
		if !bytes.Equal(out.Bytes(), []byte{1, 2, 3}) || diagnostics.Len() != 0 {
			t.Fatalf("binary output/diagnostics = %x/%q", out.Bytes(), diagnostics.String())
		}
	})

	t.Run("binary without content", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{Modality: "image"}).WriteResult(&out, &diagnostics, nil, "ignored"); err != nil {
			t.Fatalf("WriteResult: %v", err)
		}
		if out.Len() != 0 || diagnostics.String() != "no image content in response\n" {
			t.Fatalf("binary output/diagnostics = %x/%q", out.Bytes(), diagnostics.String())
		}
	})
}

func TestSessionPresentationWriteStreamModes(t *testing.T) {
	textEvents := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")},
		{Type: messages.StreamTypeRefusal, Value: messages.NewRefusalValue("no")},
	}

	t.Run("json", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{JSON: true, Model: "test-model"}).WriteStream(&out, &diagnostics, newMockStream(textEvents)); err != nil {
			t.Fatalf("WriteStream: %v", err)
		}
		if !strings.Contains(out.String(), `"type":"TEXT.DELTA"`) || !strings.Contains(diagnostics.String(), `"type":"refusal"`) {
			t.Fatalf("output/diagnostics = %q/%q", out.String(), diagnostics.String())
		}
	})

	t.Run("plain", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		if err := (SessionPresentation{}).WriteStream(&out, &diagnostics, newMockStream(textEvents)); err != nil {
			t.Fatalf("WriteStream: %v", err)
		}
		if out.String() != "hello" || !strings.Contains(diagnostics.String(), "[REFUSAL]") {
			t.Fatalf("output/diagnostics = %q/%q", out.String(), diagnostics.String())
		}
	})

	t.Run("binary", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		events := []messages.StreamMessage{{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{4, 5})}}
		if err := (SessionPresentation{Modality: "audio"}).WriteStream(&out, &diagnostics, newMockStream(events)); err != nil {
			t.Fatalf("WriteStream: %v", err)
		}
		if !bytes.Equal(out.Bytes(), []byte{4, 5}) || diagnostics.Len() != 0 {
			t.Fatalf("binary output/diagnostics = %x/%q", out.Bytes(), diagnostics.String())
		}
	})
}

func TestSessionPresentationBinaryClassification(t *testing.T) {
	for _, test := range []struct {
		modality string
		want     bool
	}{
		{modality: "", want: false},
		{modality: "text", want: false},
		{modality: "audio", want: true},
	} {
		if got := (SessionPresentation{Modality: test.modality}).binary(); got != test.want {
			t.Fatalf("binary(%q) = %t, want %t", test.modality, got, test.want)
		}
	}
}

func TestStreamToStdoutWritesContentAndTrailingNewline(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stdout-capture")
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Logf("close stdout capture: %v", err)
		}
	}()
	original := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = original }()

	if err := StreamToStdout(strings.NewReader("hello")); err != nil {
		t.Fatalf("StreamToStdout: %v", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("sync stdout capture: %v", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout capture: %v", err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	if got, want := string(data), "hello\n"; got != want {
		t.Fatalf("captured stdout = %q, want %q", got, want)
	}
}
