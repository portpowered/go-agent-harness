package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockWebSocketConn struct {
	mu sync.Mutex

	serverMessages  [][]byte
	readIdx         int
	clientMessages  [][]byte
	clientMessageCh chan []byte
	clientWriteCh   chan struct{}

	closed    bool
	readBlock chan struct{}
	writeErr  error
}

func newMockWebSocketConn() *mockWebSocketConn {
	return &mockWebSocketConn{
		readBlock:       make(chan struct{}),
		clientMessageCh: make(chan []byte, 64),
		clientWriteCh:   make(chan struct{}, 1),
	}
}

func (c *mockWebSocketConn) addServerEvent(eventType string, fields map[string]any) {
	message := map[string]any{"type": eventType}
	for key, value := range fields {
		message[key] = value
	}
	data, _ := json.Marshal(message)
	c.mu.Lock()
	c.serverMessages = append(c.serverMessages, data)
	c.mu.Unlock()
	select {
	case c.readBlock <- struct{}{}:
	default:
	}
}

func (c *mockWebSocketConn) ReadMessage() (int, []byte, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return 0, nil, errors.New("connection closed")
		}
		if c.readIdx < len(c.serverMessages) {
			msg := c.serverMessages[c.readIdx]
			c.readIdx++
			c.mu.Unlock()
			return 1, msg, nil
		}
		c.mu.Unlock()
		<-c.readBlock
	}
}

func (c *mockWebSocketConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	if c.writeErr != nil {
		return c.writeErr
	}
	payload := append([]byte(nil), data...)
	c.clientMessages = append(c.clientMessages, payload)
	select {
	case c.clientMessageCh <- payload:
	default:
	}
	select {
	case c.clientWriteCh <- struct{}{}:
	default:
	}
	return nil
}

func (c *mockWebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.readBlock)
	return nil
}

func (c *mockWebSocketConn) getClientMessages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages := make([][]byte, len(c.clientMessages))
	copy(messages, c.clientMessages)
	return messages
}

type readErrorWebSocketConn struct {
	err error
}

func (c *readErrorWebSocketConn) ReadMessage() (int, []byte, error) {
	return 0, nil, c.err
}

func (c *readErrorWebSocketConn) WriteMessage(int, []byte) error {
	return nil
}

func (c *readErrorWebSocketConn) Close() error {
	return nil
}

type mockWebSocketDialer struct {
	conn            *mockWebSocketConn
	err             error
	capturedURL     string
	capturedHeaders map[string]string
}

type readErrorWebSocketDialer struct {
	conn WebSocketConn
}

func (d *readErrorWebSocketDialer) Dial(string, map[string]string) (WebSocketConn, error) {
	return d.conn, nil
}

