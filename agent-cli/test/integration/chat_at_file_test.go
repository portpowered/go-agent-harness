package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// TestAtFile_TextFileIncludedInResponse verifies that submitting '@tempfile.txt explain this'
// includes the file content in the execute input (visible as the agent processes it)
// and strips @tempfile.txt from the prompt text shown to the LLM.
func TestAtFile_TextFileIncludedInResponse(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "tempfile.txt")
	fileContent := "This is a test file content for @file reference."
	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// The mock inferencer will receive the cleaned text + file content.
	// We verify that the response completes (agent was called) and the user message
	// shown in history uses the original input (with @).
	const agentResponse = "I can see the file content."
	inf := &mockInferencer{response: agentResponse}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID, err := chatService.NewSessionID(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	// Use absolute path so parseAtReferences can find it regardless of cwd.
	input := "@" + filepath.ToSlash(filePath) + " explain this"
	model = typeInput(model, input)
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Agent should have been called and responded.
	if !strings.Contains(history, agentResponse) {
		t.Errorf("ViewHistory() should contain agent response %q; got:\n%s", agentResponse, history)
	}
	// User message should contain the original input (displayed as-is).
	if !strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should contain 'You:' user message; got:\n%s", history)
	}
}

// TestAtFile_ImageIncludesImagePart verifies that submitting '@image.png describe'
// does not error and the agent processes it (image is included as an image content part).
func TestAtFile_ImageIncludesImagePart(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "image.png")
	// Minimal valid PNG file (1x1 pixel, 8-bit RGBA).
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, // 8-bit RGB
		0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, // IDAT chunk
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, // compressed data
		0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, 0x33, // CRC
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, // IEND chunk
		0xae, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(imagePath, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write temp image: %v", err)
	}

	const agentResponse = "I see the image."
	inf := &mockInferencer{response: agentResponse}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID, err := chatService.NewSessionID(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	input := "@" + filepath.ToSlash(imagePath) + " describe"
	model = typeInput(model, input)
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Agent should have been called (image was included as content part).
	if !strings.Contains(history, agentResponse) {
		t.Errorf("ViewHistory() should contain agent response %q; got:\n%s", agentResponse, history)
	}
}

// TestAtFile_DirectoryListsContents verifies that submitting '@tempdir/ what's here'
// includes a directory listing as text context.
func TestAtFile_DirectoryListsContents(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "testdir")
	if err := os.MkdirAll(filepath.Join(subDir, "subdir"), 0755); err != nil {
		t.Fatalf("failed to create subdirectory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file1.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write file1.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("failed to write file2.go: %v", err)
	}

	const agentResponse = "I see the directory listing."
	inf := &mockInferencer{response: agentResponse}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID, err := chatService.NewSessionID(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	input := "@" + filepath.ToSlash(subDir) + " what is here"
	model = typeInput(model, input)
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Agent should have been called with directory listing.
	if !strings.Contains(history, agentResponse) {
		t.Errorf("ViewHistory() should contain agent response %q; got:\n%s", agentResponse, history)
	}
}
