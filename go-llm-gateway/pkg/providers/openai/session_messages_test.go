package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

func newWireSeamSession(t *testing.T) (*realtimeSession, *mockWebSocketConn) {
	t.Helper()
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nopLogger())
	session.start(context.Background())
	t.Cleanup(func() { _ = session.Close() })
	return session, conn
}

func TestRealtimeSessionSendMessage_WireOrderAndFidelity(t *testing.T) {
	session, conn := newWireSeamSession(t)
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x01}
	jpegBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 'J', 'F', 'I', 'F'}

	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: "describe these"},
			messages.ImagePart{Bytes: pngBytes, MediaType: "image/png"},
			messages.ImagePart{Bytes: jpegBytes, MediaType: "image/jpeg"},
		},
	}
	if !session.SendMessage(context.Background(), msg) {
		t.Fatal("SendMessage returned false for a complete image turn")
	}

	var written [][]byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		written = conn.getClientMessages()
		if len(written) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(written) != 2 {
		t.Fatalf("wire events = %d, want conversation.item.create then response.create", len(written))
	}
	var item struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL string `json:"image_url,omitempty"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[0], &item); err != nil {
		t.Fatalf("unmarshal conversation.item.create: %v", err)
	}
	if item.Type != "conversation.item.create" || item.Item.Type != "message" || item.Item.Role != "user" {
		t.Fatalf("first event = %#v, want user message item create", item)
	}
	wantParts := []struct {
		partType string
		text     string
		raw      []byte
		mime     string
	}{
		{"input_text", "describe these", nil, ""},
		{"input_image", "", pngBytes, "image/png"},
		{"input_image", "", jpegBytes, "image/jpeg"},
	}
	if len(item.Item.Content) != len(wantParts) {
		t.Fatalf("content parts = %d, want %d in order text,png,jpeg", len(item.Item.Content), len(wantParts))
	}
	for i, want := range wantParts {
		got := item.Item.Content[i]
		if got.Type != want.partType {
			t.Fatalf("part %d type = %q, want %q", i, got.Type, want.partType)
		}
		if want.partType == "input_text" {
			if got.Text != want.text {
				t.Fatalf("part %d text = %q, want %q", i, got.Text, want.text)
			}
			continue
		}
		dataURL := "data:" + want.mime + ";base64," + base64.StdEncoding.EncodeToString(want.raw)
		if got.ImageURL != dataURL {
			t.Fatalf("part %d image_url mismatch: got %d chars, want exact data URL with MIME %s and %d bytes", i, len(got.ImageURL), want.mime, len(want.raw))
		}
	}
	var response struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(written[1], &response); err != nil {
		t.Fatalf("unmarshal response.create: %v", err)
	}
	if response.Type != "response.create" {
		t.Fatalf("second event = %q, want response.create", response.Type)
	}
}

func TestRealtimeSessionSendMessageWithoutResponse_QueuesOnlyMessageItem(t *testing.T) {
	session, conn := newWireSeamSession(t)
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: "describe this image after my question"},
			messages.ImagePart{Bytes: []byte{0x89, 'P', 'N', 'G'}, MediaType: "image/png"},
		},
	}
	if !session.SendMessageWithoutResponse(context.Background(), msg) {
		t.Fatal("SendMessageWithoutResponse returned false for a complete image turn")
	}

	var written [][]byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		written = conn.getClientMessages()
		if len(written) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(written) != 1 {
		t.Fatalf("wire events = %d, want only conversation.item.create", len(written))
	}
	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(written[0], &item); err != nil {
		t.Fatalf("unmarshal conversation.item.create: %v", err)
	}
	if item.Type != "conversation.item.create" {
		t.Fatalf("queued event type = %q, want conversation.item.create", item.Type)
	}
}

func TestRealtimeSessionSendMessage_ToolImagePreservesCallAndImageOrder(t *testing.T) {
	session, conn := newWireSeamSession(t)
	// Use a multi-megabyte logical image so the wire assertion proves that
	// only the typed projection, rather than the function output, grows with
	// the prepared image.
	imageBytes := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 512*1024)
	msg := messages.Message{
		Role:       messages.RoleTool,
		ToolCallID: "call-read-image",
		ContentParts: []messages.ContentPart{
			messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
		},
	}

	if !session.SendMessage(context.Background(), msg) {
		t.Fatal("SendMessage returned false for a rich tool result")
	}

	written := waitForWireMessages(t, conn, 3)
	var functionOutput struct {
		Type string `json:"type"`
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[0], &functionOutput); err != nil {
		t.Fatalf("unmarshal function_call_output: %v", err)
	}
	if functionOutput.Type != "conversation.item.create" || functionOutput.Item.Type != "function_call_output" || functionOutput.Item.CallID != msg.ToolCallID {
		t.Fatalf("function output event = %#v, want correlated function_call_output", functionOutput)
	}
	if functionOutput.Item.Output == "" {
		t.Fatal("function_call_output output is empty for an image result")
	}
	var result struct {
		Version         int    `json:"version"`
		Status          string `json:"status"`
		MIMEType        string `json:"mime_type"`
		ByteLength      int    `json:"byte_length"`
		SHA256          string `json:"sha256"`
		TypedProjection string `json:"typed_projection"`
	}
	if err := json.Unmarshal([]byte(functionOutput.Item.Output), &result); err != nil {
		t.Fatalf("decode image result envelope: %v", err)
	}
	digest := sha256.Sum256(imageBytes)
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	if len(functionOutput.Item.Output) > realtimeImageEnvelopeMaxBytes || strings.Contains(strings.ToLower(functionOutput.Item.Output), "data:") || strings.Contains(strings.ToLower(functionOutput.Item.Output), "base64") {
		t.Fatalf("image result envelope is not compact metadata: bytes=%d output=%q", len(functionOutput.Item.Output), functionOutput.Item.Output)
	}
	if result.Version != realtimeImageResultVersion || result.Status != realtimeImageResultStatusSuccess || result.MIMEType != "image/png" || result.ByteLength != len(imageBytes) || result.SHA256 != hex.EncodeToString(digest[:]) || result.TypedProjection != realtimeImageTypedProjection {
		t.Fatalf("image result envelope = %#v, want compact metadata for %d bytes", result, len(imageBytes))
	}

	var imageItem struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			ID      string `json:"id"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[1], &imageItem); err != nil {
		t.Fatalf("unmarshal tool image item: %v", err)
	}
	if imageItem.Type != "conversation.item.create" || imageItem.Item.Type != "message" || imageItem.Item.Role != string(messages.RoleUser) || len(imageItem.Item.Content) != 1 {
		t.Fatalf("tool image event = %#v, want one user image message", imageItem)
	}
	if imageItem.Item.ID != realtimeToolImageItemID(msg.ToolCallID) {
		t.Fatalf("tool image item ID = %q, want correlation ID %q", imageItem.Item.ID, realtimeToolImageItemID(msg.ToolCallID))
	}
	if imageItem.Item.Content[0].Type != "input_image" || imageItem.Item.Content[0].ImageURL != wantURL {
		t.Fatalf("tool image content = %#v, want exact PNG data URL", imageItem.Item.Content[0])
	}

	var response struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(written[2], &response); err != nil {
		t.Fatalf("unmarshal response.create: %v", err)
	}
	if response.Type != "response.create" {
		t.Fatalf("third event type = %q, want response.create", response.Type)
	}
	encodedImage := base64.StdEncoding.EncodeToString(imageBytes)
	if got := strings.Count(string(bytes.Join(written, nil)), encodedImage); got != 1 {
		t.Fatalf("encoded image payload occurs %d times across the provider transaction, want exactly once", got)
	}
}

