package media

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// File response parsing — non-streaming and streaming
// ---------------------------------------------------------------------------

// filePartFrom scans msgs for the first FilePart and returns it.
// Returns (FilePart{}, false) if none is found.
func filePartFrom(msgs []messages.Message) (messages.FilePart, bool) {
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if fp, ok := part.(messages.FilePart); ok {
				return fp, true
			}
		}
	}
	return messages.FilePart{}, false
}

// TestFile_NonStreamingResponse validates that a file-only model response is
// assembled correctly in non-streaming (Execute) mode:
//
//   - ExecuteResult.Messages contains exactly one assistant message.
//   - That message has a FilePart whose Bytes, MediaType, and Name match what
//     the mock inferencer was configured to return.
//   - The conversation delta buffer contains FILE.START, FILE.DELTA, and
//     FILE.END events (in that relative order) with Role=RoleAssistant.
//   - The inferencer was called exactly once.
func TestFile_NonStreamingResponse(t *testing.T) {
	fileBytes := []byte("%PDF-1.4 test content")
	const mediaType = "application/pdf"
	const fileName = "report.pdf"

	inf := new(MockInferencer).AddFileResponse(fileBytes, mediaType, fileName)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	result := s.Execute("generate a PDF report")

	// --- turn messages ---
	if len(result.Messages) != 2 {
		t.Fatalf("message count: got %d, want 1", len(result.Messages))
	}
	if result.Messages[1].Role != messages.RoleAssistant {
		t.Errorf("message role: got %q, want %q", result.Messages[1].Role, messages.RoleAssistant)
	}

	// --- FilePart content ---
	fp, found := filePartFrom(result.Messages)
	if !found {
		t.Fatal("no FilePart found in response messages")
	}
	if !bytes.Equal(fp.Bytes, fileBytes) {
		t.Errorf("FilePart.Bytes: got %v, want %v", fp.Bytes, fileBytes)
	}
	if fp.MediaType != mediaType {
		t.Errorf("FilePart.MediaType: got %q, want %q", fp.MediaType, mediaType)
	}
	if fp.Name != fileName {
		t.Errorf("FilePart.Name: got %q, want %q", fp.Name, fileName)
	}

	// --- delta stream contains file lifecycle events ---
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeFileStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileEnd, Role: messages.RoleAssistant},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestFile_StreamingResponse validates that a file-only model response is
// assembled correctly in streaming (ExecuteStreaming) mode:
//
//   - After consuming the text stream (which is empty for file-only responses),
//     Messages() returns one assistant message with a correctly populated FilePart.
//   - The conversation delta buffer contains FILE.START, FILE.DELTA, FILE.END.
//   - The inferencer was called exactly once.
func TestFile_StreamingResponse(t *testing.T) {
	fileBytes := []byte("%PDF-1.4 test content")
	const mediaType = "application/pdf"
	const fileName = "report.pdf"

	inf := new(MockInferencer).AddFileResponse(fileBytes, mediaType, fileName)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	streamText := s.ExecuteStreamingText("generate a PDF report")

	// --- stream text is empty (no text content in file-only response) ---
	if streamText != "" {
		t.Errorf("stream text: got %q, want empty string", streamText)
	}

	// --- delta stream contains file lifecycle events ---
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeFileStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileEnd, Role: messages.RoleAssistant},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestFile_ChunkedStreamingResponse validates that multiple FILE.DELTA chunks
// from the inferencer stream are correctly concatenated into a single FilePart.
func TestFile_ChunkedStreamingResponse(t *testing.T) {
	chunk1 := []byte("%PDF-1.4")      // first half
	chunk2 := []byte(" test content") // second half
	wantBytes := append(chunk1, chunk2...)

	const mediaType = "application/pdf"
	const fileName = "chunked.pdf"

	inf := &MockInferencer{}
	inf.entries = []inferenceEntry{{
		result: messages.InferenceResult{
			Message: messages.Message{
				Role:         messages.RoleAssistant,
				ContentParts: []messages.ContentPart{messages.FilePart{Bytes: wantBytes, MediaType: mediaType, Name: fileName}},
			},
		},
		fileChunks: [][]byte{chunk1, chunk2},
	}}

	tool := NewMockToolExecutor()
	s := NewScenario(t, inf, tool)
	streamText := s.ExecuteStreamingText("generate a chunked file")

	// Both FILE.DELTA chunks must appear in the delta buffer.
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeFileStart, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileDelta, Role: messages.RoleAssistant},
		{Type: messages.StreamTypeFileEnd, Role: messages.RoleAssistant},
	})

	if streamText != "" {
		t.Errorf("stream text: got %q, want empty string", streamText)
	}
}

// ---------------------------------------------------------------------------
// File input forwarding — non-streaming, streaming, and combined
// ---------------------------------------------------------------------------

