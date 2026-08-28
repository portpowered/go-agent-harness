package openai

// This file owns the multimodal-message send path for OpenAI Realtime,
// serializing one ordered conversation.item.create with an optional response.create.
import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// The OpenAI Realtime session supports both complete-message delivery modes,
// allowing a mixed tool-result batch to request exactly one response.
func (*realtimeSession) SupportsCompleteMessages() bool { return true }

func (*realtimeSession) SupportsCompleteMessagesWithoutResponse() bool { return true }

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
	if msg.Role == "" {
		return nil, false
	}
	if msg.Role == messages.RoleTool {
		return realtimeToolResultEvents(msg, requestResponse)
	}
	if len(msg.ContentParts) == 0 {
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
	imageParts := make([]messages.ImagePart, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		switch part := part.(type) {
		case messages.TextPart:
			// The text is carried by function_call_output. It is deliberately
			// not duplicated in the separate user-role vision item below.
		case messages.ImagePart:
			imageParts = append(imageParts, part)
		default:
			return nil, false
		}
	}
	// Keep older image-producing tools usable while making an empty output
	// impossible: when a rich result has no text, derive the same compact
	// envelope from its one owned image snapshot. read_image normally supplies
	// this envelope itself, which also retains any tool-specific error detail.
	if text == "" && len(imageParts) == 1 {
		text = fallbackRealtimeImageResult(imageParts[0])
	}
	if len(imageParts) > 0 {
		// Realtime has no image-bearing function_call_output variant. The
		// metadata envelope and the typed projection must therefore be a
		// single internally consistent pair before either wire item is queued.
		if strings.TrimSpace(text) == "" || len(imageParts) != 1 || !validRealtimeImageResult(text, imageParts[0]) {
			return nil, false
		}
	}
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

	imageContent := make([]map[string]any, 0, len(msg.ContentParts))
	for _, part := range imageParts {
		mediaType, ok := normalizedRealtimeImageMIME(part.MediaType)
		if !ok {
			return nil, false
		}
		encoded := base64.StdEncoding.EncodeToString(part.Bytes)
		imageContent = append(imageContent, map[string]any{
			"type":      "input_image",
			"image_url": "data:" + mediaType + ";base64," + encoded,
		})
	}
	if len(imageParts) > 0 {
		imageData, err := json.Marshal(map[string]any{
			"item": map[string]any{
				"type":    "message",
				"role":    string(messages.RoleUser),
				"id":      realtimeToolImageItemID(msg.ToolCallID),
				"content": imageContent,
			},
		})
		if err != nil {
			return nil, false
		}
		events = append(events, models.SessionEvent{Type: conversationItemCreateEvent, Data: imageData})
	}

	if requestResponse {
		events = append(events, models.NewResponseCreateEvent())
	}
	return events, true
}

const (
	realtimeToolImageItemIDPrefix      = "item_tool_result_"
	realtimeToolImageItemIDDigestBytes = 11
	realtimeImageResultVersion         = 2
	realtimeImageResultStatusSuccess   = "success"
	realtimeImageTypedProjection       = "input_image"
	realtimeImageEnvelopeMaxBytes      = 1024
)

// realtimeToolImageItemID uses the documented client-supplied ID on a
// Realtime user message to correlate a typed image projection with the
// function_call_output that carries the same tool result. The provider's user
// message schema does not define an extensible metadata field. Call IDs are
// opaque provider values, so use a fixed-size URL-safe SHA-256 encoding rather
// than copying their length or characters into the provider-facing ID. The
// 17-character prefix leaves room for 11 digest bytes: ceil(11 * 8 / 6) = 15;
// 17 + 15 = 32. Encoding 12 bytes would require 16 characters and exceed 32.
func realtimeToolImageItemID(toolCallID string) string {
	digest := sha256.Sum256([]byte(toolCallID))
	return realtimeToolImageItemIDPrefix + base64.RawURLEncoding.EncodeToString(digest[:realtimeToolImageItemIDDigestBytes])
}

// realtimeImageResultEnvelope is intentionally local to the gateway. The CLI
// tool package owns the producer type, while the provider boundary only needs
// to validate the stable wire contract without importing that package.
type realtimeImageResultEnvelope struct {
	Version         int    `json:"version"`
	Status          string `json:"status"`
	MIMEType        string `json:"mime_type"`
	ByteLength      int    `json:"byte_length"`
	SHA256          string `json:"sha256"`
	TypedProjection string `json:"typed_projection"`
}

// fallbackRealtimeImageResult keeps the provider boundary lossless for older
// rich tools that have not yet added a textual result envelope. It never reads
// a path or embeds pixels in text; it hashes only the immutable bytes already
// in msg.
func fallbackRealtimeImageResult(part messages.ImagePart) string {
	mediaType, ok := normalizedRealtimeImageMIME(part.MediaType)
	if !ok || len(part.Bytes) == 0 {
		return ""
	}
	digest := sha256.Sum256(part.Bytes)
	result := realtimeImageResultEnvelope{
		Version:         realtimeImageResultVersion,
		Status:          realtimeImageResultStatusSuccess,
		MIMEType:        mediaType,
		ByteLength:      len(part.Bytes),
		SHA256:          hex.EncodeToString(digest[:]),
		TypedProjection: realtimeImageTypedProjection,
	}
	encoded, _ := json.Marshal(result)
	return string(encoded)
}

// validRealtimeImageResult verifies the only rich tool-result shape that this
// boundary can safely project. Keeping this check before event construction
// prevents an inconsistent function output from being queued before the typed
// image item or the continuation request.
func validRealtimeImageResult(encoded string, part messages.ImagePart) bool {
	if len(encoded) == 0 || len(encoded) > realtimeImageEnvelopeMaxBytes || len(part.Bytes) == 0 {
		return false
	}

	var result realtimeImageResultEnvelope
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}

	mediaType, ok := normalizedRealtimeImageMIME(part.MediaType)
	if !ok {
		return false
	}
	digest := sha256.Sum256(part.Bytes)
	return result.Version == realtimeImageResultVersion &&
		result.Status == realtimeImageResultStatusSuccess &&
		result.MIMEType == mediaType &&
		result.ByteLength == len(part.Bytes) &&
		result.SHA256 == hex.EncodeToString(digest[:]) &&
		result.TypedProjection == realtimeImageTypedProjection
}

func normalizedRealtimeImageMIME(raw string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType, mediaType != "" && strings.HasPrefix(mediaType, "image/")
}
