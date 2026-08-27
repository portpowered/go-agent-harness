package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type mockWebSocketConn struct {
	mu sync.Mutex

	serverMessages [][]byte
	readIdx        int
	clientMessages [][]byte

	closed    bool
	readBlock chan struct{}
	writeErr  error
}

func newMockWebSocketConn() *mockWebSocketConn {
	return &mockWebSocketConn{readBlock: make(chan struct{})}
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
	c.clientMessages = append(c.clientMessages, data)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

func TestConnectSession_SurfacesUnexpectedWebSocketReadError(t *testing.T) {
	readErr := errors.New("websocket: close 1008 (policy violation): invalid API key")
	dialer := &readErrorWebSocketDialer{conn: &readErrorWebSocketConn{err: readErr}}
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
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

func waitForClientMessages(t *testing.T, conn *mockWebSocketConn, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages := conn.getClientMessages()
		if len(messages) >= want {
			return messages
		}
		time.Sleep(10 * time.Millisecond)
	}
	messages := conn.getClientMessages()
	t.Fatalf("timed out waiting for %d client messages, got %d", want, len(messages))
	return nil
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
