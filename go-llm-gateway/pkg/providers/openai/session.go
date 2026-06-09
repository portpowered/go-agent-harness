package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

const conversationItemCreateEvent = models.SessionEventType("conversation.item.create")

var _ messages.Session = (*realtimeSession)(nil)

// ConnectSession establishes an OpenAI Realtime WebSocket session through the
// provider-agnostic session gateway contract.
func (p *OpenAIProvider) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, fmt.Errorf("openai realtime: api key is required")
	}

	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = p.model
	}
	endpoint, err := p.realtimeURL(model)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	if p.realtimeDialer == nil {
		return nil, fmt.Errorf("openai realtime: websocket dialer is required")
	}

	conn, err := p.realtimeDialer.Dial(endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("openai realtime: dial websocket %s: %w", safeEndpointForError(endpoint), err)
	}

	p.logger.Info("openai realtime: websocket connected", logging.Field{Key: "endpoint", Value: safeEndpointForError(endpoint)})

	session := newRealtimeSession(conn, p.logger)
	sessionUpdate, err := p.buildRealtimeSessionUpdate(config, model)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("openai realtime: build session update for %s: %w", safeEndpointForError(endpoint), err)
	}
	if err := session.writeEvent(sessionUpdate); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("openai realtime: send session update to %s: %w", safeEndpointForError(endpoint), err)
	}

	session.start(ctx)
	return session, nil
}