func (d *mockWebSocketDialer) Dial(url string, headers map[string]string) (WebSocketConn, error) {
	d.capturedURL = url
	d.capturedHeaders = headers
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func TestConnectSession_OpenAIRealtimeSessionCreatedThroughGateway(t *testing.T) {
	conn := newMockWebSocketConn()
	conn.addServerEvent("session.created", map[string]any{
		"session": map[string]any{
			"id":    "sess-openai-123",
			"model": "gpt-realtime",
		},
	})
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}

	ctx := newRealtimeTestContext(t)

	session, err := sessionGateway.ConnectSession(ctx, models.SessionConfig{
		Model: "gpt-realtime",
		Voice: "alloy",
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if dialer.capturedURL != "wss://mock.openai.test/v1/realtime?model=gpt-realtime" {
		t.Errorf("dial URL: got %q", dialer.capturedURL)
	}
	if got := dialer.capturedHeaders["Authorization"]; got != "Bearer test-key" {
		t.Errorf("authorization header: got %q", got)
	}
	if got := dialer.capturedHeaders["OpenAI-Beta"]; got != "" {
		t.Errorf("OpenAI-Beta header should not be sent by default; got %q", got)
	}

	clientMessages := conn.getClientMessages()
	if len(clientMessages) == 0 {
		t.Fatal("expected initial session.update client event")
	}
	var firstMessage map[string]json.RawMessage
	if err := json.Unmarshal(clientMessages[0], &firstMessage); err != nil {
		t.Fatalf("unmarshal session.update: %v", err)
	}
	var eventType string
	if err := json.Unmarshal(firstMessage["type"], &eventType); err != nil {
		t.Fatalf("unmarshal event type: %v", err)
	}
	if eventType != string(models.SessionEventSessionUpdate) {
		t.Errorf("first event type: got %q, want %q", eventType, models.SessionEventSessionUpdate)
	}

	got, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("timed out waiting for normalized session.created event")
	}
	if got.Type != messages.StreamTypeSessionOpen {
		t.Errorf("first normalized event: got %q, want %q", got.Type, messages.StreamTypeSessionOpen)
	}
}

func TestConnectSession_SendsGARealtimeSessionUpdateBeforeUserInput(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	ctx := newRealtimeTestContext(t)

	session, err := provider.ConnectSession(ctx, models.SessionConfig{
		Model: "gpt-realtime",
		Modalities: []models.SessionModality{
			models.SessionModalityText,
			models.SessionModalityAudio,
		},
		Voice:                 "marin",
		Instructions:          "Keep responses concise.",
		InputAudioFormat:      models.AudioFormatPCM16,
		InputAudioSampleRate:  models.SampleRate24000,
		OutputAudioFormat:     models.AudioFormatG711Ulaw,
		OutputAudioSampleRate: models.SampleRate24000,
		TurnDetection: &models.TurnDetectionConfig{
			Type:              "server_vad",
			Threshold:         0.5,
			PrefixPaddingMs:   300,
			SilenceDurationMs: 500,
		},
		Tools: []models.ToolDefinition{
			{
				Name:        "lookup_weather",
				Description: "Look up weather.",
				Parameters: []models.ToolParameter{
					{Name: "city", Type: "string", Description: "City name", Required: true},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	clientMessages := conn.getClientMessages()
	if len(clientMessages) != 1 {
		t.Fatalf("client messages before user input: got %d, want exactly initial session.update", len(clientMessages))
	}

	var firstMessage map[string]json.RawMessage
	if err := json.Unmarshal(clientMessages[0], &firstMessage); err != nil {
		t.Fatalf("unmarshal session.update: %v", err)
	}
	var eventType string
	if err := json.Unmarshal(firstMessage["type"], &eventType); err != nil {
		t.Fatalf("unmarshal event type: %v", err)
	}
	if eventType != string(models.SessionEventSessionUpdate) {
		t.Fatalf("first event type: got %q, want %q", eventType, models.SessionEventSessionUpdate)
	}

	var sessionPayload map[string]any
	if err := json.Unmarshal(firstMessage["session"], &sessionPayload); err != nil {
		t.Fatalf("unmarshal session payload: %v", err)
	}
	assertStringField(t, sessionPayload, "type", "realtime")
	assertStringField(t, sessionPayload, "model", "gpt-realtime")
	assertStringField(t, sessionPayload, "instructions", "Keep responses concise.")
	assertStringSliceField(t, sessionPayload, "output_modalities", []string{"text", "audio"})
	if _, ok := sessionPayload["input_audio_format"]; ok {
		t.Fatal("default OpenAI realtime session.update should not use legacy flat input_audio_format")
	}
	if _, ok := sessionPayload["turn_detection"]; ok {
		t.Fatal("default OpenAI realtime session.update should not use legacy flat turn_detection")
	}

	audio, ok := sessionPayload["audio"].(map[string]any)
	if !ok {
		t.Fatalf("audio config missing or wrong type: %T", sessionPayload["audio"])
	}
	input, ok := audio["input"].(map[string]any)
	if !ok {
		t.Fatalf("audio.input missing or wrong type: %T", audio["input"])
	}
	inputFormat, ok := input["format"].(map[string]any)
	if !ok {
		t.Fatalf("audio.input.format missing or wrong type: %T", input["format"])
	}
	assertStringField(t, inputFormat, "type", "audio/pcm")
	if got := inputFormat["rate"]; got != float64(models.SampleRate24000) {
		t.Errorf("audio.input.format.rate: got %v, want %d", got, models.SampleRate24000)
	}
	turnDetection, ok := input["turn_detection"].(map[string]any)
	if !ok {
		t.Fatalf("audio.input.turn_detection missing or wrong type: %T", input["turn_detection"])
	}
	assertStringField(t, turnDetection, "type", "server_vad")
	if _, ok := turnDetection["create_response"]; ok {
		t.Fatal("server-owned OpenAI realtime VAD should leave create_response unspecified")
	}

	output, ok := audio["output"].(map[string]any)
	if !ok {
		t.Fatalf("audio.output missing or wrong type: %T", audio["output"])
	}
	outputFormat, ok := output["format"].(map[string]any)
	if !ok {
		t.Fatalf("audio.output.format missing or wrong type: %T", output["format"])
	}
	assertStringField(t, outputFormat, "type", "audio/pcmu")
	assertStringField(t, output, "voice", "marin")

	tools, ok := sessionPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: got %#v, want one tool", sessionPayload["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool wrong type: %T", tools[0])
	}
	assertStringField(t, tool, "type", "function")
	assertStringField(t, tool, "name", "lookup_weather")
	parameters, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tool parameters missing or wrong type: %T", tool["parameters"])
	}
	required, ok := parameters["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "city" {
		t.Fatalf("tool required parameters: got %#v, want [city]", parameters["required"])
	}
}

func TestConnectSession_ClientOwnedAudioTurnBoundariesDisableTurnDetection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "ga"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			conn := newMockWebSocketConn()
			dialer := &mockWebSocketDialer{conn: conn}
			options := []Option{
				WithAPIKey("test-key"),
				WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
				WithWebSocketDialer(dialer),
				WithClientOwnedAudioTurnBoundaries(),
			}
			if legacy {
				options = append(options, WithLegacyRealtimeSessionUpdate())
			}
			provider := New(options...)

			createResponse := true
			session, err := provider.ConnectSession(context.Background(), models.SessionConfig{
				Model: "gpt-realtime",
				TurnDetection: &models.TurnDetectionConfig{
					Type:           "server_vad",
					CreateResponse: &createResponse,
				},
			})
			if err != nil {
				t.Fatalf("ConnectSession: %v", err)
			}
			defer func() { _ = session.Close() }()

			clientMessages := conn.getClientMessages()
			if len(clientMessages) != 1 {
				t.Fatalf("client messages: got %d, want initial session.update only", len(clientMessages))
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(clientMessages[0], &envelope); err != nil {
				t.Fatalf("unmarshal session.update: %v", err)
			}
			var sessionPayload map[string]json.RawMessage
			if err := json.Unmarshal(envelope["session"], &sessionPayload); err != nil {
				t.Fatalf("unmarshal session payload: %v", err)
			}

			var turnDetection json.RawMessage
			if legacy {
				turnDetection = sessionPayload["turn_detection"]
				if len(turnDetection) == 0 {
					t.Fatal("legacy turn_detection field is missing")
				}
			} else {
				var audio map[string]json.RawMessage
				if err := json.Unmarshal(sessionPayload["audio"], &audio); err != nil {
					t.Fatalf("decode audio config: %v", err)
				}
				var input map[string]json.RawMessage
				if err := json.Unmarshal(audio["input"], &input); err != nil {
					t.Fatalf("decode audio.input config: %v", err)
				}
				turnDetection = input["turn_detection"]
				if len(turnDetection) == 0 {
					t.Fatal("audio.input.turn_detection field is missing")
				}
			}
			if strings.TrimSpace(string(turnDetection)) != "null" {
				t.Fatalf("turn_detection = %s, want explicit null", turnDetection)
			}
		})
	}
}

func TestConnectSession_SendsGARealtimeG711AudioFormatValues(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	session, err := provider.ConnectSession(context.Background(), models.SessionConfig{
		Model:             "gpt-realtime",
		InputAudioFormat:  models.AudioFormatG711Ulaw,
		OutputAudioFormat: models.AudioFormatG711Alaw,
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	clientMessages := conn.getClientMessages()
	if len(clientMessages) == 0 {
		t.Fatal("expected initial session.update client event")
	}
	var firstMessage map[string]json.RawMessage
	if err := json.Unmarshal(clientMessages[0], &firstMessage); err != nil {
		t.Fatalf("unmarshal session.update: %v", err)
	}
	var sessionPayload map[string]any
	if err := json.Unmarshal(firstMessage["session"], &sessionPayload); err != nil {
		t.Fatalf("unmarshal session payload: %v", err)
	}
	audio, ok := sessionPayload["audio"].(map[string]any)
	if !ok {
		t.Fatalf("audio config missing or wrong type: %T", sessionPayload["audio"])
	}
	input, ok := audio["input"].(map[string]any)
	if !ok {
		t.Fatalf("audio.input missing or wrong type: %T", audio["input"])
	}
	inputFormat, ok := input["format"].(map[string]any)
	if !ok {
		t.Fatalf("audio.input.format missing or wrong type: %T", input["format"])
	}
	assertStringField(t, inputFormat, "type", "audio/pcmu")
	output, ok := audio["output"].(map[string]any)
	if !ok {
		t.Fatalf("audio.output missing or wrong type: %T", audio["output"])
	}
	outputFormat, ok := output["format"].(map[string]any)
	if !ok {
		t.Fatalf("audio.output.format missing or wrong type: %T", output["format"])
	}
	assertStringField(t, outputFormat, "type", "audio/pcma")
}

func TestConnectSession_LegacyRealtimeSessionUpdateIsExplicit(t *testing.T) {
	conn := newMockWebSocketConn()
	dialer := &mockWebSocketDialer{conn: conn}
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
		WithLegacyRealtimeSessionUpdate(),
	)

	session, err := provider.ConnectSession(context.Background(), models.SessionConfig{
		Model:             "gpt-realtime",
		Voice:             "alloy",
		InputAudioFormat:  models.AudioFormatPCM16,
		OutputAudioFormat: models.AudioFormatG711Alaw,
		TurnDetection:     &models.TurnDetectionConfig{Type: "server_vad"},
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	clientMessages := conn.getClientMessages()
	if len(clientMessages) == 0 {
		t.Fatal("expected initial session.update client event")
	}
	var firstMessage map[string]json.RawMessage
	if err := json.Unmarshal(clientMessages[0], &firstMessage); err != nil {
		t.Fatalf("unmarshal session.update: %v", err)
	}
	var sessionPayload map[string]any
	if err := json.Unmarshal(firstMessage["session"], &sessionPayload); err != nil {
		t.Fatalf("unmarshal session payload: %v", err)
	}
	assertStringField(t, sessionPayload, "input_audio_format", "pcm16")
	assertStringField(t, sessionPayload, "output_audio_format", "g711_alaw")
	assertStringField(t, sessionPayload, "type", realtimeSessionType)
	if _, ok := sessionPayload["audio"]; ok {
		t.Fatal("legacy OpenAI realtime session.update should not send GA nested audio")
	}
}

func TestConnectSession_SendsResponseCreateAfterTextInput(t *testing.T) {
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

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello realtime"),
	}) {
		t.Fatal("Send text delta returned false")
	}

	clientMessages := waitForClientMessages(t, conn, 3)
	gotTypes := make([]string, 0, len(clientMessages))
	for _, payload := range clientMessages {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("unmarshal client event: %v", err)
		}
		gotTypes = append(gotTypes, event.Type)
	}
	wantTypes := []string{
		string(models.SessionEventSessionUpdate),
		"conversation.item.create",
		string(models.SessionEventResponseCreate),
	}
	if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("client event sequence: got %v, want %v", gotTypes, wantTypes)
	}
}

