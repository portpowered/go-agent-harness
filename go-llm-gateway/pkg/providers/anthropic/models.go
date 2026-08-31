package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// messagesToParams converts gateway messages to Anthropic message params.
// System messages are collected into a single system prompt; user/assistant/tool
// are converted to alternating user/assistant turns. Consecutive tool messages
// are merged into one user message with multiple tool_result blocks.
func messagesToParams(msgs []models.Message) (system []anthropic.TextBlockParam, params []anthropic.MessageParam, err error) {
	var toolResultBlocks []anthropic.ContentBlockParamUnion
	flushToolResults := func() {
		if len(toolResultBlocks) > 0 {
			params = append(params, anthropic.NewUserMessage(toolResultBlocks...))
			toolResultBlocks = nil
		}
	}

	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleSystem:
			flushToolResults()
			text := msg.TextContent()
			if len(msg.ContentParts) > 0 {
				text = contentPartsToSingleText(msg.ContentParts)
			}
			if text != "" {
				system = append(system, anthropic.TextBlockParam{Text: text})
			}
		case models.RoleUser:
			flushToolResults()
			blocks := contentPartsToBlocks(msg)
			if len(blocks) == 0 && msg.TextContent() != "" {
				blocks = []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(msg.TextContent())}
			}
			if len(blocks) > 0 {
				params = append(params, anthropic.NewUserMessage(blocks...))
			}
		case models.RoleAssistant:
			flushToolResults()
			blocks, blockErr := assistantMessageToBlocks(msg)
			if blockErr != nil {
				return nil, nil, blockErr
			}
			if len(blocks) > 0 {
				params = append(params, anthropic.NewAssistantMessage(blocks...))
			}
		case models.RoleTool:
			toolResultBlocks = append(toolResultBlocks, anthropic.NewToolResultBlock(msg.ToolCallID, msg.TextContent(), false))
		}
	}
	flushToolResults()
	return system, params, nil
}

// contentPartsToBlocks converts gateway user content parts to Anthropic content blocks.
// Text -> text block; image -> image block (base64); audio -> text block with data URL (Anthropic has no native audio input block).
func contentPartsToBlocks(msg models.Message) []anthropic.ContentBlockParamUnion {
	if len(msg.ContentParts) == 0 {
		return nil
	}
	out := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ContentParts))
	for _, p := range msg.ContentParts {
		switch v := p.(type) {
		case models.TextPart:
			out = append(out, anthropic.NewTextBlock(v.Text))
		case models.ImagePart:
			if v.URL != "" {
				out = append(out, anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: v.URL, Type: constant.URL("url")}))
			} else if len(v.Bytes) > 0 {
				mediaType := v.MediaType
				if mediaType == "" {
					mediaType = "image/png"
				}
				out = append(out, anthropic.NewImageBlockBase64(mediaType, base64.StdEncoding.EncodeToString(v.Bytes)))
			}
		case models.AudioPart:
			// Anthropic Messages API has no native audio input; pass as data URL in text so model can reference.
			if len(v.Bytes) > 0 {
				mediaType := v.MediaType
				if mediaType == "" {
					mediaType = "audio/wav"
				}
				dataURL := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
				out = append(out, anthropic.NewTextBlock("[Audio input: "+dataURL+"]"))
			} else if v.URL != "" {
				out = append(out, anthropic.NewTextBlock("[Audio URL: "+v.URL+"]"))
			}
		}
	}
	return out
}

func contentPartsToSingleText(parts []models.ContentPart) string {
	var s string
	for _, p := range parts {
		if tp, ok := p.(models.TextPart); ok {
			s += tp.Text
		}
	}
	return s
}

// assistantMessageToBlocks converts a gateway assistant message to Anthropic content blocks (text + tool_use).
func assistantMessageToBlocks(msg models.Message) ([]anthropic.ContentBlockParamUnion, error) {
	var blocks []anthropic.ContentBlockParamUnion
	if len(msg.ContentParts) > 0 {
		for _, p := range msg.ContentParts {
			if tp, ok := p.(models.TextPart); ok && tp.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(tp.Text))
			}
		}
	} else if msg.TextContent() != "" {
		blocks = append(blocks, anthropic.NewTextBlock(msg.TextContent()))
	}
	for _, tc := range msg.ToolCalls {
		// ToolUseBlockParam expects input as any (JSON object). Arguments is JSON string.
		var input any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool call arguments for %q: %w (raw: %s)", tc.Name, err, tc.Arguments)
			}
		}
		if input == nil {
			input = map[string]any{}
		}
		blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
	}
	return blocks, nil
}

// toolsToParams converts gateway tool definitions to Anthropic tool params.
func toolsToParams(tools []models.ToolDefinition) []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		schema := mapToolToInputSchema(tool)
		toolParam := &anthropic.ToolParam{
			Name:        tool.Name,
			InputSchema: schema,
		}
		if tool.Description != "" {
			toolParam.Description = anthropic.String(tool.Description)
		}
		params = append(params, anthropic.ToolUnionParam{OfTool: toolParam})
	}
	return params
}

func mapToolToInputSchema(tool models.ToolDefinition) anthropic.ToolInputSchemaParam {
	if schema, ok := completeToolInputSchema(tool.ParameterSchema); ok {
		return schema
	}

	properties := map[string]any{}
	required := []string{}
	for _, p := range tool.Parameters {
		properties[p.Name] = map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return anthropic.ToolInputSchemaParam{
		Type:       constant.Object("object"),
		Properties: properties,
		Required:   required,
	}
}

// completeToolInputSchema retains nested JSON Schema fields for dynamic page
// tools. The Anthropic SDK exposes root-level extension fields through
// ExtraFields while Properties keeps the nested property tree intact.
func completeToolInputSchema(raw json.RawMessage) (anthropic.ToolInputSchemaParam, bool) {
	var parsed struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &parsed) != nil || (parsed.Type != "" && parsed.Type != "object") {
		return anthropic.ToolInputSchemaParam{}, false
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil || all == nil {
		return anthropic.ToolInputSchemaParam{}, false
	}
	delete(all, "type")
	delete(all, "properties")
	delete(all, "required")
	if parsed.Properties == nil {
		parsed.Properties = map[string]any{}
	}
	return anthropic.ToolInputSchemaParam{
		Type:        constant.Object("object"),
		Properties:  parsed.Properties,
		Required:    parsed.Required,
		ExtraFields: all,
	}, true
}

// responseToMessage converts an Anthropic Message to a gateway message.
func responseToMessage(msg anthropic.Message) models.Message {
	result := models.Message{
		Role: models.RoleAssistant,
	}
	var contentParts []models.ContentPart
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			contentParts = append(contentParts, models.TextPart{Text: block.Text})
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, models.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			})
		}
	}
	if len(contentParts) > 0 {
		result.ContentParts = contentParts
	}
	return result
}

// StreamChunksToMessage builds a gateway assistant message from accumulated stream state.
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
