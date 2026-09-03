package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestConnectSession_PreparesRTCMediaBeforeReadLoopForConsumer(t *testing.T) {
	conn := newMockWebSocketConn()
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(&mockWebSocketDialer{conn: conn}),
	)
	ctx := rtc.WithSessionMediaConsumer(newRealtimeTestContext(t))
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime", OutputAudioSampleRate: models.SampleRate24000})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	realtime := session.(*realtimeSession)
	if realtime.currentRTCMedia() == nil {
		t.Fatal("RTC media was not prepared before ConnectSession returned")
	}
}

func TestConnectSession_NormalizesOpenAIRealtimeEventsInOrder(t *testing.T) {
	conn := newMockWebSocketConn()
	audioB64 := base64.StdEncoding.EncodeToString([]byte("audio-chunk"))
	conn.addServerEvent("response.created", nil)
	conn.addServerEvent("conversation.item.input_audio_transcription.delta", map[string]any{"delta": "hello "})
	conn.addServerEvent("conversation.item.input_audio_transcription.completed", map[string]any{"transcript": "hello world"})
	conn.addServerEvent("response.output_text.delta", map[string]any{"delta": "hello"})
	conn.addServerEvent("response.output_audio.delta", map[string]any{
		"delta":  audioB64,
		"format": "pcm16",
	})
	conn.addServerEvent("response.output_audio_transcript.delta", map[string]any{"delta": "spoken"})
	conn.addServerEvent("response.output_item.added", map[string]any{
		"item": map[string]any{
			"type":    "function_call",
			"call_id": "call-weather",
			"name":    "lookup_weather",
		},
	})
	conn.addServerEvent("response.function_call_arguments.delta", map[string]any{
		"call_id": "call-weather",
		"delta":   `{"city":`,
	})
	conn.addServerEvent("response.function_call_arguments.done", map[string]any{
		"call_id":   "call-weather",
		"name":      "lookup_weather",
		"arguments": `{"city":"Seattle"}`,
	})
	conn.addServerEvent("response.output_audio_transcript.done", map[string]any{"transcript": "spoken words"})
	conn.addServerEvent("response.output_text.done", nil)
	conn.addServerEvent("response.output_audio.done", nil)
	conn.addServerEvent("response.done", nil)
	conn.addServerEvent("session.closed", map[string]any{
		"session_id": "sess-openai-normalize",
		"reason":     "fixture_complete",
	})
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newRealtimeTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeTextDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeTextEnd,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeSessionClose,
	}
	gotMessages := make([]messages.StreamMessage, 0, len(wantTypes))
	for range wantTypes {
		got, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("timed out waiting for normalized event %d", len(gotMessages))
		}
		gotMessages = append(gotMessages, got)
	}
	for i, want := range wantTypes {
		if gotMessages[i].Type != want {
			t.Fatalf("event %d type: got %q, want %q", i, gotMessages[i].Type, want)
		}
	}
	if text, ok := gotMessages[3].Value.(*messages.TextDeltaValue); !ok || text.Content != "hello" {
		t.Fatalf("text delta: got %#v", gotMessages[3].Value)
	}
	audio, ok := gotMessages[4].Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("audio delta: got %T", gotMessages[4].Value)
	}
	if string(audio.Content) != "audio-chunk" {
		t.Fatalf("audio content: got %q", string(audio.Content))
	}
	if audio.MediaType != "audio/pcm" {
		t.Fatalf("audio media type: got %q, want audio/pcm", audio.MediaType)
	}
	if gotMessages[7].ToolCallId != "call-weather" {
		t.Fatalf("tool delta call id: got %q", gotMessages[7].ToolCallId)
	}
	if gotMessages[1].Role != messages.RoleUser || gotMessages[2].Role != messages.RoleUser {
		t.Fatalf("input transcript roles: got %q and %q, want user", gotMessages[1].Role, gotMessages[2].Role)
	}
	inputTranscript, ok := gotMessages[2].Value.(*messages.TranscriptEndValue)
	if !ok || inputTranscript.FullText != "hello world" {
		t.Fatalf("input transcript end: got %#v", gotMessages[2].Value)
	}
	toolDone, ok := gotMessages[8].Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("tool end: got %T", gotMessages[8].Value)
	}
	if toolDone.ToolCallID != "call-weather" || toolDone.Name != "lookup_weather" || toolDone.Arguments != `{"city":"Seattle"}` {
		t.Fatalf("tool end value: got %#v", toolDone)
	}
	sessionClose, ok := gotMessages[13].Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("session close: got %T", gotMessages[13].Value)
	}
	if sessionClose.SessionID != "sess-openai-normalize" || sessionClose.Reason != "fixture_complete" {
		t.Fatalf("session close value: got %#v", sessionClose)
	}
	if sessionClose.Classification != string(messages.TerminalReasonProviderClose) ||
		sessionClose.TerminalReason != messages.TerminalReasonProviderClose ||
		sessionClose.TerminalProvenance != messages.TerminalProvenanceProvider ||
		sessionClose.OutputState != messages.TerminalOutputNotApplicable {
		t.Fatalf("session close terminal metadata: got %#v", sessionClose)
	}
}