func TestRealtimeSessionSendMessage_RejectsInconsistentToolImageBeforeWriting(t *testing.T) {
	imageBytes := []byte("prepared image bytes")
	digest := sha256.Sum256(imageBytes)
	valid := realtimeImageResultEnvelope{
		Version:         realtimeImageResultVersion,
		Status:          realtimeImageResultStatusSuccess,
		MIMEType:        "image/png",
		ByteLength:      len(imageBytes),
		SHA256:          hex.EncodeToString(digest[:]),
		TypedProjection: realtimeImageTypedProjection,
	}
	// Keep the test data readable while still deriving the valid baseline from
	// the same fields the provider validates.
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid image envelope: %v", err)
	}
	var validMap map[string]any
	if err := json.Unmarshal(validJSON, &validMap); err != nil {
		t.Fatalf("decode valid image envelope: %v", err)
	}

	cases := map[string]func(map[string]any, []messages.ContentPart) ([]messages.ContentPart, string){
		"wrong version": func(envelope map[string]any, parts []messages.ContentPart) ([]messages.ContentPart, string) {
			envelope["version"] = float64(realtimeImageResultVersion - 1)
			return parts, "version"
		},
		"wrong byte length": func(envelope map[string]any, parts []messages.ContentPart) ([]messages.ContentPart, string) {
			envelope["byte_length"] = float64(len(imageBytes) + 1)
			return parts, "byte length"
		},
		"wrong digest": func(envelope map[string]any, parts []messages.ContentPart) ([]messages.ContentPart, string) {
			envelope["sha256"] = strings.Repeat("0", sha256.Size*2)
			return parts, "digest"
		},
		"unknown image data field": func(envelope map[string]any, parts []messages.ContentPart) ([]messages.ContentPart, string) {
			envelope["data_url"] = "data:image/png;base64,not-inline-pixels"
			return parts, "data URL"
		},
		"duplicate typed images": func(envelope map[string]any, parts []messages.ContentPart) ([]messages.ContentPart, string) {
			return append(parts, messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"}), "duplicate image"
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := make(map[string]any, len(validMap))
			for key, value := range validMap {
				envelope[key] = value
			}
			parts, want := mutate(envelope, []messages.ContentPart{
				messages.TextPart{Text: mustMarshalJSON(t, envelope)},
				messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
			})
			// The mutation callback receives the baseline text part before the
			// mutation is serialized; replace it with the final envelope now.
			parts[0] = messages.TextPart{Text: mustMarshalJSON(t, envelope)}
			session, conn := newWireSeamSession(t)
			if session.SendMessage(context.Background(), messages.Message{
				Role:         messages.RoleTool,
				ToolCallID:   "call-inconsistent-image",
				ContentParts: parts,
			}) {
				t.Fatalf("inconsistent %s result was accepted", want)
			}
			if written := conn.getClientMessages(); len(written) != 0 {
				t.Fatalf("inconsistent %s result wrote %d provider events before rejection", want, len(written))
			}
		})
	}
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(data)
}

