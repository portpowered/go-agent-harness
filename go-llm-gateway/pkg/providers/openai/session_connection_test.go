package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestConnectSession_PreparesRTCMediaBeforeReadLoopForConsumer(t *testing.T) {
	conn := newMockWebSocketConn()
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(&mockWebSocketDialer{conn: conn}),
	)
	ctx := newRealtimeTestContext(t)
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

func TestRealtimeSession_QueuesLateToolContinuationUntilResponseDone(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response request: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")

	// The provider's response.created is the authoritative indication that a
	// response is active. A timeout result can arrive while that response is
	// still streaming, so its result and continuation are admitted locally and
	// dispatched only after response.done.
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-active"}})
	if got := readRealtimeMessage(t, session, ctx, "response.created"); got.Type != messages.StreamTypeMessageStart {
		t.Fatalf("response.created normalized as %s, want MESSAGE.START", got.Type)
	}

	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-late-timeout", "slow_tool", "timeout output"),
	}); !outcome.OK() {
		t.Fatalf("late timeout result admission: %#v", outcome)
	}
	if got := len(conn.getClientMessages()); got != 1 {
		t.Fatalf("wire frames while response active = %d, want 1 (initial response only)", got)
	}
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("queued continuation admission: %#v", outcome)
	}

	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-active", "status": "failed"}})
	if got := readRealtimeMessage(t, session, ctx, "response.done"); got.Type != messages.StreamTypeMessageEnd {
		t.Fatalf("response.done normalized as %s, want MESSAGE.END", got.Type)
	}
	waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if frames[0].Type != "response.create" || frames[1].Type != "conversation.item.create" || frames[2].Type != "response.create" {
		t.Fatalf("wire order = %#v, want response.create, function_call_output item, response.create", frames)
	}
}

func TestRealtimeSession_DropsStaleResponseCreateBeforeReplacementToolResult(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response admission: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-original"}})
	if got := readRealtimeMessage(t, session, ctx, "original response.created"); got.Type != messages.StreamTypeMessageStart {
		t.Fatalf("response.created normalized as %s, want MESSAGE.START", got.Type)
	}

	// The replacement response starts as a function call. The standalone
	// request below may be admitted after response.created but before the
	// function-call output item is observed, so it must still be retired.
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-replacement"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("queued correction response admission: %#v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-replacement", "write_file", "result"),
	}); !outcome.OK() {
		t.Fatalf("queued replacement tool result admission: %#v", outcome)
	}

	// The provider's function-call item proves that the standalone request was
	// stale. Retire only it, preserving the function_call_output intent.
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	session.responseMu.Lock()
	if len(session.pendingResponseIntents) != 1 || len(session.pendingResponseIntents[0].events) != 1 || session.pendingResponseIntents[0].events[0].Type != conversationItemCreateEvent {
		t.Fatalf("pending intents after replacement = %#v, want only function_call_output", session.pendingResponseIntents)
	}
	session.responseMu.Unlock()

	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-replacement","status":"completed"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("stale response admission after function-call completion: %#v", outcome)
	}
	waitForFrameCount(t, conn, 2, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if len(frames) != 2 || frames[0].Type != "response.create" || frames[1].Type != string(conversationItemCreateEvent) {
		t.Fatalf("wire order after replacement = %#v, want response.create then function_call_output", frames)
	}
}