func TestConnectSession_SendsExplicitResponseCreate(t *testing.T) {
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

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}) {
		t.Fatal("Send explicit response request returned false")
	}

	clientMessages := waitForClientMessages(t, conn, 2)
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(clientMessages[1], &event); err != nil {
		t.Fatalf("unmarshal response event: %v", err)
	}
	if event.Type != string(models.SessionEventResponseCreate) {
		t.Fatalf("event type = %q, want %q", event.Type, models.SessionEventResponseCreate)
	}
}

func TestConnectSession_VADObservationsDoNotCreateAnOutboundTurnBoundary(t *testing.T) {
	conn := newMockWebSocketConn()
	conn.addServerEvent("input_audio_buffer.speech_started", nil)
	conn.addServerEvent("input_audio_buffer.speech_stopped", nil)
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

	for i, want := range []messages.StreamMessageType{messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped} {
		got, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("timed out waiting for VAD observation %d", i)
		}
		if got.Type != want {
			t.Fatalf("VAD observation %d: got %q, want %q", i, got.Type, want)
		}
	}

	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0, 0, 0, 0}),
	}) {
		t.Fatal("Send audio returned false")
	}
	if !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("Send message end returned false")
	}

	clientMessages := waitForClientMessages(t, conn, 4)
	gotTypes := make([]string, 0, len(clientMessages))
	for _, payload := range clientMessages {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("unmarshal client event: %v", err)
		}
		gotTypes = append(gotTypes, event.Type)
	}
	wantTypes := []string{
		string(models.SessionEventSessionUpdate),
		string(models.SessionEventInputAudioBufferAppend),
		string(models.SessionEventInputAudioBufferCommit),
		string(models.SessionEventResponseCreate),
	}
	if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("client event sequence: got %v, want %v", gotTypes, wantTypes)
	}
}

