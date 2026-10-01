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

// runAskWithAttachment writes an attachment named fileName into a temp dir,
// runs `ask <prompt> <attachment>` against a recording inferencer, and returns
// the inferencer so the caller can assert on the recorded request.
func runAskWithAttachment(t *testing.T, fileName string, content []byte, prompt, response string) *recordingInferencer {
	t.Helper()
	attachmentPath := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(attachmentPath, content, 0644); err != nil {
		t.Fatalf("write attachment %s: %v", fileName, err)
	}

	rec := &recordingInferencer{response: response}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(t.Context(), exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", prompt, attachmentPath})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	if !rec.containsSystemPrompt(prompt) {
		t.Errorf("inference request should contain prompt text %q", prompt)
	}
	return rec
}

// TestAskWithImageFile runs ask with a prompt and an image file path and asserts the
// inferencer receives a user message containing an ImagePart (e.g. image/jpeg).
func TestAskWithImageFile(t *testing.T) {
	// Minimal content; extension drives MIME type in LoadContentPart
	rec := runAskWithAttachment(t, "photo.jpg", []byte("\xff\xd8\xff fake jpeg"), "describe this picture", "Looks like an image.")
	if !rec.hasUserMessageWithImagePart("image/jpeg") {
		t.Error("inference request should contain a user message with an ImagePart (image/jpeg); recorded messages did not")
	}
}

// TestAskWithAudioFile runs ask with a prompt and an audio file path and asserts the
// inferencer receives a user message containing an AudioPart.
func TestAskWithAudioFile(t *testing.T) {
	rec := runAskWithAttachment(t, "recording.mp3", []byte("fake mp3 content"), "what is this audio?", "Sounds good.")
	if !rec.hasUserMessageWithAudioPart("audio/mpeg") {
		t.Error("inference request should contain a user message with an AudioPart (audio/mpeg); recorded messages did not")
	}
}
