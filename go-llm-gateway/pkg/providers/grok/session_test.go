package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/logging"
)

// readFromSession reads one StreamMessage from the session's receive buffer,
// failing the test if no message arrives within 2 seconds.
func readFromSession(t *testing.T, ctx context.Context, s *grokSession) messages.StreamMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msg, ok := s.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("timed out waiting for session message")
	}
	return msg
}

func TestSession_SendAudioBufferAppend(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	// Send audio via Send() which translates StreamMessage → wire event.
	audioData := []byte{0x01, 0x02, 0x03}
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(audioData),
	}
	if !session.Send(ctx, msg) {
		t.Fatal("Send returned false")
	}

	// Wait for the write loop to process it.
	time.Sleep(100 * time.Millisecond)

	clientMsgs := conn.getClientMessages()
	if len(clientMsgs) == 0 {
		t.Fatal("expected client message for audio append")
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(clientMsgs[0], &wire); err != nil {
		t.Fatalf("unmarshal wire message: %v", err)
	}

	var msgType string
	if err := json.Unmarshal(wire["type"], &msgType); err != nil {
		t.Fatalf("unmarshal message type: %v", err)
	}
	if msgType != "input_audio_buffer.append" {
		t.Errorf("type: got %q, want %q", msgType, "input_audio_buffer.append")
	}

	// Verify audio is base64 encoded.
	var audio string
	if err := json.Unmarshal(wire["audio"], &audio); err != nil {
		t.Fatalf("unmarshal audio payload: %v", err)
	}
	expected := base64.StdEncoding.EncodeToString(audioData)
	if audio != expected {
		t.Errorf("audio: got %q, want %q", audio, expected)
	}
}

