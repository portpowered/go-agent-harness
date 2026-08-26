package openai

// This file owns the multimodal-message send path for OpenAI Realtime,
// serializing one ordered conversation.item.create with an optional response.create.
import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// SendMessage delivers a complete user message (text plus ordered image parts)
// as exactly one conversation.item.create event followed by one response.create.
func (s *realtimeSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	events, ok := realtimeCompleteMessageEvents(msg, true)
	if !ok {
		return false
	}
	return s.sendEvents(ctx, events).OK()
}

// SendMessageWithoutResponse queues a complete user message without starting
// a response. The caller can append audio and use the audio end-of-turn event
// to commit the combined voice+image turn and request one response.
func (s *realtimeSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	events, ok := realtimeCompleteMessageEvents(msg, false)
	if !ok {
		return false
	}
	return s.sendEvents(ctx, events).OK()
}

func realtimeCompleteMessageEvents(msg messages.Message, requestResponse bool) ([]models.SessionEvent, bool) {
	if msg.Role == "" || len(msg.ContentParts) == 0 {
		return nil, false
	}
	content := make([]map[string]any, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		switch part := part.(type) {
		case messages.TextPart:
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		case messages.ImagePart:
			encoded := base64.StdEncoding.EncodeToString(part.Bytes)
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": "data:" + part.MediaType + ";base64," + encoded,
			})
		default:
			return nil, false
		}
	}
	data, err := json.Marshal(map[string]any{
		"item": map[string]any{
			"type":    "message",
			"role":    string(msg.Role),
			"content": content,
		},
	})
	if err != nil {
		return nil, false
	}
	events := []models.SessionEvent{{Type: conversationItemCreateEvent, Data: data}}
	if requestResponse {
		events = append(events, models.NewResponseCreateEvent())
	}
	return events, true
}