func TestRealtimeSession_CancelClearsFunctionCallResponseSuppression(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	}); !outcome.OK() {
		t.Fatalf("response.cancel admission: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "response.cancel")
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-tool","status":"cancelled"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("fresh response admission after cancellation: %#v", outcome)
	}
	waitForFrameCount(t, conn, 2, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if frames[0].Type != "response.cancel" || frames[1].Type != "response.create" {
		t.Fatalf("wire order after cancelled function response = %#v, want cancel then fresh response.create", frames)
	}
}

func TestRealtimeSession_PreservesFreshUserTurnWhileFunctionCallPending(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	if !session.SendMessage(ctx, messages.NewTextMessage(messages.RoleUser, "fresh turn")) {
		t.Fatal("fresh user turn was rejected while function call was pending")
	}

	session.responseMu.Lock()
	defer session.responseMu.Unlock()
	if len(session.pendingResponseIntents) != 1 {
		t.Fatalf("pending intents = %#v, want one fresh user turn", session.pendingResponseIntents)
	}
	intent := session.pendingResponseIntents[0]
	if len(intent.events) != 2 || intent.events[0].Type != conversationItemCreateEvent || intent.events[1].Type != models.SessionEventResponseCreate {
		t.Fatalf("fresh user turn events = %#v, want item followed by response.create", intent.events)
	}
	if !strings.Contains(string(intent.events[0].Data), "fresh turn") {
		t.Fatalf("fresh user turn payload = %s", intent.events[0].Data)
	}
}

func TestRealtimeSession_PreservesAudioCommitWhenSuppressingStaleResponse(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}); !outcome.OK() {
		t.Fatalf("audio end admission: %#v", outcome)
	}
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	session.responseMu.Lock()
	if len(session.pendingResponseIntents) != 1 || len(session.pendingResponseIntents[0].events) != 2 || session.pendingResponseIntents[0].events[0].Type != models.SessionEventInputAudioBufferCommit || session.pendingResponseIntents[0].events[1].Type != models.SessionEventResponseCreate {
		t.Fatalf("pending audio intent = %#v, want preserved commit and response.create", session.pendingResponseIntents)
	}
	session.responseMu.Unlock()
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-tool","status":"completed"}}`),
	})
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-tool", "tool", "result"),
	}); !outcome.OK() {
		t.Fatalf("tool result admission: %#v", outcome)
	}
	waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if len(frames) != 3 || frames[0].Type != string(conversationItemCreateEvent) || frames[1].Type != "input_audio_buffer.commit" || frames[2].Type != "response.create" {
		t.Fatalf("wire frames = %#v, want tool result then commit then response.create", frames)
	}
}

func TestRealtimeSession_PreservesFreshAudioResponseAfterFunctionCall(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-tool","status":"completed"}}`),
	})
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}); !outcome.OK() {
		t.Fatalf("fresh audio end admission: %#v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-tool", "tool", "result"),
	}); !outcome.OK() {
		t.Fatalf("tool result admission after fresh audio: %#v", outcome)
	}
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("fresh audio continuation admission: %#v", outcome)
	}
	waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if len(frames) != 3 || frames[0].Type != "input_audio_buffer.commit" || frames[1].Type != string(conversationItemCreateEvent) || frames[2].Type != "response.create" {
		t.Fatalf("fresh audio wire frames = %#v, want commit then tool result then response.create", frames)
	}
}

func TestRealtimeSession_ResponseDoneRequiresMatchingIdentity(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-current"}}`),
	})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"status":"completed"}}`),
	})
	session.responseMu.Lock()
	active := session.responseActive
	session.responseMu.Unlock()
	if !active {
		t.Fatal("response.done without an id released the active response")
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-current","conversation":"none"}}`),
	})
	session.responseMu.Lock()
	active = session.responseActive
	session.responseMu.Unlock()
	if !active {
		t.Fatal("out-of-band response.done released the active response")
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-current","status":"completed"}}`),
	})
	session.responseMu.Lock()
	active = session.responseActive
	session.responseMu.Unlock()
	if active {
		t.Fatal("matching response.done did not release the active response")
	}

	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"unexpected"}}`),
	})
	session.responseMu.Lock()
	active = session.responseActive
	session.responseMu.Unlock()
	if !active {
		t.Fatal("response.done id released response with unknown local identity")
	}
}

func TestRealtimeSession_ToolResultBufferFullDoesNotAdmitResult(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	session.responseMu.Lock()
	session.pendingResponseIntents = make([]responseIntent, maxPendingResponseIntents)
	session.responseMu.Unlock()
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-full", "tool", "result"),
	}); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("tool result admission = %#v, want buffer full", outcome)
	}
	session.responseMu.Lock()
	admitted := session.toolResultAdmitted
	session.responseMu.Unlock()
	if admitted {
		t.Fatal("buffer-full tool result was marked admitted")
	}
}

func TestRealtimeSession_AllowsMultipleToolResultsBeforeContinuation(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tools"}}`),
	})
	session.observeResponseLifecycle(models.SessionEvent{
		Type: models.SessionEventResponseOutputItemAdded,
		Data: []byte(`{"item":{"type":"function_call"}}`),
	})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-tools","status":"completed"}}`),
	})
	for _, callID := range []string{"call-1", "call-2"} {
		if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Value: messages.NewToolCallEndValue(callID, "tool", "result"),
		}); !outcome.OK() {
			t.Fatalf("tool result %s admission: %#v", callID, outcome)
		}
	}
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("tool continuation response admission: %#v", outcome)
	}
	waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if len(frames) != 3 || frames[0].Type != string(conversationItemCreateEvent) || frames[1].Type != string(conversationItemCreateEvent) || frames[2].Type != "response.create" {
		t.Fatalf("wire order after multiple tool results = %#v, want two items then response.create", frames)
	}
}