func TestConnectSession_MissingAPIKeyFailsBeforeDial(t *testing.T) {
	dialer := &mockWebSocketDialer{conn: newMockWebSocketConn()}
	provider := New(
		WithBaseURL("wss://mock.openai.test/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "gpt-realtime"})
	if err == nil {
		t.Fatal("expected missing API key error")
	}
	if !strings.Contains(err.Error(), "api key is required") {
		t.Errorf("error: got %v", err)
	}
	if dialer.capturedURL != "" {
		t.Errorf("dial should not run before API key validation; got URL %q", dialer.capturedURL)
	}
}

func TestConnectSession_MissingDialerFailsBeforeDial(t *testing.T) {
	provider := New(
		WithAPIKey("test-key"),
		WithRealtimeBaseURL("wss://mock.openai.test/v1/realtime"),
	)

	_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "gpt-realtime"})
	if err == nil {
		t.Fatal("expected missing dialer error")
	}
	if !strings.Contains(err.Error(), "websocket dialer is required") {
		t.Fatalf("expected missing dialer error, got: %v", err)
	}
}

func assertStringField(t *testing.T, fields map[string]any, key string, want string) {
	t.Helper()
	if got := fields[key]; got != want {
		t.Errorf("%s: got %v, want %q", key, got, want)
	}
}

