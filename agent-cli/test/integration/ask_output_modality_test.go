package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// audioBinaryInferencer returns a streaming response containing AudioDelta events.
type audioBinaryInferencer struct {
	audioBytes []byte
}

func (b *audioBinaryInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: b.audioBytes, MediaType: "audio/mpeg"}},
	}
	return messages.InferenceResult{Message: msg}, nil
}

func (b *audioBinaryInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(b.audioBytes)}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)
	return ch, nil
}

// embeddingBinaryInferencer returns a streaming response containing EmbeddingDelta events.
type embeddingBinaryInferencer struct {
	embBytes []byte
}

func (b *embeddingBinaryInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	msg := messages.Message{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.EmbeddingPart{Bytes: b.embBytes, MediaType: "application/octet-stream"}},
	}
	return messages.InferenceResult{Message: msg}, nil
}

func (b *embeddingBinaryInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeEmbeddingDelta, Value: messages.NewEmbeddingDeltaValue(b.embBytes)}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)
	return ch, nil
}

// TestAskOutputModalityAudio creates a temporary embedding file, runs ask with
// --stream --output-modality audio, and asserts that the raw audio bytes are written to stdout.
func TestAskOutputModalityAudio(t *testing.T) {
	fakeAudioBytes := []byte{0xFF, 0xFB, 0x90, 0x00, 0x01, 0x02, 0x03} // fake MP3 bytes

	tmpDir := t.TempDir()
	embeddingPath := filepath.Join(tmpDir, "speaker.bin")
	if err := os.WriteFile(embeddingPath, []byte("fake embedding content"), 0644); err != nil {
		t.Fatalf("write embedding file: %v", err)
	}

	inf := &audioBinaryInferencer{audioBytes: fakeAudioBytes}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "--stream", "--output-modality", "audio", "say hello", embeddingPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	got := testWriter.stdout.Bytes()
	if !bytes.Equal(got, fakeAudioBytes) {
		t.Errorf("stdout bytes = %v, want %v", got, fakeAudioBytes)
	}
}

// TestAskOutputModalityEmbedding creates a temporary audio file, runs ask with
// --stream --output-modality embedding, and asserts that the raw embedding bytes are written to stdout.
func TestAskOutputModalityEmbedding(t *testing.T) {
	fakeEmbBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05} // fake embedding bytes

	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "voice.mp3")
	if err := os.WriteFile(audioPath, []byte("fake mp3 content"), 0644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	inf := &embeddingBinaryInferencer{embBytes: fakeEmbBytes}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "--stream", "--output-modality", "embedding", "extract voice", audioPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	got := testWriter.stdout.Bytes()
	if !bytes.Equal(got, fakeEmbBytes) {
		t.Errorf("stdout bytes = %v, want %v", got, fakeEmbBytes)
	}
}
