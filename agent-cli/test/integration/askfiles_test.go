package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// hasUserMessageWithImagePart returns true if any recorded Infer call had a user message
// containing an ImagePart with the given media type (or any image if mediaType is "").
func (r *recordingInferencer) hasUserMessageWithImagePart(mediaType string) bool {
	for _, msgs := range r.recorded {
		for _, m := range msgs {
			if m.Role != messages.RoleUser {
				continue
			}
			for _, p := range m.ContentParts {
				if ip, ok := p.(messages.ImagePart); ok {
					if mediaType == "" || ip.MediaType == mediaType {
						return true
					}
				}
			}
		}
	}
	return false
}

// hasUserMessageWithAudioPart returns true if any recorded Infer call had a user message
// containing an AudioPart with the given media type (or any audio if mediaType is "").
func (r *recordingInferencer) hasUserMessageWithAudioPart(mediaType string) bool {
	for _, msgs := range r.recorded {
		for _, m := range msgs {
			if m.Role != messages.RoleUser {
				continue
			}
			for _, p := range m.ContentParts {
				if ap, ok := p.(messages.AudioPart); ok {
					if mediaType == "" || ap.MediaType == mediaType {
						return true
					}
				}
			}
		}
	}
	return false
}

// hasUserMessageWithFilePart returns true if any recorded Infer call had a user message
// containing a FilePart with the given name (or any file if name is "").
func (r *recordingInferencer) hasUserMessageWithFilePart(name string) bool {
	for _, msgs := range r.recorded {
		for _, m := range msgs {
			if m.Role != messages.RoleUser {
				continue
			}
			for _, p := range m.ContentParts {
				if fp, ok := p.(messages.FilePart); ok {
					if name == "" || fp.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestAskWithImageFile runs ask with a prompt and an image file path and asserts the
// inferencer receives a user message containing an ImagePart (e.g. image/jpeg).
func TestAskWithImageFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Minimal content; extension drives MIME type in LoadContentPart
	imgPath := filepath.Join(tmpDir, "photo.jpg")
	if err := os.WriteFile(imgPath, []byte("\xff\xd8\xff fake jpeg"), 0644); err != nil {
		t.Fatalf("write image file: %v", err)
	}

	rec := &recordingInferencer{response: "Looks like an image."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "describe this picture", imgPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.hasUserMessageWithImagePart("image/jpeg") {
		t.Error("inference request should contain a user message with an ImagePart (image/jpeg); recorded messages did not")
	}
	if !rec.containsSystemPrompt("describe this picture") {
		t.Error("inference request should contain prompt text \"describe this picture\"")
	}
}

// TestAskWithAudioFile runs ask with a prompt and an audio file path and asserts the
// inferencer receives a user message containing an AudioPart.
func TestAskWithAudioFile(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "recording.mp3")
	if err := os.WriteFile(audioPath, []byte("fake mp3 content"), 0644); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	rec := &recordingInferencer{response: "Sounds good."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "what is this audio?", audioPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.hasUserMessageWithAudioPart("audio/mpeg") {
		t.Error("inference request should contain a user message with an AudioPart (audio/mpeg); recorded messages did not")
	}
	if !rec.containsSystemPrompt("what is this audio?") {
		t.Error("inference request should contain prompt text \"what is this audio?\"")
	}
}
