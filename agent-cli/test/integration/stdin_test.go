package integration

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// stdinWAV is a minimal go-audio encoded WAV, which http.DetectContentType
// recognizes as "audio/wave".
func stdinWAV(t *testing.T) []byte {
	t.Helper()
	var wav bytes.Buffer
	if err := wavio.Write(&wav, wavio.Rate16kHz, make([]int16, 16)); err != nil {
		t.Fatalf("encode stdin WAV: %v", err)
	}
	return wav.Bytes()
}

// pngHeaderBytes is the 8-byte PNG file signature recognized by http.DetectContentType as "image/png".
func pngHeaderBytes() []byte { return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'} }

// TestAskNoArgsNoStdin validates that running ask with neither args nor piped
// stdin returns an error instead of hanging or panicking.
func TestAskNoArgsNoStdin(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(t.Context(), exec, inf)
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

	agentCLI, err := wire.InitializeMockAgentCLI(t.Context(), exec, inf)
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
	rec := runAskWithStdinBytes(t, stdinWAV(t), "transcribe this", "Transcription here.")
	// WAV bytes must be received as an AudioPart (audio/wave), not as text.
	if !rec.hasUserMessageWithAudioPart("audio/wave") {
		t.Error("inference request should contain an AudioPart (audio/wave) from WAV stdin")
	}
}

// TestAskWithStdinImageBytes validates that piped PNG image bytes are detected and delivered
// as an ImagePart to the inferencer.
//
//	ffmpeg -i photo.png -f image2 - | agent ask "describe this image"
func TestAskWithStdinImageBytes(t *testing.T) {
	rec := runAskWithStdinBytes(t, pngHeaderBytes(), "describe this image", "It looks like a landscape.")
	// PNG bytes must be received as an ImagePart (image/png), not as text.
	if !rec.hasUserMessageWithImagePart("image/png") {
		t.Error("inference request should contain an ImagePart (image/png) from PNG stdin")
	}
}

// runAskWithStdinBytes runs `ask prompt` with stdin piped from data and
// requires the argument prompt to reach the inference request.
func runAskWithStdinBytes(t *testing.T, data []byte, prompt, response string) *recordingInferencer {
	t.Helper()
	rec := &recordingInferencer{response: response}
	agentCLI, err := wire.InitializeMockAgentCLI(t.Context(), &mockToolExecutor{}, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}
	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(bytes.NewReader(data))
	rootCmd.SetArgs([]string{"ask", prompt})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}
	if !rec.containsSystemPrompt(prompt) {
		t.Errorf("inference request should contain the arg prompt %q", prompt)
	}
	return rec
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

	agentCLI, err := wire.InitializeMockAgentCLI(t.Context(), exec, rec)
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
