package openai

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"testing"
	"time"
)

func TestRealtimeSession_SendWithOutcomeLifecycle(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	sender, ok := session.(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("session does not implement SessionSendOutcomeSender")
	}

	// Unsupported outbound stream types fail terminally.
	outcome := sender.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeError})
	if outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("unsupported message status = %q, want terminal_failure", outcome.Status)
	}

	// A nil payload for a supported type is also a terminal failure.
	outcome = sender.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("nil-payload message status = %q, want terminal_failure", outcome.Status)
	}

	// A cancelled context reports cancellation.
	cancelledCtx, cancelInner := context.WithCancel(ctx)
	cancelInner()
	textInput := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi")}
	outcome = sender.SendWithOutcome(cancelledCtx, textInput)
	if outcome.Status != messages.SessionSendCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("cancelled send = %#v, want cancelled with context.Canceled", outcome)
	}

	// A deadline-exceeded context reports timeout.
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, 0)
	defer cancelTimeout()
	outcome = sender.SendWithOutcome(timeoutCtx, textInput)
	if outcome.Status != messages.SessionSendTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("timed-out send = %#v, want timed_out with DeadlineExceeded", outcome)
	}

	// A successful text-delta send maps to wire events.
	if outcome := sender.SendWithOutcome(ctx, textInput); outcome.Status != messages.SessionSendSucceeded {
		t.Fatalf("successful send status = %q, want succeeded", outcome.Status)
	}
}

func TestRealtimeSession_ToolCallEndSendsSingleFunctionCallOutput(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	sender, ok := session.(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("session does not implement SendWithOutcome")
	}
	outcome := sender.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-1", "tool_name", "result text"),
	})
	if outcome.Status != messages.SessionSendSucceeded {
		t.Fatalf("send status = %q (err=%v), want succeeded", outcome.Status, outcome.Err)
	}

	// ConnectSession emits one initial session.update; the tool result must
	// add exactly one more frame and nothing else.
	clientMessages := waitForClientMessages(t, conn, 2)
	if len(clientMessages) != 2 {
		t.Fatalf("client frames: got %d, want 2", len(clientMessages))
	}
	var event struct {
		Type string         `json:"type"`
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(clientMessages[1], &event); err != nil {
		t.Fatalf("unmarshal client event: %v", err)
	}
	if event.Type != "conversation.item.create" {
		t.Errorf("event type = %q, want conversation.item.create", event.Type)
	}
	assertStringField(t, event.Item, "type", "function_call_output")
	assertStringField(t, event.Item, "call_id", "call-1")
	assertStringField(t, event.Item, "output", "result text")
}

func TestRealtimeSession_ToolCallEndInvalidValueFailsWithoutFrames(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "gpt-realtime"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	sender, ok := session.(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("session does not implement SendWithOutcome")
	}
	before := len(conn.getClientMessages())

	outcome := sender.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd})
	if outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("nil-payload status = %q, want terminal_failure", outcome.Status)
	}
	wrongType := sender.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewTextDeltaValue("not a tool result"),
	})
	if wrongType.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("wrong-type status = %q, want terminal_failure", wrongType.Status)
	}

	if got := len(conn.getClientMessages()); got != before {
		t.Fatalf("client frames after failed sends: got %d, want %d (no frame written)", got, before)
	}
}
