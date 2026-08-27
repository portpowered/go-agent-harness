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
	if msg.Role == messages.RoleTool {
		return realtimeToolResultEvents(msg, requestResponse)
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

// realtimeToolResultEvents delivers a complete tool result to an OpenAI
// Realtime session. Text-only results use the native function_call_output
// item. Realtime function_call_output items have no image content variant,
// so a rich result emits the correlated output item followed by a user image
// message containing the original bytes; the response is requested only once
// after both items are queued.
func realtimeToolResultEvents(msg messages.Message, requestResponse bool) ([]models.SessionEvent, bool) {
	if msg.ToolCallID == "" {
		return nil, false
	}

	text := msg.TextContent()
	outputData, err := json.Marshal(map[string]any{
		"item": map[string]any{
			"type":    "function_call_output",
			"call_id": msg.ToolCallID,
			"output":  text,
		},
	})
	if err != nil {
		return nil, false
	}
	events := []models.SessionEvent{{Type: conversationItemCreateEvent, Data: outputData}}

	content := make([]map[string]any, 0, len(msg.ContentParts))
	imageSeen := false
	for _, part := range msg.ContentParts {
		switch part := part.(type) {
		case messages.TextPart:
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		case messages.ImagePart:
			imageSeen = true
			encoded := base64.StdEncoding.EncodeToString(part.Bytes)
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": "data:" + part.MediaType + ";base64," + encoded,
			})
		default:
			return nil, false
		}
	}
	if imageSeen {
		imageData, err := json.Marshal(map[string]any{
			"item": map[string]any{
				"type":    "message",
				"role":    string(messages.RoleUser),
				"content": content,
			},
		})
		if err != nil {
			return nil, false
		}
		events = append(events, models.SessionEvent{Type: conversationItemCreateEvent, Data: imageData})
	} else if len(content) == 0 {
		return nil, false
	}

	if requestResponse {
		events = append(events, models.NewResponseCreateEvent())
	}
	return events, true
}
