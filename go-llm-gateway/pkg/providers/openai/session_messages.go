package openai

// This file owns the complete multimodal-message send path for OpenAI Realtime,
// serializing one ordered conversation.item.create followed by response.create.
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
	events, ok := realtimeCompleteMessageEvents(msg)
	if !ok {
		return false
	}
	return s.sendEvents(ctx, events).OK()
}

func realtimeCompleteMessageEvents(msg messages.Message) ([]models.SessionEvent, bool) {
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
	return []models.SessionEvent{
		{Type: conversationItemCreateEvent, Data: data},
		models.NewResponseCreateEvent(),
	}, true
}
