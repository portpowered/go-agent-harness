package integration

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/agent-cli/internal/wire"
)

// wavHeaderBytes is a minimal WAV file header recognized by http.DetectContentType as "audio/wave".
// Bytes 0-3: "RIFF", bytes 4-7: size placeholder (zeroes), bytes 8-11: "WAVE".
var wavHeaderBytes = []byte("RIFF\x00\x00\x00\x00WAVE")

// pngHeaderBytes is the 8-byte PNG file signature recognized by http.DetectContentType as "image/png".
var pngHeaderBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// TestAskWithStdinPrompt validates that piped stdin text is used as the prompt
// when no args are provided:
//
//	echo "what is two plus two?" | agent ask
func TestAskWithStdinPrompt(t *testing.T) {
	const stdinText = "what is two plus two?"
	const fakeResponse = "2+2 = 4"

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
	rootCmd.SetIn(strings.NewReader(stdinText))
	rootCmd.SetArgs([]string{"ask"}) // no prompt arg — stdin provides it

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	actual := strings.TrimSpace(testWriter.StdoutString())
	if actual != fakeResponse {
		t.Errorf("output: got %q, want %q", actual, fakeResponse)
	}
}

// TestAskWithStdinAndArgPrompt validates that when both stdin and an arg prompt
// are provided the stdin text is used as context and the arg is the instruction,
// joined as "<stdin>\n\n<arg>":
//
//	echo "some context" | agent ask "summarize this"
func TestAskWithStdinAndArgPrompt(t *testing.T) {
	const stdinText = "some context"
	const argPrompt = "summarize this"
	const fakeResponse = "Here is the summary."

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
	rootCmd.SetIn(strings.NewReader(stdinText))
	rootCmd.SetArgs([]string{"ask", argPrompt})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// The inferencer must receive the combined prompt: stdin + "\n\n" + arg.
	combined := stdinText + "\n\n" + argPrompt
	if !rec.containsSystemPrompt(stdinText) {
		t.Errorf("inference request missing stdin context %q", stdinText)
	}
	if !rec.containsSystemPrompt(argPrompt) {
		t.Errorf("inference request missing arg prompt %q", argPrompt)
	}
	if !rec.containsSystemPrompt(combined) {
		t.Errorf("inference request should contain combined prompt %q", combined)
	}
}

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

// TestAskStdinStreamedInput validates that chunked/streamed stdin data is fully consumed
// and consolidated into a single input object before the agent runs.
//
// This simulates a streaming source (e.g. a subprocess pipe) that delivers data
// in multiple writes rather than as one contiguous block.
func TestAskStdinStreamedInput(t *testing.T) {
	const fakeResponse = "Got it all."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	// io.Pipe lets a goroutine stream chunks while the command reads them.
	pr, pw := io.Pipe()
	chunks := []string{"line one\n", "line two\n", "line three"}
	go func() {
		for _, chunk := range chunks {
			if _, err := pw.Write([]byte(chunk)); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(pr) // SetIn triggers isStdinPiped → service reads until EOF
	rootCmd.SetArgs([]string{"ask"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// All chunks must arrive as one consolidated prompt (trailing newline trimmed).
	expectedPrompt := "line one\nline two\nline three"
	if !rec.containsSystemPrompt(expectedPrompt) {
		t.Errorf("inference should receive consolidated prompt %q", expectedPrompt)
	}

	actual := strings.TrimSpace(testWriter.StdoutString())
	if actual != fakeResponse {
		t.Errorf("output: got %q, want %q", actual, fakeResponse)
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

// TestAskWithStdinBinaryOnlyNoPrompt validates that binary-only stdin (no arg prompt)
// is accepted without error; the binary data alone is sufficient input.
func TestAskWithStdinBinaryOnlyNoPrompt(t *testing.T) {
	const fakeResponse = "Heard you."

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
	rootCmd.SetArgs([]string{"ask"}) // no prompt arg — binary stdin is the only input

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask with binary-only stdin: %v", err)
	}

	if !rec.hasUserMessageWithAudioPart("audio/wave") {
		t.Error("inference request should contain AudioPart when only binary stdin is provided")
	}
}

// TestAskStdinStreamedAudio validates that WAV audio bytes delivered in multiple chunks
// via a pipe are fully consumed and consolidated into a single AudioPart before inference.
//
// This simulates piping the output of a streaming audio encoder into agent ask.
func TestAskStdinStreamedAudio(t *testing.T) {
	const fakeResponse = "Audio received."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	// Split the WAV header into three chunks to simulate a streaming source.
	pr, pw := io.Pipe()
	audioData := append(wavHeaderBytes, make([]byte, 32)...) // header + trailing zero payload
	chunks := [][]byte{audioData[:4], audioData[4:8], audioData[8:]}
	go func() {
		for _, chunk := range chunks {
			if _, err := pw.Write(chunk); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(pr)
	rootCmd.SetArgs([]string{"ask", "transcribe"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// All chunks must be consolidated: the inferencer receives a single AudioPart.
	// Detection as audio/wave requires the full RIFF+WAVE header — if only partial data
	// arrived, http.DetectContentType would return application/octet-stream instead.
	if !rec.hasUserMessageWithAudioPart("audio/wave") {
		t.Error("inference request should contain a consolidated AudioPart from streamed WAV chunks")
	}
}

// TestAskStdinStreamedImage validates that PNG image bytes delivered in multiple chunks
// via a pipe are fully consumed and consolidated into a single ImagePart before inference.
func TestAskStdinStreamedImage(t *testing.T) {
	const fakeResponse = "Image received."

	rec := &recordingInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	// Build a larger PNG-like payload and split into chunks.
	imageData := make([]byte, 64)
	copy(imageData, pngHeaderBytes) // first 8 bytes are PNG signature; rest is payload
	pr, pw := io.Pipe()
	chunks := [][]byte{imageData[:4], imageData[4:16], imageData[16:]}
	go func() {
		for _, chunk := range chunks {
			if _, err := pw.Write(chunk); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(pr)
	rootCmd.SetArgs([]string{"ask", "describe"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	// All chunks consolidated: inferencer receives a single ImagePart.
	if !rec.hasUserMessageWithImagePart("image/png") {
		t.Error("inference request should contain a consolidated ImagePart from streamed PNG chunks")
	}
}