func (p *OpenAIProvider) realtimeURL(model string) (string, error) {
	base := strings.TrimSpace(p.realtimeBaseURL)
	if base == "" {
		base = strings.TrimSpace(p.baseURL)
	}
	if base == "" || base == defaultBaseURL {
		base = defaultRealtimeBaseURL
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("openai realtime: invalid endpoint: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", fmt.Errorf("openai realtime: invalid endpoint scheme %q for %s", parsed.Scheme, safeEndpointForError(base))
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("openai realtime: invalid endpoint host for %s", safeEndpointForError(base))
	}

	query := parsed.Query()
	if query.Get("model") == "" && model != "" {
		query.Set("model", model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func safeEndpointForError(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "<invalid>"
	}
	parsed.User = nil
	query := parsed.Query()
	query.Del("key")
	query.Del("api_key")
	query.Del("access_token")
	query.Del("token")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (p *OpenAIProvider) buildRealtimeSessionUpdate(config models.SessionConfig, model string) (models.SessionEvent, error) {
	if p.realtimeLegacySessionUpdate {
		return buildLegacyRealtimeSessionUpdate(config, model)
	}

	update := map[string]any{
		"type":  "realtime",
		"model": model,
	}
	if len(config.Modalities) > 0 {
		update["output_modalities"] = sessionModalitiesToStrings(config.Modalities)
	}
	if config.Instructions != "" {
		update["instructions"] = config.Instructions
	}
	audio := buildRealtimeAudioConfig(config)
	if len(audio) > 0 {
		update["audio"] = audio
	}
	if len(config.Tools) > 0 {
		update["tools"] = realtimeToolsToParams(config.Tools)
	}

	data, err := json.Marshal(map[string]any{"session": update})
	if err != nil {
		return models.SessionEvent{}, fmt.Errorf("marshal session update: %w", err)
	}
	return models.NewSessionUpdateEvent(data), nil
}

func buildLegacyRealtimeSessionUpdate(config models.SessionConfig, model string) (models.SessionEvent, error) {
	update := map[string]any{
		"model": model,
	}
	if len(config.Modalities) > 0 {
		update["modalities"] = sessionModalitiesToStrings(config.Modalities)
	}
	if config.Voice != "" {
		update["voice"] = config.Voice
	}
	if config.Instructions != "" {
		update["instructions"] = config.Instructions
	}
	if config.InputAudioFormat != "" {
		update["input_audio_format"] = config.InputAudioFormat
	}
	if config.OutputAudioFormat != "" {
		update["output_audio_format"] = config.OutputAudioFormat
	}
	if config.TurnDetection != nil {
		update["turn_detection"] = config.TurnDetection
	}
	if len(config.Tools) > 0 {
		update["tools"] = config.Tools
	}

	data, err := json.Marshal(map[string]any{"session": update})
	if err != nil {
		return models.SessionEvent{}, fmt.Errorf("marshal session update: %w", err)
	}
	return models.NewSessionUpdateEvent(data), nil
}

func sessionModalitiesToStrings(modalities []models.SessionModality) []string {
	out := make([]string, 0, len(modalities))
	for _, modality := range modalities {
		if modality == "" {
			continue
		}
		out = append(out, string(modality))
	}
	return out
}

func buildRealtimeAudioConfig(config models.SessionConfig) map[string]any {
	audio := map[string]any{}
	input := map[string]any{}
	output := map[string]any{}

	if config.InputAudioFormat != "" || config.InputAudioSampleRate != 0 {
		input["format"] = realtimeAudioFormat(config.InputAudioFormat, config.InputAudioSampleRate)
	}
	if config.TurnDetection != nil {
		input["turn_detection"] = config.TurnDetection
	}
	if len(input) > 0 {
		audio["input"] = input
	}

	if config.OutputAudioFormat != "" || config.OutputAudioSampleRate != 0 {
		output["format"] = realtimeAudioFormat(config.OutputAudioFormat, config.OutputAudioSampleRate)
	}
	if config.Voice != "" {
		output["voice"] = config.Voice
	}
	if len(output) > 0 {
		audio["output"] = output
	}

	return audio
}

func realtimeAudioFormat(format models.AudioFormat, rate models.SampleRate) map[string]any {
	switch format {
	case models.AudioFormatG711Ulaw:
		return map[string]any{"type": "audio/pcmu"}
	case models.AudioFormatG711Alaw:
		return map[string]any{"type": "audio/pcma"}
	default:
		audioFormat := map[string]any{"type": "audio/pcm"}
		if rate != 0 {
			audioFormat["rate"] = rate
		}
		return audioFormat
	}
}

func realtimeToolsToParams(tools []models.ToolDefinition) []map[string]any {
	params := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		params = append(params, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  buildParameters(tool),
		})
	}
	return params
}

type realtimeSession struct {
	conn    WebSocketConn
	logger  logging.Logger
	sendCh  chan models.SessionEvent
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done      chan struct{}
	closeOnce sync.Once
}

var _ messages.SessionSendOutcomeSender = (*realtimeSession)(nil)

func newRealtimeSession(conn WebSocketConn, logger logging.Logger) *realtimeSession {
	return &realtimeSession{
		conn:    conn,
		logger:  logger,
		sendCh:  make(chan models.SessionEvent, 64),
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *realtimeSession) start(ctx context.Context) {
	go s.readLoop(ctx)
	go s.writeLoop(ctx)
}

func (s *realtimeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome writes a StreamMessage to the outbound queue and reports the
// precise public lifecycle outcome.
func (s *realtimeSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	events, ok := realtimeOutboundEvents(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	default:
	}
	for _, event := range events {
		select {
		case <-ctx.Done():
			return sessionSendContextOutcome(ctx)
		case <-s.done:
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
		case s.sendCh <- event:
		default:
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func sessionSendContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *realtimeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

func (s *realtimeSession) Done() <-chan struct{} {
	return s.done
}

func (s *realtimeSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		closeErr = s.conn.Close()
	})
	return closeErr
}

func (s *realtimeSession) readLoop(ctx context.Context) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.logger.Error("openai realtime: websocket read error", logging.Field{Key: "error", Value: err})
			_ = s.Close()
			return
		}

		event, err := parseRealtimeServerEvent(data)
		if err != nil {
			s.logger.Warn("openai realtime: failed to parse server event", logging.Field{Key: "error", Value: err})
			continue
		}
		for _, msg := range realtimeInboundMessages(event) {
			if !s.recvBuf.Write(ctx, msg) {
				select {
				case <-s.done:
					return
				case <-ctx.Done():
					_ = s.Close()
					return
				default:
				}
			}
		}
	}
}

func (s *realtimeSession) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
			return
		case <-s.done:
			return
		case event, ok := <-s.sendCh:
			if !ok {
				return
			}
			if err := s.writeEvent(event); err != nil {
				s.logger.Error("openai realtime: websocket write error", logging.Field{Key: "error", Value: err})
				_ = s.Close()
				return
			}
		}
	}
}

func (s *realtimeSession) writeEvent(event models.SessionEvent) error {
	payload := map[string]json.RawMessage{}
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return fmt.Errorf("unmarshal event payload: %w", err)
		}
	}
	typeBytes, _ := json.Marshal(event.Type)
	payload["type"] = typeBytes
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(1, data)
}

func parseRealtimeServerEvent(raw []byte) (models.SessionEvent, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return models.SessionEvent{}, fmt.Errorf("unmarshal event type: %w", err)
	}
	if envelope.Type == "" {
		return models.SessionEvent{}, fmt.Errorf("event missing type field")
	}
	return models.SessionEvent{Type: models.SessionEventType(envelope.Type), Data: raw}, nil
}

