package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const grokTestSafetyTimeout = 10 * time.Second

func newGrokTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grokTestSafetyTimeout)
	t.Cleanup(cancel)
	return ctx
}

// readFromSession waits for the next provider event. The deadline is only a
// diagnostic safety bound; the test succeeds when the expected event arrives.
func readFromSession(t *testing.T, ctx context.Context, s messages.Session, phase ...string) messages.StreamMessage {
	t.Helper()
	label := "session message"
	if len(phase) > 0 && phase[0] != "" {
		label = phase[0]
	}
	readContext, cancel := context.WithTimeout(ctx, grokTestSafetyTimeout)
	defer cancel()
	msg, err := s.Receive().ReadContext(readContext)
	if err != nil {
		done := false
		select {
		case <-s.Done():
			done = true
		default:
		}
		t.Fatalf("waiting for %s failed: %v; session_done=%t receive_buffer=%d", label, err, done, s.Receive().Len())
	}
	return msg
}

func waitForGrokClientMessages(t *testing.T, conn *mockWebSocketConn, want int, phase string) [][]byte {
	t.Helper()
	timer := time.NewTimer(grokTestSafetyTimeout)
	defer timer.Stop()
	for {
		messages := conn.getClientMessages()
		if len(messages) >= want {
			return messages
		}
		select {
		case <-conn.clientWriteCh:
		case <-timer.C:
			messages := conn.getClientMessages()
			t.Fatalf("timed out waiting for %s after %s: got %d client messages", phase, grokTestSafetyTimeout, len(messages))
		}
	}
}

func waitForGrokSignal(t *testing.T, signal <-chan struct{}, phase string) {
	t.Helper()
	timer := time.NewTimer(grokTestSafetyTimeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s after %s", phase, grokTestSafetyTimeout)
	}
}

func TestSession_SendAudioBufferAppend(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
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

	clientMsgs := waitForGrokClientMessages(t, conn, 1, "audio append wire event")
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

func TestSession_SendMessageEndCommitsAndRequestsResponse(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("Send returned false for MESSAGE.END")
	}

	clientMessages := waitForGrokClientMessages(t, conn, 2, "commit and response wire events")
	if len(clientMessages) != 2 {
		t.Fatalf("wire events = %d, want input_audio_buffer.commit followed by response.create", len(clientMessages))
	}
	var commit, response struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(clientMessages[0], &commit); err != nil {
		t.Fatalf("unmarshal commit event: %v", err)
	}
	if err := json.Unmarshal(clientMessages[1], &response); err != nil {
		t.Fatalf("unmarshal response event: %v", err)
	}
	if commit.Type != "input_audio_buffer.commit" || response.Type != "response.create" {
		t.Fatalf("wire event order = %q, %q; want commit then response.create", commit.Type, response.Type)
	}
}

func TestSession_SendMessageEndRequestsResponseAndCompletesTurn(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.created", map[string]any{"session_id": "grok-session", "model": "grok-device-test"})
	conn.addServerEvent("response.created", nil)
	conn.addServerEvent("response.audio.delta", map[string]any{
		"delta": base64.StdEncoding.EncodeToString([]byte{0x01, 0x02}),
	})
	conn.addServerEvent("response.audio.done", nil)
	conn.addServerEvent("response.audio_transcript.done", map[string]any{"transcript": "device round trip"})
	conn.addServerEvent("response.done", nil)

	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0x11, 0x12}),
	}) {
		t.Fatal("Send returned false for audio delta")
	}
	if !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("Send returned false for MESSAGE.END")
	}

	for {
		if message := readFromSession(t, ctx, session); message.Type == messages.StreamTypeMessageEnd {
			break
		}
	}

	clientMessages := waitForGrokClientMessages(t, conn, 3, "audio turn wire events")
	if len(clientMessages) != 3 {
		t.Fatalf("wire events = %d, want audio append, commit, and response.create", len(clientMessages))
	}
	var types []string
	for _, raw := range clientMessages {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("unmarshal client event: %v", err)
		}
		types = append(types, event.Type)
	}
	want := []string{"input_audio_buffer.append", "input_audio_buffer.commit", "response.create"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("wire event types = %#v, want %#v", types, want)
	}
}