func TestConnectSession_NormalizesOpenAIRealtimeErrorDetails(t *testing.T) {
	conn := newMockWebSocketConn()
	conn.addServerEvent("error", map[string]any{
		"error": map[string]any{
			"type":     "invalid_request_error",
			"code":     "invalid_event",
			"param":    "event.type",
			"event_id": "client-event-123",
			"message":  "Invalid realtime event.",
		},
	})
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newRealtimeTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("timed out waiting for error event")
	}
	if got.Type != messages.StreamTypeError {
		t.Fatalf("type: got %q, want %q", got.Type, messages.StreamTypeError)
	}
	value, ok := got.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("value: got %T, want *messages.ErrorValue", got.Value)
	}
	if value.Message != "Invalid realtime event." ||
		value.ErrorType != "invalid_request_error" ||
		value.Code != "invalid_event" ||
		value.Param != "event.type" ||
		value.EventID != "client-event-123" {
		t.Fatalf("error value: got %#v", value)
	}
	if value.Classification != providers.ErrorClassProviderRejected ||
		value.TerminalReason != messages.TerminalReasonTerminalFailure ||
		value.TerminalProvenance != messages.TerminalProvenanceProvider ||
		value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("error terminal metadata: got %#v", value)
	}
}

func TestConnectSession_IgnoresInactiveCancelRejectionAndContinuesResponse(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newRealtimeTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	var initial struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(waitForClientMessage(t, ctx, conn, "initial session.update"), &initial); err != nil {
		t.Fatalf("unmarshal initial client event: %v", err)
	}
	if initial.Type != string(models.SessionEventSessionUpdate) {
		t.Fatalf("initial client event type = %q, want %q", initial.Type, models.SessionEventSessionUpdate)
	}

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	}) {
		t.Fatal("sending response cancel returned false")
	}
	var cancelEvent struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(waitForClientMessage(t, ctx, conn, "response.cancel"), &cancelEvent); err != nil {
		t.Fatalf("unmarshal response.cancel event: %v", err)
	}
	if cancelEvent.Type != string(models.SessionEventResponseCancel) {
		t.Fatalf("cancel client event type = %q, want %q", cancelEvent.Type, models.SessionEventResponseCancel)
	}

	conn.addServerEvent("error", map[string]any{
		"error": map[string]any{
			"type":     "invalid_request_error",
			"code":     "response_cancel_not_active",
			"param":    "response.cancel",
			"event_id": "evt-cancel-1",
			"message":  "Can only cancel an active response.",
		},
	})
	got := readRealtimeMessage(t, session, ctx, "inactive-cancel diagnostic")
	if got.Type != messages.StreamTypeError {
		t.Fatalf("diagnostic type = %q, want %q", got.Type, messages.StreamTypeError)
	}
	diagnostic, ok := got.Value.(*messages.ErrorValue)
	if !ok || diagnostic == nil {
		t.Fatalf("diagnostic value = %T, want *messages.ErrorValue", got.Value)
	}
	if diagnostic.IsTerminal() || diagnostic.Classification != providers.ErrorClassResponseCancelNotActive ||
		diagnostic.ErrorType != "invalid_request_error" || diagnostic.Code != "response_cancel_not_active" ||
		diagnostic.Param != "response.cancel" || diagnostic.EventID != "evt-cancel-1" {
		t.Fatalf("inactive-cancel diagnostic = %#v", diagnostic)
	}
	select {
	case <-session.Done():
		t.Fatal("inactive-cancel diagnostic terminated the session")
	default:
	}

	conn.addServerEvent("response.created", nil)
	conn.addServerEvent("response.output_text.delta", map[string]any{"delta": "still alive"})
	conn.addServerEvent("response.output_text.done", nil)
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"status": "completed"}})
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	}
	for _, wantType := range wantTypes {
		got = readRealtimeMessage(t, session, ctx, fmt.Sprintf("%s after inactive-cancel diagnostic", wantType))
		if got.Type != wantType {
			t.Fatalf("event after diagnostic = %q, want %q", got.Type, wantType)
		}
	}
	end, ok := got.Value.(*messages.MessageEndValue)
	if !ok || end == nil {
		t.Fatalf("final event value = %T, want *MessageEndValue", got.Value)
	}
	if end.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
		t.Fatalf("final MESSAGE.END terminal reason = %q, want provider completion", end.TerminalReason)
	}
}

