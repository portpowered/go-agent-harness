package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/input"
	"github.com/portpowered/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// TestAskFilesParseArgs is a smoke test that the input package and file-input tests are compiled.
func TestAskFilesParseArgs(t *testing.T) {
	prompt, paths := input.ParseAskArgs([]string{"hello", "world"})
	if prompt != "hello world" || len(paths) != 0 {
		t.Errorf("ParseAskArgs([\"hello\", \"world\"]): got prompt=%q paths=%v", prompt, paths)
	}
}

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

// TestAskWithGenericFile runs ask with a prompt and a generic file path and asserts the
// inferencer receives a user message containing a FilePart with the correct name.
func TestAskWithGenericFile(t *testing.T) {
	tmpDir := t.TempDir()
	docPath := filepath.Join(tmpDir, "notes.txt")
	if err := os.WriteFile(docPath, []byte("hello world"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	rec := &recordingInferencer{response: "OK."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "read this file", docPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.hasUserMessageWithFilePart("notes.txt") {
		t.Error("inference request should contain a user message with a FilePart named notes.txt; recorded messages did not")
	}
	if !rec.containsSystemPrompt("read this file") {
		t.Error("inference request should contain prompt text \"read this file\"")
	}
}

// TestAskWithPromptAndFileOrder runs ask with prompt then file (as in README: "describe this picture" ./file.jpg)
// and asserts both prompt and file are present.
func TestAskWithPromptAndFileOrder(t *testing.T) {
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "pic.png")
	if err := os.WriteFile(imgPath, []byte("png content"), 0644); err != nil {
		t.Fatalf("write image file: %v", err)
	}

	rec := &recordingInferencer{response: "Done."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "describe this picture for me", imgPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.hasUserMessageWithImagePart("image/png") {
		t.Error("expected user message to contain ImagePart image/png")
	}
	if !rec.containsSystemPrompt("describe this picture for me") {
		t.Error("expected prompt text in request")
	}
}

// TestAskWithFileThenPrompt runs ask with file path first then prompt (as in README: ./recording.mp4 "is that a fish?")
// and asserts both are present.
func TestAskWithFileThenPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4 content"), 0644); err != nil {
		t.Fatalf("write video file: %v", err)
	}

	rec := &recordingInferencer{response: "Maybe."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", videoPath, "is that a fish or a banana?"})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// Video is sent as VideoPart
	var hasVideo bool
	for _, msgs := range rec.recorded {
		for _, m := range msgs {
			if m.Role != messages.RoleUser {
				continue
			}
			for _, p := range m.ContentParts {
				if _, ok := p.(messages.VideoPart); ok {
					hasVideo = true
					break
				}
			}
		}
	}
	if !hasVideo {
		t.Error("expected user message to contain VideoPart for .mp4 file")
	}
	if !rec.containsSystemPrompt("is that a fish or a banana?") {
		t.Error("expected prompt text in request")
	}
}

// TestAskWithConfigDirAndImageFile runs ask with --config-dir and a file (AGENTS.md test style)
// to ensure file parsing works when flags are present.
func TestAskWithConfigDirAndImageFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	configYAML := `model:
  provider: openrouter
  openrouter:
    model: z-ai/glm-4.7
    api_key: test-key
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	imgPath := filepath.Join(tmpDir, "x.jpg")
	if err := os.WriteFile(imgPath, []byte("jpeg"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	rec := &recordingInferencer{response: "OK"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeAgentCLIWithInferencerOverride(exec, rec)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "--config-dir", tmpDir, "what is this?", imgPath})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.hasUserMessageWithImagePart("image/jpeg") {
		t.Error("expected user message to contain ImagePart when using --config-dir and image file")
	}
	if !rec.containsSystemPrompt("what is this?") {
		t.Error("expected prompt in request")
	}
}

// TestAskNonexistentPathTreatedAsPrompt runs ask with a path that does not exist.
// Such args are not treated as file paths (ParseAskArgs only adds existing files), so they become prompt text and the command succeeds.
func TestAskNonexistentPathTreatedAsPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistent := filepath.Join(tmpDir, "does-not-exist.jpg")

	rec := &recordingInferencer{response: "OK"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "prompt", nonexistent})

	ctx := context.Background()
	err = rootCmd.ExecuteContext(ctx)
	if err != nil {
		t.Fatalf("unexpected error (nonexistent path is treated as prompt): %v", err)
	}
	// Nonexistent path was included in prompt text, not loaded as file
	if !rec.containsSystemPrompt("prompt") {
		t.Error("expected prompt text in request")
	}
}