func TestRealtimeSessionSendMessage_EmptyToolResultPreservesCorrelation(t *testing.T) {
	session, conn := newWireSeamSession(t)
	msg := messages.Message{Role: messages.RoleTool, ToolCallID: "call-empty"}

	if !session.SendMessage(context.Background(), msg) {
		t.Fatal("SendMessage returned false for a valid empty tool result")
	}
	written := waitForWireMessages(t, conn, 2)

	var output struct {
		Type string `json:"type"`
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[0], &output); err != nil {
		t.Fatalf("unmarshal empty function_call_output: %v", err)
	}
	if output.Type != "conversation.item.create" || output.Item.Type != "function_call_output" || output.Item.CallID != msg.ToolCallID || output.Item.Output != "" {
		t.Fatalf("empty function_call_output = %#v, want correlated empty output", output)
	}

	var response struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(written[1], &response); err != nil {
		t.Fatalf("unmarshal empty-result response.create: %v", err)
	}
	if response.Type != "response.create" {
		t.Fatalf("empty-result second event = %q, want response.create", response.Type)
	}
}

func TestRealtimeSessionSendMessage_ToolImageCorrelationIDIsBoundedForOpaqueCallID(t *testing.T) {
	session, conn := newWireSeamSession(t)
	toolCallID := "call/opaque id?" + strings.Repeat("/:? with spaces", 512)
	msg := messages.Message{
		Role:       messages.RoleTool,
		ToolCallID: toolCallID,
		ContentParts: []messages.ContentPart{
			messages.ImagePart{Bytes: []byte{0x89, 'P', 'N', 'G'}, MediaType: "image/png"},
		},
	}

	if !session.SendMessage(context.Background(), msg) {
		t.Fatal("SendMessage returned false for an opaque long tool call ID")
	}
	written := waitForWireMessages(t, conn, 3)

	var functionOutput struct {
		Item struct {
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[0], &functionOutput); err != nil {
		t.Fatalf("unmarshal function_call_output: %v", err)
	}
	if functionOutput.Item.CallID != toolCallID {
		t.Fatalf("function_call_output call ID = %q, want opaque source ID preserved", functionOutput.Item.CallID)
	}
	if functionOutput.Item.Output == "" {
		t.Fatal("function_call_output output is empty for an image result")
	}

	var imageItem struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(written[1], &imageItem); err != nil {
		t.Fatalf("unmarshal correlated image item: %v", err)
	}
	wantDigest := sha256.Sum256([]byte(toolCallID))
	wantID := realtimeToolImageItemID(toolCallID)
	if imageItem.Item.ID != wantID {
		t.Fatalf("correlated image item ID = %q, want deterministic ID %q", imageItem.Item.ID, wantID)
	}
	if len(wantID) > 32 {
		t.Fatalf("correlated image item ID length = %d, want at most 32 characters", len(wantID))
	}
	if !strings.HasPrefix(wantID, realtimeToolImageItemIDPrefix) {
		t.Fatalf("correlated image item ID = %q, want prefix %q", wantID, realtimeToolImageItemIDPrefix)
	}
	encodedDigest := strings.TrimPrefix(wantID, realtimeToolImageItemIDPrefix)
	decodedDigest, err := base64.RawURLEncoding.DecodeString(encodedDigest)
	if err != nil {
		t.Fatalf("decode correlated image item digest: %v", err)
	}
	if !bytes.Equal(decodedDigest, wantDigest[:realtimeToolImageItemIDDigestBytes]) {
		t.Fatalf("correlated image item digest does not match the first %d SHA-256 bytes", realtimeToolImageItemIDDigestBytes)
	}
	if strings.ContainsAny(wantID, "+/=/:? ") {
		t.Fatalf("correlated image item ID contains provider-unsafe characters: %q", wantID)
	}
}

func TestRealtimeToolImageItemID_IsDeterministicURLSafeAndBounded(t *testing.T) {
	cases := []struct {
		name   string
		callID string
	}{
		{name: "typical real call ID", callID: "call_read_image_12345"},
		{name: "empty call ID", callID: ""},
		{name: "long call ID", callID: strings.Repeat("opaque-call-id/", 16)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "long call ID" && len(tc.callID) <= 200 {
				t.Fatalf("test call ID length = %d, want more than 200 characters", len(tc.callID))
			}
			got := realtimeToolImageItemID(tc.callID)
			if len(got) > 32 {
				t.Fatalf("correlation ID length = %d, want at most 32 characters", len(got))
			}
			if !strings.HasPrefix(got, realtimeToolImageItemIDPrefix) {
				t.Fatalf("correlation ID = %q, want prefix %q", got, realtimeToolImageItemIDPrefix)
			}
			if got != realtimeToolImageItemID(tc.callID) {
				t.Fatalf("correlation ID is not deterministic: first %q, second %q", got, realtimeToolImageItemID(tc.callID))
			}
			if strings.ContainsAny(got, "+/=/:? ") {
				t.Fatalf("correlation ID contains provider-unsafe characters: %q", got)
			}

			wantDigest := sha256.Sum256([]byte(tc.callID))
			encodedDigest := strings.TrimPrefix(got, realtimeToolImageItemIDPrefix)
			decodedDigest, err := base64.RawURLEncoding.DecodeString(encodedDigest)
			if err != nil {
				t.Fatalf("decode correlation ID digest: %v", err)
			}
			if !bytes.Equal(decodedDigest, wantDigest[:realtimeToolImageItemIDDigestBytes]) {
				t.Fatalf("decoded digest = %x, want first %d SHA-256 bytes %x", decodedDigest, realtimeToolImageItemIDDigestBytes, wantDigest[:realtimeToolImageItemIDDigestBytes])
			}
		})
	}
}

