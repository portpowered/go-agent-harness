package openai

import (
	"encoding/base64"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// messagesToParams converts gateway messages to OpenAI request messages.
// All message roles (system, user, assistant, tool) support all content types
// (text, image, audio, video) to allow maximum compatibility with third-party
// APIs such as OpenRouter that extend the standard OpenAI schema.
func messagesToParams(msgs []models.Message, logger logging.Logger) []requestMsg {
	result := make([]requestMsg, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleSystem:
			result = append(result, requestMsg{
				Role:    "system",
				Content: strContent(msg.TextContent()),
			})
		case models.RoleUser:
			result = append(result, userMessageToParam(msg))
		case models.RoleAssistant:
			result = append(result, assistantMessageToParam(msg))
		case models.RoleTool:
			result = append(result, toolMessageToParam(msg, logger))
		}
	}
	return result
}

// userMessageToParam converts a gateway user message to an OpenAI user request message.
// ContentParts are sent as a typed content array; without parts Content is sent as a plain string.
func userMessageToParam(msg models.Message) requestMsg {
	if len(msg.ContentParts) > 0 {
		parts := contentPartsToRequestParts(msg.ContentParts)
		return requestMsg{Role: "user", Content: arrContent(parts)}
	}
	return requestMsg{Role: "user", Content: strContent(msg.TextContent())}
}

// assistantMessageToParam converts a gateway assistant message to an OpenAI assistant request message.
// ContentParts are sent as a typed content array; tool calls are preserved.
func assistantMessageToParam(msg models.Message) requestMsg {
	var toolCalls []toolCall
	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, toolCall{
			ID:   tc.ID,
			Type: "function",
			Function: toolCallFunc{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}

	var content *contentField
	if len(msg.ContentParts) > 0 {
		parts := contentPartsToRequestParts(msg.ContentParts)
		if len(parts) > 0 {
			content = arrContent(parts)
		} else {
			content = strContent(msg.TextContent())
		}
	} else if msg.TextContent() != "" {
		content = strContent(msg.TextContent())
	}

	return requestMsg{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}

// toolMessageToParam converts a gateway tool message to an OpenAI tool request message.
// ContentParts are sent as a typed content array; without parts Content is sent as a plain string.
// Compatible APIs (e.g. OpenRouter) accept image and audio content parts in tool messages.
//
// OpenAI image workaround: The standard OpenAI chat completions API does not support
// image_url content parts in tool-role messages (returns 400). When any content part
// contains an image, the role is changed from "tool" to "user" so the image can be
// delivered. This workaround is required for OpenAI's API and is also accepted by
// compatible providers such as OpenRouter and Groq.
func toolMessageToParam(msg models.Message, logger logging.Logger) requestMsg {
	parts := contentPartsToRequestParts(msg.ContentParts)
	if len(parts) > 0 {
		if hasImagePart(msg.ContentParts) {
			logger.Warn("openai: tool message contains image; changing role from tool to user",
				logging.Field{Key: "tool_call_id", Value: msg.ToolCallID},
			)
			return requestMsg{
				Role:       "user",
				Content:    arrContent(parts),
				ToolCallID: msg.ToolCallID,
			}
		}
		return requestMsg{
			Role:       "tool",
			Content:    arrContent(parts),
			ToolCallID: msg.ToolCallID,
		}
	}
	return requestMsg{
		Role:       "tool",
		Content:    strContent(msg.TextContent()),
		ToolCallID: msg.ToolCallID,
	}
}

// hasImagePart returns true if any content part is an ImagePart.
func hasImagePart(parts []models.ContentPart) bool {
	for _, p := range parts {
		if _, ok := p.(models.ImagePart); ok {
			return true
		}
	}
	return false
}

// contentPartsToRequestParts converts gateway ContentParts to API content parts.
// Supported: text, image (URL or base64 data URL), audio (base64), video (URL or data URL).
// EmbeddingPart is skipped as it has no standard OpenAI-compatible representation.
func contentPartsToRequestParts(parts []models.ContentPart) []contentPart {
	out := make([]contentPart, 0, len(parts))
	for _, p := range parts {
		cp, ok := contentPartToRequestPart(p)
		if ok {
			out = append(out, cp)
		}
	}
	return out
}

// contentPartToRequestPart converts a single gateway ContentPart to an API content part.
// Returns (part, true) on success; (zero, false) when the part should be skipped.
func contentPartToRequestPart(p models.ContentPart) (contentPart, bool) {
	switch v := p.(type) {
	case models.TextPart:
		return contentPart{Type: "text", Text: v.Text}, true

	case models.ImagePart:
		url := v.URL
		if len(v.Bytes) > 0 {
			mt := v.MediaType
			if mt == "" {
				mt = "image/png"
			}
			url = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
		}
		if url == "" {
			return contentPart{}, false
		}
		return contentPart{Type: "image_url", ImageURL: &imageURL{URL: url}}, true

	case models.AudioPart:
		if len(v.Bytes) == 0 {
			return contentPart{}, false
		}
		format := audioFormatFromMediaType(v.MediaType)
		return contentPart{Type: "input_audio", InputAudio: &inputAudio{
			Data:   base64.StdEncoding.EncodeToString(v.Bytes),
			Format: format,
		}}, true

	case models.VideoPart:
		url := v.URL
		if len(v.Bytes) > 0 && url == "" {
			mt := v.MediaType
			if mt == "" {
				mt = "video/mp4"
			}
			url = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
		}
		if url == "" {
			return contentPart{}, false
		}
		return contentPart{Type: "video_url", VideoURL: &videoURL{URL: url}}, true

	default:
		return contentPart{}, false
	}
}

// audioFormatFromMediaType maps gateway MediaType to OpenAI audio format ("wav" or "mp3").
func audioFormatFromMediaType(mediaType string) string {
	switch mediaType {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav":
		return "wav"
	default:
		return "wav"
	}
}

// toolsToParams converts gateway tool definitions to OpenAI tool params.
func toolsToParams(tools []models.ToolDefinition) []chatTool {
	tools = messages.CanonicalToolDefinitions(tools)
	params := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		params = append(params, chatTool{
			Type: "function",
			Function: functionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  buildParameters(tool),
			},
		})
	}
	return params
}

