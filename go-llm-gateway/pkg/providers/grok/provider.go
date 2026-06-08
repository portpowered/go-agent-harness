package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const defaultBaseURL = "https://api.x.ai/v1/realtime"

// GrokSessionProvider implements SessionProvider for the xAI Grok realtime
// WebSocket API. It follows the OpenAI Realtime API conventions for event
// types and message formats.
type GrokSessionProvider struct {
	apiKey  string
	baseURL string
	dialer  WebSocketDialer
	logger  logging.Logger
}

// Ensure GrokSessionProvider satisfies SessionProvider at compile time.
var _ providers.SessionProvider = (*GrokSessionProvider)(nil)

// New creates a new Grok realtime session provider.
func New(opts ...Option) *GrokSessionProvider {
	p := &GrokSessionProvider{
		baseURL: defaultBaseURL,
		logger:  logging.DummyLogger(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *GrokSessionProvider) Name() string { return "grok" }

// ConnectSession establishes a WebSocket connection to the Grok realtime API,
// sends the initial session.update with the provided config, and returns a
// Session wrapping the bidirectional StreamMessage stream.
func (p *GrokSessionProvider) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	url := strings.TrimRight(p.baseURL, "/")

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	if p.dialer == nil {
		return nil, fmt.Errorf("grok: websocket dialer is required")
	}

	conn, err := p.dialer.Dial(url, headers)
	if err != nil {
		return nil, fmt.Errorf("grok: dial websocket: %w", err)
	}

	p.logger.Info("grok: websocket connected", logging.Field{Key: "url", Value: url})

	gs := newGrokSession(conn, p.logger)

	// Send initial session.update with config.
	sessionUpdate, err := buildSessionUpdate(config)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("grok: build session update: %w", err)
	}

	if err := gs.writeEvent(sessionUpdate); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("grok: send session update: %w", err)
	}

	// Start the read/write goroutines.
	gs.start(ctx)

	return gs, nil
}

// buildSessionUpdate constructs the initial session.update event from SessionConfig.
func buildSessionUpdate(config models.SessionConfig) (models.SessionEvent, error) {
	update := map[string]any{
		"model": config.Model,
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

	data, err := json.Marshal(map[string]any{
		"session": update,
	})
	if err != nil {
		return models.SessionEvent{}, fmt.Errorf("marshal session update: %w", err)
	}

	return models.NewSessionUpdateEvent(data), nil
}