func TestRealtimeSession_CancellingQueuedContinuationInvalidatesIt(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response request: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")
	conn.addServerEvent("response.created", nil)
	if got := readRealtimeMessage(t, session, ctx, "response.created"); got.Type != messages.StreamTypeMessageStart {
		t.Fatalf("response.created normalized as %s, want MESSAGE.START", got.Type)
	}

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("queued continuation admission = %#v, want accepted local queueing", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}); !outcome.OK() {
		t.Fatalf("response.cancel admission = %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "response.cancel")
	if got := len(conn.getClientMessages()); got != 2 {
		t.Fatalf("wire frames after cancelled queued continuation = %d, want 2 including cancel", got)
	}
}

func TestRealtimeSession_CancelWaitsForPoppedContinuationAdmission(t *testing.T) {
	conn := newMockWebSocketConn()
	entered := make(chan struct{})
	release := make(chan struct{})
	session := newRealtimeSession(conn, nil)
	session.responseDispatchBarrier = func() {
		close(entered)
		<-release
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response admission: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-active"}})
	if got := readRealtimeMessage(t, session, ctx, "response.created"); got.Type != messages.StreamTypeMessageStart {
		t.Fatalf("response.created normalized as %s, want MESSAGE.START", got.Type)
	}
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("continuation admission: %#v", outcome)
	}

	// Completing the active response wakes the independent dispatcher. The
	// barrier freezes it after the pending intent is popped, exactly where the
	// old implementation allowed response.cancel to invalidate the generation
	// before the popped intent reached sendQueue.
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-active","status":"failed"}}`),
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("pending continuation was not popped before cancellation")
	}

	cancelDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		cancelDone <- session.SendWithOutcome(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeResponseCancel,
			Value: messages.NewResponseCancelValue(),
		})
	}()
	select {
	case outcome := <-cancelDone:
		t.Fatalf("response.cancel completed while popped continuation held admission: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	select {
	case outcome := <-cancelDone:
		if !outcome.OK() {
			t.Fatalf("response.cancel admission: %#v", outcome)
		}
	case <-ctx.Done():
		t.Fatal("response.cancel remained blocked after continuation admission")
	}
	waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	frames := parseWireFrames(t, conn.getClientMessages())
	if frames[0].Type != "response.create" || frames[1].Type != "response.create" || frames[2].Type != "response.cancel" {
		t.Fatalf("wire order = %#v, want initial response.create, popped continuation, response.cancel", frames)
	}
}