// buildParameters converts tool parameters to OpenAI function parameters (JSON Schema).
func buildParameters(tool models.ToolDefinition) map[string]interface{} {
	if len(tool.Parameters) == 0 {
		parameters := map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
		if tool.ParametersClosed {
			parameters["additionalProperties"] = false
		}
		return parameters
	}

	properties := map[string]interface{}{}
	required := []string{}
	for _, param := range tool.Parameters {
		properties[param.Name] = map[string]interface{}{
			"type":        param.Type,
			"description": param.Description,
		}
		if param.Required {
			required = append(required, param.Name)
		}
	}
	parameters := map[string]interface{}{
		"type":       "object",
		"required":   required,
		"properties": properties,
	}
	if tool.ParametersClosed {
		parameters["additionalProperties"] = false
	}
	return parameters
}

// responseToMessage converts an OpenAI chat completion message to a gateway message.
// Populates ContentParts (text, optional AudioPart from base64-decoded Audio) and ToolCalls.
func responseToMessage(msg responseMsg) models.Message {
	result := models.Message{
		Role:    models.RoleAssistant,
		Refusal: msg.Refusal,
	}
	if msg.Content != "" || (msg.Audio != nil && msg.Audio.Data != "") {
		result.ContentParts = responseToContentParts(msg)
	}
	for _, tc := range msg.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, models.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return result
}

// responseToContentParts builds gateway ContentParts from a non-streaming response message.
// Text from Content, optional AudioPart from Audio (base64 data decoded).
func responseToContentParts(msg responseMsg) []models.ContentPart {
	var parts []models.ContentPart
	if msg.Content != "" {
		parts = append(parts, models.TextPart{Text: msg.Content})
	}
	if msg.Audio != nil && msg.Audio.Data != "" {
		decoded, err := base64.StdEncoding.DecodeString(msg.Audio.Data)
		if err == nil {
			parts = append(parts, models.AudioPart{Bytes: decoded, MediaType: "audio/wav"})
		}
	}
	return parts
}

// StreamChunksToMessage builds a gateway assistant message from accumulated stream state.
// Use after consuming a stream: accumulate delta.Content (and optionally tool calls), then call this
// to get the same message shape as responseToMessage for non-stream responses.
func StreamChunksToMessage(accumulatedContent string, toolCalls []models.ToolCall) models.Message {
	result := models.Message{
		Role: models.RoleAssistant,
	}
	if accumulatedContent != "" {
		result.ContentParts = []models.ContentPart{models.TextPart{Text: accumulatedContent}}
	}
	result.ToolCalls = append(result.ToolCalls, toolCalls...)
	return result
}