func TestSession_SendExplicitResponseCreate(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}) {
		t.Fatal("Send explicit response request returned false")
	}

	clientMessages := waitForGrokClientMessages(t, conn, 1, "explicit response wire event")
	if len(clientMessages) != 1 {
		t.Fatalf("wire events = %d, want one response.create", len(clientMessages))
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(clientMessages[0], &event); err != nil {
		t.Fatalf("unmarshal response event: %v", err)
	}
	if event.Type != "response.create" {
		t.Fatalf("event type = %q, want response.create", event.Type)
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
	ctx := newGrokTestContext(t)
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

func TestSession_RTCMediaBridgesProviderAudioPath(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	session.mediaSampleRate = 24000
	owner, ok := any(session).(rtc.MediaSession)
	if !ok {
		t.Fatal("grok session does not expose rtc.MediaSession")
	}
	endpoints := owner.RTCMedia()
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	want := make([]int16, 720)
	for index := range want {
		want[index] = int16((index*73)%24000 - 12000) //nolint:gosec // bounded test tone
	}
	if err := endpoints.Outbound.WriteFrame(ctx, rtc.PCMFrame{Samples: want}); err != nil {
		t.Fatalf("write RTC outbound frame: %v", err)
	}

	clientMessage := waitForGrokClientMessages(t, conn, 1, "RTC outbound audio event")[0]
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(clientMessage, &wire); err != nil {
		t.Fatalf("unmarshal RTC outbound event: %v", err)
	}
	var eventType, encoded string
	if err := json.Unmarshal(wire["type"], &eventType); err != nil {
		t.Fatalf("unmarshal RTC outbound event type: %v", err)
	}
	if err := json.Unmarshal(wire["audio"], &encoded); err != nil {
		t.Fatalf("unmarshal RTC outbound audio: %v", err)
	}
	if eventType != "input_audio_buffer.append" {
		t.Fatalf("RTC outbound event type = %q, want input_audio_buffer.append", eventType)
	}
	if got, wantBytes := encoded, base64.StdEncoding.EncodeToString(encodePCM16(want)); got != wantBytes {
		t.Fatalf("RTC outbound PCM payload = %q, want %q", got, wantBytes)
	}

	conn.addServerEvent("response.audio.delta", map[string]any{
		"delta": base64.StdEncoding.EncodeToString(encodePCM16(want)),
	})
	conn.addServerEvent("response.audio.done", nil)
	got, err := endpoints.Inbound.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("read RTC inbound frame: %v", err)
	}
	if !reflect.DeepEqual(got.Samples, want) {
		t.Fatalf("RTC inbound PCM frame differs from provider audio: got %d samples", len(got.Samples))
	}
}

func TestConnectSession_PreparesRTCMediaBeforeReadLoopForConsumer(t *testing.T) {
	conn := newMockConn()
	provider := New(WithAPIKey("test-key"), WithWebSocketDialer(&mockDialer{conn: conn}))
	ctx := newGrokTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "grok-realtime", OutputAudioSampleRate: models.SampleRate24000})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	if session.(*grokSession).currentRTCMedia() == nil {
		t.Fatal("RTC media was not prepared before ConnectSession returned")
	}
}

func TestSession_ReceiveTranscriptDelta(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("response.audio_transcript.delta", map[string]any{
		"delta": "hello world",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeTranscriptDelta {
		t.Errorf("type: got %q, want %q", got.Type, messages.StreamTypeTranscriptDelta)
	}
}

func TestSession_ReceiveInputAudioTranscriptWithUserRole(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("conversation.item.input_audio_transcription.delta", map[string]any{
		"delta": "hello ",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session)
	if got.Type != messages.StreamTypeTranscriptDelta || got.Role != messages.RoleUser {
		t.Fatalf("type/role: got %q/%q, want %q/%q", got.Type, got.Role, messages.StreamTypeTranscriptDelta, messages.RoleUser)
	}
	value, ok := got.Value.(*messages.TranscriptDeltaValue)
	if !ok || value.Text != "hello " {
		t.Fatalf("input transcript delta: got %#v", got.Value)
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
	ctx := newGrokTestContext(t)
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
	ctx := newGrokTestContext(t)
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

	clientMsgs := waitForGrokClientMessages(t, conn, 1, "text conversation-item wire event")
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
	ctx := newGrokTestContext(t)
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
	ctx := newGrokTestContext(t)
	session.start(ctx)

	_ = session.Close()
	waitForGrokSignal(t, session.Done(), "session Done after Close")
}

func TestSession_WriteLoopStopsWhenClosedWithEmptyQueue(t *testing.T) {
	session := newGrokSession(newMockConn(), logging.DummyLogger())
	exited := make(chan struct{})

	go func() {
		session.writeLoop(context.Background())
		close(exited)
	}()

	if err := session.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	waitForGrokSignal(t, exited, "idle writeLoop exit after Close")
}

func TestSession_MalformedServerEvent(t *testing.T) {
	conn := newMockConn()
	// The message is malformed (no type field).
	conn.addServerMessage([]byte(`{"data": "no type"}`))
	// A valid event afterwards must never be delivered: the malformed frame is
	// a protocol violation that terminally fails the session.
	conn.addServerEvent("response.done", nil)

	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
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

	waitForGrokSignal(t, session.Done(), "session termination after malformed server frame")
}

func TestSession_ContextCancellation(t *testing.T) {
	conn := newMockConn()
	session := newGrokSession(conn, logging.DummyLogger())
	ctx, cancel := context.WithCancel(context.Background())
	session.start(ctx)

	// Cancel context — should trigger session close.
	cancel()

	waitForGrokSignal(t, session.done, "session close after context cancellation")
}

// TestSession_SessionCreatedEmitsSessionOpen is the acceptance criterion test:
// GrokSessionProvider.ConnectSession returns a Session whose typed buffer receives
// a SESSION.OPEN event when the server sends session.created. The test deadline
// is a diagnostic safety bound rather than an expected event latency.
func TestSession_SessionCreatedEmitsSessionOpen(t *testing.T) {
	conn := newMockConn()
	conn.addServerEvent("session.created", map[string]any{
		"session_id": "test-session-abc",
	})

	session := newGrokSession(conn, logging.DummyLogger())
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session, "SESSION.OPEN")
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
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	// First event should be SESSION.OPEN.
	first := readFromSession(t, ctx, session, "first SESSION.OPEN")
	if first.Type != messages.StreamTypeSessionOpen {
		t.Errorf("first event type: got %q, want %q", first.Type, messages.StreamTypeSessionOpen)
	}

	// Second event should be SESSION.CREATED with session config.
	second := readFromSession(t, ctx, session, "SESSION.CREATED")
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
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session, "SESSION.UPDATED")
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
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session, "SESSION.CLOSE")
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
	ctx := newGrokTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	got := readFromSession(t, ctx, session, "provider ERROR")
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
	waitForGrokSignal(t, session.Done(), "session termination after Close")
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