func waitForWireMessages(t *testing.T, conn *mockWebSocketConn, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		written := conn.getClientMessages()
		if len(written) == want || time.Now().After(deadline) {
			if len(written) != want {
				t.Fatalf("wire events = %d, want %d", len(written), want)
			}
			return written
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRealtimeSessionSendMessage_RejectsIncompleteMessages(t *testing.T) {
	session, conn := newWireSeamSession(t)
	cases := map[string]messages.Message{
		"no parts":     {Role: messages.RoleUser},
		"unsupported":  {Role: messages.RoleUser, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: []byte{1}, MediaType: "audio/pcm"}}},
		"missing role": {ContentParts: []messages.ContentPart{messages.TextPart{Text: "hi"}}},
	}
	for name, msg := range cases {
		if ok := session.SendMessage(context.Background(), msg); ok {
			t.Fatalf("%s: SendMessage succeeded, want false", name)
		}
	}
	if written := conn.getClientMessages(); len(written) != 0 {
		t.Fatalf("rejected turns wrote %d wire events, want zero", len(written))
	}
}

type wireNopLogger struct{}

func (wireNopLogger) Debug(string, ...logging.Field) {}
func (wireNopLogger) Info(string, ...logging.Field)  {}
func (wireNopLogger) Warn(string, ...logging.Field)  {}
func (wireNopLogger) Error(string, ...logging.Field) {}
func (wireNopLogger) Fatal(string, ...logging.Field) {}
func (wireNopLogger) Panic(string, ...logging.Field) {}

func nopLogger() logging.Logger { return wireNopLogger{} }