func TestRealtimeSession_DispatchFailureInvalidatesBeforeFreshAdmission(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	session.responseDispatchFailureBarrier = func() {
		close(entered)
		<-release
	}
	for index := 0; index < 64; index++ {
		if !session.sendQueue.Write(context.Background(), models.SessionEvent{Type: models.SessionEventSessionUpdate}) {
			t.Fatalf("fill send queue at %d", index)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-active"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("queue continuation: %#v", outcome)
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-active","status":"failed"}}`),
	})
	dispatchDone := make(chan struct{})
	go func() {
		session.dispatchPendingResponseIntents()
		close(dispatchDone)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("failed dispatch did not reach cleanup barrier")
	}

	freshDone := make(chan messages.SessionSendOutcome, 1)
	go func() { freshDone <- session.RequestResponse(ctx) }()
	select {
	case outcome := <-freshDone:
		t.Fatalf("fresh response admitted before failed generation invalidation: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := session.sendQueue.Read(); !ok {
		t.Fatal("failed to free one send queue slot")
	}
	close(release)
	select {
	case <-dispatchDone:
	case <-ctx.Done():
		t.Fatal("failed dispatch did not finish")
	}
	select {
	case outcome := <-freshDone:
		if !outcome.OK() {
			t.Fatalf("fresh response after failed generation: %#v", outcome)
		}
	case <-ctx.Done():
		t.Fatal("fresh response remained blocked after failed generation cleanup")
	}
}

func TestRealtimeSession_CancelRejectionInvalidatesQueuedContinuation(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response request: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")
	conn.addServerEvent("response.created", nil)
	if got := readRealtimeMessage(t, session, ctx, "response.created"); got.Type != messages.StreamTypeMessageStart {
		t.Fatalf("response.created normalized as %s, want MESSAGE.START", got.Type)
	}

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("continuation admission: %#v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	}); !outcome.OK() {
		t.Fatalf("response.cancel: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "response.cancel")
	conn.addServerEvent("error", map[string]any{"error": map[string]any{
		"type": "invalid_request_error", "code": "response_cancel_not_active",
		"param": "response.cancel", "message": "Can only cancel an active response.",
	}})
	if got := readRealtimeMessage(t, session, ctx, "response.cancel rejection"); got.Type != messages.StreamTypeError {
		t.Fatalf("cancel rejection normalized as %s, want ERROR", got.Type)
	}
	if got := len(conn.getClientMessages()); got != 2 {
		t.Fatalf("wire frames after cancel rejection = %d, want initial response and cancel", got)
	}
}

func TestRealtimeSession_IgnoresStaleResponseDoneForCurrentResponse(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	if outcome := session.sendEvents(ctx, []models.SessionEvent{models.NewResponseCreateEvent()}); !outcome.OK() {
		t.Fatalf("current response admission: %#v", outcome)
	}
	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated, Data: []byte(`{"response":{"id":"resp-current"}}`)})
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	session.responseMu.Lock()
	active := session.responseActive
	session.responseMu.Unlock()
	if !active {
		t.Fatal("stale response.done released the current response admission")
	}
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-current"}}`)})
	session.responseMu.Lock()
	active = session.responseActive
	session.responseMu.Unlock()
	if active {
		t.Fatal("current response.done did not release response admission")
	}
}

func TestRealtimeSession_ResponseAdmissionExcludesOutOfBandCreate(t *testing.T) {
	oob := models.SessionEvent{
		Type: models.SessionEventResponseCreate,
		Data: []byte(`{"response":{"conversation":"none"}}`),
	}
	if realtimeEventNeedsResponseAdmission(oob) {
		t.Fatal("out-of-band response.create was incorrectly serialized with default conversation")
	}
	defaultConversation := models.SessionEvent{Type: models.SessionEventResponseCreate}
	if !realtimeEventNeedsResponseAdmission(defaultConversation) {
		t.Fatal("default response.create was not admitted")
	}
}

func TestRealtimeSession_CloneSessionEventsCopiesRawData(t *testing.T) {
	original := []models.SessionEvent{{Type: models.SessionEventResponseCreate, Data: []byte(`{"response":{"conversation":"none"}}`)}}
	cloned := cloneSessionEvents(original)
	if len(cloned) != 1 || &cloned[0].Data[0] == &original[0].Data[0] {
		t.Fatal("clone retained raw event backing storage")
	}
	original[0].Data[0] = 'x'
	if cloned[0].Data[0] == 'x' {
		t.Fatal("mutating source event changed queued intent")
	}
}

func TestRealtimeSession_ResponseIntentOverflowIsExplicit(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated})
	for i := 0; i < maxPendingResponseIntents; i++ {
		if outcome := session.RequestResponse(ctx); !outcome.OK() {
			t.Fatalf("pending response intent %d: %#v", i, outcome)
		}
	}
	if outcome := session.RequestResponse(ctx); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("overflow response intent = %#v, want buffer_full", outcome)
	}
}

func TestRealtimeSession_RetriesOwnedCreateAfterActiveResponseRejection(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.start(ctx)
	defer func() { _ = session.Close() }()

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response request: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "initial response.create")
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-initial"}})
	readRealtimeMessage(t, session, ctx, "initial response.created")
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-initial"}})
	readRealtimeMessage(t, session, ctx, "initial response.done")

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("owned continuation request: %#v", outcome)
	}
	waitForClientMessage(t, ctx, conn, "owned continuation response.create")
	// Simulate an automatic default-conversation response winning the tiny
	// server race after the client write. The exact active-response rejection
	// must retain and replay the owned continuation after that response ends.
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-auto"}})
	readRealtimeMessage(t, session, ctx, "automatic response.created")
	conn.addServerEvent("error", map[string]any{"error": map[string]any{
		"type": "invalid_request_error", "code": realtimeResponseCreateActiveCode,
		"message": "Conversation already has an active response.",
	}})
	if got := readRealtimeMessage(t, session, ctx, "active-response rejection"); got.Type != messages.StreamTypeError {
		t.Fatalf("active-response rejection normalized as %s, want ERROR", got.Type)
	}
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-auto", "status": "completed"}})
	readRealtimeMessage(t, session, ctx, "automatic response.done")
	frames := waitForFrameCount(t, conn, 3, time.Now().Add(time.Second))
	if frames[0].Type != "response.create" || frames[1].Type != "response.create" || frames[2].Type != "response.create" {
		t.Fatalf("response create retry wire sequence = %#v, want initial, rejected, retry", frames)
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