func realtimeInboundMessages(event models.SessionEvent) []messages.StreamMessage {
	switch event.Type {
	case models.SessionEventSessionCreated:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		model := firstStringField(event.Data, "model", "session.model")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(sessionID, "audio_inference")},
			{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(sessionID, model)},
		}
	case models.SessionEventSessionUpdated:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue(sessionID)},
		}
	case models.SessionEventSessionClosed:
		sessionID := firstStringField(event.Data, "session_id", "session.id", "id")
		reason := firstStringField(event.Data, "reason", "session.reason")
		return []messages.StreamMessage{
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue(sessionID, reason)},
		}
	case models.SessionEventResponseCreated:
		return []messages.StreamMessage{{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}}
	case models.SessionEventResponseDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}}
	case models.SessionEventResponseOutputItemAdded:
		itemType := firstStringField(event.Data, "item.type")
		if itemType != "function_call" {
			return nil
		}
		callID := firstStringField(event.Data, "item.call_id", "item.id")
		name := firstStringField(event.Data, "item.name")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallStart,
			ToolCallId: callID,
			Value:      messages.NewToolCallStartValue(callID, name),
		}}
	case models.SessionEventResponseTextDelta:
		text := firstStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}}
	case models.SessionEventResponseTextDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}}
	case models.SessionEventResponseOutputAudioDelta:
		audioBytes := realtimeAudioBytes(event.Data)
		if audioBytes == nil {
			return nil
		}
		return []messages.StreamMessage{{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValueWithMediaType(audioBytes, realtimeAudioMediaType(event.Data)),
		}}
	case models.SessionEventResponseOutputAudioDone:
		return []messages.StreamMessage{{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()}}
	case models.SessionEventResponseOutputAudioTranscriptDelta:
		text := firstStringField(event.Data, "delta")
		if text == "" {
			return nil
		}
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue(text)}}
	case models.SessionEventResponseOutputAudioTranscriptDone:
		text := firstStringField(event.Data, "transcript")
		return []messages.StreamMessage{{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue(text)}}
	case models.SessionEventResponseFunctionCallArgumentsDelta:
		partial := firstStringField(event.Data, "delta")
		if partial == "" {
			return nil
		}
		callID := firstStringField(event.Data, "call_id", "item.call_id", "item_id")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallDelta,
			ToolCallId: callID,
			Value:      messages.NewToolCallDeltaValue(partial),
		}}
	case models.SessionEventResponseFunctionCallArgumentsDone:
		callID := firstStringField(event.Data, "call_id", "item.call_id", "item_id")
		name := firstStringField(event.Data, "name", "item.name")
		args := firstStringField(event.Data, "arguments")
		return []messages.StreamMessage{{
			Type:       messages.StreamTypeToolCallEnd,
			ToolCallId: callID,
			Value:      messages.NewToolCallEndValue(callID, name, args),
		}}
	case models.SessionEventError:
		msg := firstStringField(event.Data, "message", "error.message")
		if msg == "" {
			msg = "session error"
		}
		return []messages.StreamMessage{{
			Type: messages.StreamTypeError,
			Value: messages.NewErrorValueWithDetails(
				msg,
				firstStringField(event.Data, "error.type"),
				firstStringField(event.Data, "error.code"),
				firstStringField(event.Data, "error.param"),
				firstStringField(event.Data, "error.event_id"),
			),
		}}
	default:
		return nil
	}
}

func realtimeOutboundEvents(msg messages.StreamMessage) ([]models.SessionEvent, bool) {
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		v, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || v == nil {
			return nil, false
		}
		return []models.SessionEvent{models.NewAudioBufferAppendEvent(base64.StdEncoding.EncodeToString(v.Content))}, true
	case messages.StreamTypeMessageEnd:
		return []models.SessionEvent{models.NewAudioBufferCommitEvent()}, true
	case messages.StreamTypeTextDelta:
		v, ok := msg.Value.(*messages.TextDeltaValue)
		if !ok || v == nil {
			return nil, false
		}
		data, _ := json.Marshal(map[string]any{
			"item": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": v.Content},
				},
			},
		})
		return []models.SessionEvent{
			{Type: conversationItemCreateEvent, Data: data},
			models.NewResponseCreateEvent(),
		}, true
	default:
		return nil, false
	}
}

func realtimeAudioBytes(data json.RawMessage) []byte {
	encoded := firstStringField(data, "delta")
	if encoded == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	return decoded
}

func realtimeAudioMediaType(data json.RawMessage) string {
	format := firstStringField(data, "format", "format.type", "audio_format", "response.audio.output.format.type", "response.output_audio_format")
	switch format {
	case "pcm16":
		return "audio/pcm"
	case "g711_ulaw":
		return "audio/g711-ulaw"
	case "g711_alaw":
		return "audio/g711-alaw"
	default:
		return format
	}
}

func firstStringField(data json.RawMessage, paths ...string) string {
	for _, path := range paths {
		if value := stringField(data, strings.Split(path, ".")); value != "" {
			return value
		}
	}
	return ""
}

func stringField(data json.RawMessage, path []string) string {
	if len(data) == 0 || len(path) == 0 {
		return ""
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(data, &current); err != nil {
		return ""
	}
	for i, part := range path {
		raw, ok := current[part]
		if !ok {
			return ""
		}
		if i == len(path)-1 {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return ""
			}
			return value
		}
		current = map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &current); err != nil {
			return ""
		}
	}
	return ""
}
