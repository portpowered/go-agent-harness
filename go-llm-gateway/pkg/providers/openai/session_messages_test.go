package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