func TestConnectSession_SurfacesUnexpectedWebSocketReadError(t *testing.T) {
	readErr := errors.New("websocket: close 1008 (policy violation): invalid API key")
	dialer := &readErrorWebSocketDialer{conn: &readErrorWebSocketConn{err: readErr}}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newRealtimeTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("did not receive unexpected WebSocket read error")
	}
	if got.Type != messages.StreamTypeError {
		t.Fatalf("type: got %q, want %q", got.Type, messages.StreamTypeError)
	}
	value, ok := got.Value.(*messages.ErrorValue)
	if !ok || value == nil {
		t.Fatalf("value: got %T, want *messages.ErrorValue", got.Value)
	}
	if value.Message != readErr.Error() || value.Classification != providers.ErrorClassTransport {
		t.Fatalf("transport error value: got %#v, want message %q and classification %q", value, readErr.Error(), providers.ErrorClassTransport)
	}
	if !errors.Is(value.Err, readErr) {
		t.Fatalf("transport error cause = %v, want %v", value.Err, readErr)
	}
}

func TestConnectSession_ReplaysOpenAIRealtimeTextFixture(t *testing.T) {
	replayDialer, err := gwtesting.NewReplayWebSocketDialer(filepath.Join("testdata", "realtime_text.session.json"))
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	provider := New(
		WithAPIKey("replay-key"),
		WithRealtimeBaseURL("wss://replay.openai.test/v1/realtime"),
		WithWebSocketDialer(replayDialer),
	)

	ctx := newRealtimeTestContext(t)
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	openMsg, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("timed out waiting for replayed session.open")
	}
	if openMsg.Type != messages.StreamTypeSessionOpen {
		t.Fatalf("first replay event: got %q, want %q", openMsg.Type, messages.StreamTypeSessionOpen)
	}
	createdMsg, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("timed out waiting for replayed session.created")
	}
	if createdMsg.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("second replay event: got %q, want %q", createdMsg.Type, messages.StreamTypeSessionCreated)
	}

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello realtime"),
	}) {
		t.Fatalf("Send user input returned false: %v", replayDialer.Err())
	}

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	}
	for i, want := range wantTypes {
		got, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("timed out waiting for replayed response event %d (%s)", i, want)
		}
		if got.Type != want {
			t.Fatalf("response event %d: got %q, want %q", i, got.Type, want)
		}
		if want == messages.StreamTypeTextDelta {
			delta, ok := got.Value.(*messages.TextDeltaValue)
			if !ok {
				t.Fatalf("text delta value: got %T", got.Value)
			}
			if delta.Content != "fixture response" {
				t.Fatalf("text delta content: got %q, want fixture response", delta.Content)
			}
		}
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := replayDialer.Err(); err != nil {
		t.Fatalf("replay diverged: %v", err)
	}
}