func assertStringSliceField(t *testing.T, fields map[string]any, key string, want []string) {
	t.Helper()
	got, ok := fields[key].([]any)
	if !ok {
		t.Fatalf("%s: got %T, want []any", key, fields[key])
	}
	if len(got) != len(want) {
		t.Fatalf("%s length: got %d, want %d", key, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %v, want %q", key, i, got[i], want[i])
		}
	}
}

const realtimeTestSafetyTimeout = 10 * time.Second

func newRealtimeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realtimeTestSafetyTimeout)
	t.Cleanup(cancel)
	return ctx
}

func waitForClientMessages(t *testing.T, conn *mockWebSocketConn, want int, phase ...string) [][]byte {
	t.Helper()
	label := fmt.Sprintf("%d client messages", want)
	if len(phase) > 0 && phase[0] != "" {
		label = phase[0]
	}
	timer := time.NewTimer(realtimeTestSafetyTimeout)
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
			t.Fatalf("timed out waiting for %s after %s: got %d client messages", label, realtimeTestSafetyTimeout, len(messages))
		}
	}
}

func waitForClientMessage(t *testing.T, ctx context.Context, conn *mockWebSocketConn, phase ...string) []byte {
	t.Helper()
	label := "client message"
	if len(phase) > 0 && phase[0] != "" {
		label = phase[0]
	}
	waitContext, cancel := context.WithTimeout(ctx, realtimeTestSafetyTimeout)
	defer cancel()
	select {
	case data := <-conn.clientMessageCh:
		return data
	case <-waitContext.Done():
		t.Fatalf("timed out waiting for %s: %v; observed %d client messages", label, waitContext.Err(), len(conn.getClientMessages()))
		return nil
	}
}

func readRealtimeMessage(t *testing.T, session messages.Session, ctx context.Context, phase string) messages.StreamMessage {
	t.Helper()
	message, err := session.Receive().ReadContext(ctx)
	if err != nil {
		t.Fatalf("waiting for %s failed: %v; %s", phase, err, realtimeSessionDiagnostics(session))
	}
	return message
}

func realtimeSessionDiagnostics(session messages.Session) string {
	done := false
	select {
	case <-session.Done():
		done = true
	default:
	}
	terminalError := "<unavailable>"
	if providerSession, ok := session.(interface{ TerminalError() error }); ok {
		terminalError = fmt.Sprintf("%v", providerSession.TerminalError())
	}
	drops := "<unavailable>"
	if counters, ok := session.(messages.SessionDropCounters); ok {
		drops = fmt.Sprintf("input=%d output=%d", counters.InputDrops(), counters.OutputDrops())
	}
	return fmt.Sprintf("session_done=%t receive_buffer=%d drops=%s terminal_error=%s", done, session.Receive().Len(), drops, terminalError)
}

func TestConnectSession_InvalidRealtimeEndpointFailsBeforeDial(t *testing.T) {
	dialer := &mockWebSocketDialer{conn: newMockWebSocketConn()}
	provider := New(
		WithAPIKey("test-key"),
		WithBaseURL("https://api.openai.com/v1/realtime"),
		WithWebSocketDialer(dialer),
	)

	_, err := provider.ConnectSession(context.Background(), models.SessionConfig{Model: "gpt-realtime"})
	if err == nil {
		t.Fatal("expected invalid endpoint error")
	}
	if !strings.Contains(err.Error(), "invalid endpoint scheme") {
		t.Errorf("error: got %v", err)
	}
	if dialer.capturedURL != "" {
		t.Errorf("dial should not run before endpoint validation; got URL %q", dialer.capturedURL)
	}
}
