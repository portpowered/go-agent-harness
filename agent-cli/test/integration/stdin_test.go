package integration

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

// wavHeaderBytes is a minimal WAV file header recognized by http.DetectContentType as "audio/wave".
// Bytes 0-3: "RIFF", bytes 4-7: size placeholder (zeroes), bytes 8-11: "WAVE".
var wavHeaderBytes = []byte("RIFF\x00\x00\x00\x00WAVE")

// pngHeaderBytes is the 8-byte PNG file signature recognized by http.DetectContentType as "image/png".
var pngHeaderBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// TestAskNoArgsNoStdin validates that running ask with neither args nor piped
// stdin returns an error instead of hanging or panicking.
func TestAskNoArgsNoStdin(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	// SetIn with an empty reader simulates piped-but-empty stdin.
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{"ask"})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected an error when no prompt and no stdin, got nil")
	}
	if !strings.Contains(err.Error(), "no prompt") {
		t.Errorf("error should mention %q, got: %v", "no prompt", err)
	}
}

// TestAskStdinWhitespaceOnlyTreatedAsEmpty validates that stdin containing only
// whitespace is ignored and the arg prompt is used instead.
func TestAskStdinWhitespaceOnlyTreatedAsEmpty(t *testing.T) {
	const argPrompt = "hello"
	const fakeResponse = "Hello!"

	inf := &mockInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(strings.NewReader("   \n\t  ")) // whitespace only
	rootCmd.SetArgs([]string{"ask", argPrompt})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	actual := strings.TrimSpace(testWriter.StdoutString())
	if actual != fakeResponse {
		t.Errorf("output: got %q, want %q", actual, fakeResponse)
	}
}

// TestAskWithStdinAudioBytes validates that piped WAV audio bytes are detected and delivered
// as an AudioPart to the inferencer, not treated as text.
//
//	ffmpeg -i recording.wav -f wav - | agent ask "transcribe this"
func TestAskWithStdinAudioBytes(t *testing.T) {
	const fakeResponse = "Transcription here."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(bytes.NewReader(wavHeaderBytes))
	rootCmd.SetArgs([]string{"ask", "transcribe this"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// WAV bytes must be received as an AudioPart (audio/wave), not as text.
	if !rec.hasUserMessageWithAudioPart("audio/wave") {
		t.Error("inference request should contain an AudioPart (audio/wave) from WAV stdin")
	}
	if !rec.containsSystemPrompt("transcribe this") {
		t.Error("inference request should contain the arg prompt \"transcribe this\"")
	}
}

// TestAskWithStdinImageBytes validates that piped PNG image bytes are detected and delivered
// as an ImagePart to the inferencer.
//
//	ffmpeg -i photo.png -f image2 - | agent ask "describe this image"
func TestAskWithStdinImageBytes(t *testing.T) {
	const fakeResponse = "It looks like a landscape."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(bytes.NewReader(pngHeaderBytes))
	rootCmd.SetArgs([]string{"ask", "describe this image"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// PNG bytes must be received as an ImagePart (image/png), not as text.
	if !rec.hasUserMessageWithImagePart("image/png") {
		t.Error("inference request should contain an ImagePart (image/png) from PNG stdin")
	}
	if !rec.containsSystemPrompt("describe this image") {
		t.Error("inference request should contain the arg prompt \"describe this image\"")
	}
}

// TestAskWithStdinUnknownBinaryBytes validates that unrecognized binary data piped via stdin
// is wrapped in a FilePart (Name: "stdin") rather than being dropped or causing an error.
// This covers formats that http.DetectContentType cannot identify (e.g. raw MP4, WebM, OGG).
func TestAskWithStdinUnknownBinaryBytes(t *testing.T) {
	// Null bytes are unambiguously binary and won't match any known media signature.
	unknownBinary := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}

	const fakeResponse = "Got your file."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(bytes.NewReader(unknownBinary))
	rootCmd.SetArgs([]string{"ask", "what is this?"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// Unknown binary must land as a FilePart named "stdin".
	if !rec.hasUserMessageWithFilePart("stdin") {
		t.Error("inference request should contain a FilePart named \"stdin\" for unrecognized binary input")
	}
}