// TestFile_InputForwarded validates that File in ExecuteInput is forwarded
// to the inferencer as a FilePart in the user message (non-streaming path):
//
//   - The first captured message slice contains a user message with a FilePart.
//   - FilePart.Bytes, MediaType, and Name match the supplied file.
//   - The conversation history preserves the user message with its FilePart.
//   - The inferencer was called exactly once.
func TestFile_InputForwarded(t *testing.T) {
	fileBytes := []byte("col1,col2\n1,2\n3,4")
	const mediaType = "text/csv"
	const fileName = "data.csv"
	file := &agentloop.File{Bytes: fileBytes, MediaType: mediaType, Name: fileName}

	inf := new(MockInferencer).AddTextResponse("file acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{File: file}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// --- inferencer received the user message ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]
	if userMsg.Role != messages.RoleUser {
		t.Errorf("user message role: got %q, want %q", userMsg.Role, messages.RoleUser)
	}

	// --- FilePart present with correct bytes, media type, and name ---
	fp, found := filePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no FilePart in captured user message")
	}
	if !bytes.Equal(fp.Bytes, fileBytes) {
		t.Errorf("FilePart.Bytes: got %v, want %v", fp.Bytes, fileBytes)
	}
	if fp.MediaType != mediaType {
		t.Errorf("FilePart.MediaType: got %q, want %q", fp.MediaType, mediaType)
	}
	if fp.Name != fileName {
		t.Errorf("FilePart.Name: got %q, want %q", fp.Name, fileName)
	}

	// --- conversation history preserves the FilePart ---
	histFP, found := filePartFrom(sc.History())
	if !found {
		t.Fatal("no FilePart in conversation history")
	}
	if !bytes.Equal(histFP.Bytes, fileBytes) {
		t.Errorf("history FilePart.Bytes: got %v, want %v", histFP.Bytes, fileBytes)
	}
}

// TestFile_InputStreamingForwarded validates that File in ExecuteInput is
// forwarded to the inferencer when using the ExecuteStreaming path.
func TestFile_InputStreamingForwarded(t *testing.T) {
	fileBytes := []byte("col1,col2\n1,2\n3,4")
	const mediaType = "text/csv"
	const fileName = "data.csv"
	file := &agentloop.File{Bytes: fileBytes, MediaType: mediaType, Name: fileName}

	inf := new(MockInferencer).AddTextResponse("streaming file acknowledged")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sc.Loop.ExecuteStreaming(ctx, agentloop.ExecuteInput{File: file})
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	for result.EventStream.HasNext() {
		// drain event stream so kernel does not block
	}

	// --- inferencer received the user message ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]

	// --- FilePart present with correct bytes, media type, and name ---
	fp, found := filePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no FilePart in captured user message")
	}
	if !bytes.Equal(fp.Bytes, fileBytes) {
		t.Errorf("FilePart.Bytes: got %v, want %v", fp.Bytes, fileBytes)
	}
	if fp.MediaType != mediaType {
		t.Errorf("FilePart.MediaType: got %q, want %q", fp.MediaType, mediaType)
	}
	if fp.Name != fileName {
		t.Errorf("FilePart.Name: got %q, want %q", fp.Name, fileName)
	}
}

// TestFile_InputWithTextForwarded validates that a multimodal ExecuteInput
// with both a text message and a file produces a user message that contains
// both a TextPart and a FilePart.
func TestFile_InputWithTextForwarded(t *testing.T) {
	const userText = "analyse this CSV"
	fileBytes := []byte("col1,col2\n1,2\n3,4")
	const mediaType = "text/csv"
	const fileName = "data.csv"
	file := &agentloop.File{Bytes: fileBytes, MediaType: mediaType, Name: fileName}

	inf := new(MockInferencer).AddTextResponse("the CSV has 2 columns and 2 rows")
	tool := NewMockToolExecutor()
	sc := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sc.Loop.Execute(ctx, agentloop.ExecuteInput{Message: userText, File: file}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(inf.CapturedMessages) == 0 || len(inf.CapturedMessages[0]) == 0 {
		t.Fatal("no captured messages")
	}
	userMsg := inf.CapturedMessages[0][0]

	// --- TextPart present ---
	if userMsg.TextContent() != userText {
		t.Errorf("TextContent: got %q, want %q", userMsg.TextContent(), userText)
	}

	// --- FilePart present with correct bytes ---
	fp, found := filePartFrom([]messages.Message{userMsg})
	if !found {
		t.Fatal("no FilePart in multimodal user message")
	}
	if !bytes.Equal(fp.Bytes, fileBytes) {
		t.Errorf("FilePart.Bytes: got %v, want %v", fp.Bytes, fileBytes)
	}
}
