package grok

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestConnectSession_HappyPath(t *testing.T) {
	conn := newMockConn()
	// Queue a session.created reply so the session receive buffer gets a SESSION.OPEN.
	conn.addServerEvent("session.created", map[string]any{
		"session_id": "test-abc",
	})

	dialer := &mockDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithBaseURL("wss://mock.example.com/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newGrokTestContext(t)

	session, err := provider.ConnectSession(ctx, models.SessionConfig{
		Model: "grok-3-mini",
		Voice: "Eve",
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Verify dial URL and auth header.
	if dialer.capturedURL != "wss://mock.example.com/v1/realtime" {
		t.Errorf("dial URL: got %q, want %q", dialer.capturedURL, "wss://mock.example.com/v1/realtime")
	}
	if dialer.capturedHeaders["Authorization"] != "Bearer test-key" {
		t.Errorf("auth header: got %q, want %q", dialer.capturedHeaders["Authorization"], "Bearer test-key")
	}

	// Verify the provider sent a session.update as the first client message.
	msgs := waitForGrokClientMessages(t, conn, 1, "initial session.update")
	var firstMsg map[string]json.RawMessage
	if err := json.Unmarshal(msgs[0], &firstMsg); err != nil {
		t.Fatalf("unmarshal first message: %v", err)
	}
	var msgType string
	if err := json.Unmarshal(firstMsg["type"], &msgType); err != nil {
		t.Fatalf("unmarshal first message type: %v", err)
	}
	if msgType != "session.update" {
		t.Errorf("first message type: got %q, want %q", msgType, "session.update")
	}

	// Verify session config fields in the session.update payload.
	var sessionField json.RawMessage
	if err := json.Unmarshal(firstMsg["session"], &sessionField); err != nil {
		t.Fatalf("unmarshal session field: %v", err)
	}
	var sessionPayload map[string]any
	if err := json.Unmarshal(sessionField, &sessionPayload); err != nil {
		t.Fatalf("unmarshal session payload: %v", err)
	}
	if sessionPayload["model"] != "grok-3-mini" {
		t.Errorf("session.update model: got %v, want grok-3-mini", sessionPayload["model"])
	}
	if sessionPayload["voice"] != "Eve" {
		t.Errorf("session.update voice: got %v, want Eve", sessionPayload["voice"])
	}

	// Verify we can receive the queued session.created → SESSION.OPEN event.
	got := readFromSession(t, ctx, session, "SESSION.OPEN")
	if got.Type != messages.StreamTypeSessionOpen {
		t.Errorf("received event type: got %q, want %q", got.Type, messages.StreamTypeSessionOpen)
	}
}

func TestConnectSession_DialError(t *testing.T) {
	dialer := &mockDialer{
		dialErr: errors.New("connection refused"),
	}
	provider := New(
		WithAPIKey("test-key"),
		WithWebSocketDialer(dialer),
	)

	_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "grok-3-mini"})
	if err == nil {
		t.Fatal("expected error from dial failure")
	}
	if !strings.Contains(err.Error(), "dial websocket") {
		t.Errorf("error should mention dial: %v", err)
	}
}

func TestConnectSession_MissingDialerFailsBeforeDial(t *testing.T) {
	provider := New(
		WithAPIKey("test-key"),
		WithBaseURL("wss://mock.example.com/v1/realtime"),
	)

	_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "grok-3-mini"})
	if err == nil {
		t.Fatal("expected missing dialer error")
	}
	if !strings.Contains(err.Error(), "websocket dialer is required") {
		t.Fatalf("expected missing dialer error, got: %v", err)
	}
}

func TestConnectSession_CustomConfig(t *testing.T) {
	conn := newMockConn()
	dialer := &mockDialer{conn: conn}
	provider := New(
		WithAPIKey("key"),
		WithWebSocketDialer(dialer),
	)

	ctx := newGrokTestContext(t)

	session, err := provider.ConnectSession(ctx, models.SessionConfig{
		Model:             "grok-3-mini",
		Voice:             "Rex",
		Instructions:      "Be helpful",
		InputAudioFormat:  models.AudioFormatPCM16,
		OutputAudioFormat: models.AudioFormatG711Ulaw,
		TurnDetection: &models.TurnDetectionConfig{
			Type:              "server_vad",
			Threshold:         0.5,
			SilenceDurationMs: 300,
		},
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Parse the session.update message and verify all config fields.
	msgs := waitForGrokClientMessages(t, conn, 1, "custom session.update")

	var flat map[string]json.RawMessage
	if err := json.Unmarshal(msgs[0], &flat); err != nil {
		t.Fatalf("unmarshal session.update envelope: %v", err)
	}
	var sessionPayload map[string]any
	if err := json.Unmarshal(flat["session"], &sessionPayload); err != nil {
		t.Fatalf("unmarshal session.update payload: %v", err)
	}

	if sessionPayload["voice"] != "Rex" {
		t.Errorf("voice: got %v, want Rex", sessionPayload["voice"])
	}
	if sessionPayload["instructions"] != "Be helpful" {
		t.Errorf("instructions: got %v, want 'Be helpful'", sessionPayload["instructions"])
	}
	if sessionPayload["input_audio_format"] != "pcm16" {
		t.Errorf("input_audio_format: got %v, want pcm16", sessionPayload["input_audio_format"])
	}
	if sessionPayload["output_audio_format"] != "g711_ulaw" {
		t.Errorf("output_audio_format: got %v, want g711_ulaw", sessionPayload["output_audio_format"])
	}

	td, ok := sessionPayload["turn_detection"].(map[string]any)
	if !ok {
		t.Fatal("turn_detection missing or wrong type")
	}
	if td["type"] != "server_vad" {
		t.Errorf("turn_detection.type: got %v, want server_vad", td["type"])
	}
}

func TestProviderName(t *testing.T) {
	p := New()
	if p.Name() != "grok" {
		t.Errorf("Name: got %q, want %q", p.Name(), "grok")
	}
}

func TestConnectSession_DefaultBaseURL(t *testing.T) {
	conn := newMockConn()
	dialer := &mockDialer{conn: conn}
	provider := New(
		WithAPIKey("key"),
		WithWebSocketDialer(dialer),
	)

	ctx := newGrokTestContext(t)

	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "grok-3-mini"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if dialer.capturedURL != "https://api.x.ai/v1/realtime" {
		t.Errorf("default URL: got %q, want %q", dialer.capturedURL, "https://api.x.ai/v1/realtime")
	}
}
