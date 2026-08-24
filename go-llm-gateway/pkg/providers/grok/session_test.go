package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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
	// The message is malformed (no type field).
	conn.addServerMessage([]byte(`{"data": "no type"}`))
	// A valid event afterwards must never be delivered: the malformed frame is
	// a protocol violation that terminally fails the session.
	conn.addServerEvent("response.done", nil)

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeError {
		t.Fatalf("expected %q, got %q", messages.StreamTypeError, got.Type)
	}
	errValue, ok := got.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("expected *messages.ErrorValue, got %T", got.Value)
	}
	if errValue.Classification != providers.ErrorClassInvalidRequest {
		t.Errorf("classification = %q, want %q", errValue.Classification, providers.ErrorClassInvalidRequest)
	}
	if errValue.TerminalProvenance != messages.TerminalProvenanceGateway {
		t.Errorf("terminal_provenance = %q, want %q", errValue.TerminalProvenance, messages.TerminalProvenanceGateway)
	}

	select {
	case <-session.Done():
		// Session terminated after the malformed frame.
	case <-time.After(2 * time.Second):
		t.Fatal("session was not terminated after malformed server frame")
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

func TestSession_SessionClosedEmitsTerminalMetadata(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.closed", map[string]any{
		"session_id": "sess-xyz",
		"reason":     "fixture_complete",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive SESSION.CLOSE within 1 second")
	}
	if got.Type != messages.StreamTypeSessionClose {
		t.Fatalf("type: got %q, want %q", got.Type, messages.StreamTypeSessionClose)
	}
	v, ok := got.Value.(*messages.SessionCloseValue)
	if !ok || v == nil {
		t.Fatal("expected SessionCloseValue")
	}
	if v.SessionID != "sess-xyz" || v.Reason != "fixture_complete" {
		t.Fatalf("session close value: got %#v", v)
	}
	if v.Classification != providers.ErrorClassTransport ||
		v.TerminalReason != messages.TerminalReasonProviderClose ||
		v.TerminalProvenance != messages.TerminalProvenanceProvider ||
		v.OutputState != messages.TerminalOutputNotApplicable {
		t.Fatalf("session close terminal metadata: got %#v", v)
	}
}

func TestSession_SessionErrorEmitsClassifiedFailure(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("error", map[string]any{
		"message": "bad key",
		"error":   map[string]any{"type": "invalid_api_key", "code": "invalid_api_key"},
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive ERROR within 1 second")
	}
	if got.Type != messages.StreamTypeError {
		t.Fatalf("type: got %q, want %q", got.Type, messages.StreamTypeError)
	}
	v, ok := got.Value.(*messages.ErrorValue)
	if !ok || v == nil {
		t.Fatal("expected ErrorValue")
	}
	if v.Classification != providers.ErrorClassAuthentication {
		t.Errorf("classification: got %q, want %q", v.Classification, providers.ErrorClassAuthentication)
	}
	if v.ErrorType != "invalid_api_key" || v.Code != "invalid_api_key" {
		t.Errorf("provider error identity: type=%q code=%q", v.ErrorType, v.Code)
	}
	if v.TerminalReason != messages.TerminalReasonTerminalFailure ||
		v.TerminalProvenance != messages.TerminalProvenanceProvider ||
		v.OutputState != messages.TerminalOutputNone {
		t.Fatalf("terminal metadata: got %#v", v)
	}
}

func TestSession_SendWithOutcomeLifecycle(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx := context.Background()

	// Unsupported outbound stream types fail terminally.
	outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeError})
	if outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported message status = %q, want terminal_failure", outcome.Status)
	}

	// A cancelled context reports cancellation.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	textInput := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi")}
	outcome = session.SendWithOutcome(cancelledCtx, textInput)
	if outcome.Status != messages.SessionSendCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("cancelled send = %#v, want cancelled with context.Canceled", outcome)
	}

	// A deadline-exceeded context reports timeout.
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, 0)
	defer cancelTimeout()
	outcome = session.SendWithOutcome(timeoutCtx, textInput)
	if outcome.Status != messages.SessionSendTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("timed-out send = %#v, want timed_out with DeadlineExceeded", outcome)
	}

	// After the session is closed, sends report closed.
	_ = session.Close()
	select {
	case <-session.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("session did not terminate within 1 second after Close")
	}
	outcome = session.SendWithOutcome(ctx, textInput)
	if outcome.Status != messages.SessionSendClosed {
		t.Fatalf("closed send status = %q, want closed", outcome.Status)
	}

	// A successful text-delta send maps to a wire event.
	open := newGrokSession(newMockConn(), logging.DummyLogger())
	defer func() { _ = open.Close() }()
	outcome = open.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi")})
	if outcome.Status != messages.SessionSendSucceeded {
		t.Fatalf("successful send status = %q, want succeeded", outcome.Status)
	}
}