func TestSession_ReceiveAudioDelta(t *testing.T) {
	conn := newMockConn()
	audioBytes := []byte{0x01, 0x02, 0x03, 0x04}
	audioB64 := base64.StdEncoding.EncodeToString(audioBytes)
	conn.addServerEvent("response.audio.delta", map[string]any{
		"delta": audioB64,
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeAudioDelta {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeAudioDelta)
	}
	v, ok := got.Value.(*messages.AudioDeltaValue)
	if !ok || v == nil {
		t.Fatal("expected AudioDeltaValue")
	}
	if string(v.Content) != string(audioBytes) {
		t.Errorf("content mismatch: got %v, want %v", v.Content, audioBytes)
	}
}

func TestSession_ReceiveTranscriptDelta(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("response.audio_transcript.delta", map[string]any{
		"delta": "hello world",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeTranscriptDelta {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeTranscriptDelta)
	}
}

func TestSession_ReceiveFunctionCallDone(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("response.function_call_arguments.done", map[string]any{
		"call_id":   "call-123",
		"name":      "get_weather",
		"arguments": `{"city":"London"}`,
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeToolCallEnd {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeToolCallEnd)
	}
}

func TestSession_SendTextCreatesConversationItem(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	// Send text which should map to conversation.item.create outbound.
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello from user"),
	}
	if !session.Send(ctx, msg) {
		t.Fatal("Send returned false")
	}

	time.Sleep(100 * time.Millisecond)

	clientMsgs := conn.getClientMessages()
	if len(clientMsgs) == 0 {
		t.Fatal("expected conversation.item.create message")
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(clientMsgs[0], &wire); err != nil {
		t.Fatalf("unmarshal conversation item envelope: %v", err)
	}
	var msgType string
	if err := json.Unmarshal(wire["type"], &msgType); err != nil {
		t.Fatalf("unmarshal conversation item type: %v", err)
	}
	if msgType != "conversation.item.create" {
		t.Errorf("type: got %q, want %q", msgType, "conversation.item.create")
	}

	// Verify item contains the user text.
	var item map[string]json.RawMessage
	if err := json.Unmarshal(wire["item"], &item); err != nil {
		t.Fatalf("unmarshal conversation item payload: %v", err)
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(item["content"], &content); err != nil {
		t.Fatalf("unmarshal conversation item content: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("expected content array in conversation item")
	}
	var text string
	if err := json.Unmarshal(content[0]["text"], &text); err != nil {
		t.Fatalf("unmarshal conversation item text: %v", err)
	}
	if text != "hello from user" {
		t.Errorf("text: got %q, want %q", text, "hello from user")
	}
}

func TestSession_CloseIsIdempotent(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)

	err1 := session.Close()
	if err1 != nil {
		t.Errorf("first Close: %v", err1)
	}

	err2 := session.Close()
	if err2 != nil {
		t.Errorf("second Close: %v", err2)
	}
}

func TestSession_CloseStopsDone(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)

	_ = session.Close()

	select {
	case <-session.Done():
		// Session closed as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("Done() channel was not closed after session close")
	}
}

func TestSession_MalformedServerEvent(t *testing.T) {
	conn := newMockConn()
	// First message is malformed (no type field).
	conn.addServerMessage([]byte(`{"data": "no type"}`))
	// Second message is valid — emits a MESSAGE.END via response.done.
	conn.addServerEvent("response.done", nil)

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	// The malformed event should be skipped; we should receive the valid one.
	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeMessageEnd {
		t.Errorf("expected %q, got %q", messages.StreamTypeMessageEnd, got.Type)
	}
}

func TestSession_ContextCancellation(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithCancel(context.Background())
	session.start(ctx)

	// Cancel context — should trigger session close.
	cancel()

	select {
	case <-session.done:
		// Session closed as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("session did not close after context cancellation")
	}
}

// TestSession_SessionCreatedEmitsSessionOpen is the acceptance criterion test:
// GrokSessionProvider.ConnectSession returns a Session whose typed buffer receives
// a SESSION.OPEN event when the server sends session.created within 1 second.
func TestSession_SessionCreatedEmitsSessionOpen(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.created", map[string]any{
		"session_id": "test-session-abc",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive SESSION.OPEN within 1 second")
	}
	if got.Type != messages.StreamTypeSessionOpen {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeSessionOpen)
	}
}

// TestSession_SessionCreatedEmitsSessionCreated verifies that session.created also
// emits SESSION.CREATED with the session config (model, session_id).
func TestSession_SessionCreatedEmitsSessionCreated(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.created", map[string]any{
		"session_id": "sess-xyz",
		"model":      "grok-3-mini",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	// First event should be SESSION.OPEN.
	first, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive first event within 1 second")
	}
	if first.Type != messages.StreamTypeSessionOpen {
		t.Errorf("first event type: got %q, want %q", first.Type, messages.StreamTypeSessionOpen)
	}

	// Second event should be SESSION.CREATED with session config.
	second, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive SESSION.CREATED within 1 second")
	}
	if second.Type != messages.StreamTypeSessionCreated {
		t.Errorf("second event type: got %q, want %q", second.Type, messages.StreamTypeSessionCreated)
	}
	v, ok := second.Value.(*messages.SessionCreatedValue)
	if !ok || v == nil {
		t.Fatal("expected SessionCreatedValue")
	}
	if v.SessionID != "sess-xyz" {
		t.Errorf("session_id: got %q, want %q", v.SessionID, "sess-xyz")
	}
	if v.Model != "grok-3-mini" {
		t.Errorf("model: got %q, want %q", v.Model, "grok-3-mini")
	}
}

// TestSession_SessionUpdatedEmitsSessionUpdated verifies that session.updated
// produces a SESSION.UPDATED StreamMessage.
func TestSession_SessionUpdatedEmitsSessionUpdated(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.updated", map[string]any{
		"session_id": "sess-xyz",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive SESSION.UPDATED within 1 second")
	}
	if got.Type != messages.StreamTypeSessionUpdated {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeSessionUpdated)
	}
	v, ok := got.Value.(*messages.SessionUpdatedValue)
	if !ok || v == nil {
		t.Fatal("expected SessionUpdatedValue")
	}
	if v.SessionID != "sess-xyz" {
		t.Errorf("session_id: got %q, want %q", v.SessionID, "sess-xyz")
	}
}
