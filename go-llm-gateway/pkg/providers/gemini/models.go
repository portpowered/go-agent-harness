package gemini

import (
	"encoding/json"

	"google.golang.org/genai"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// messagesToContents converts gateway messages to Gemini Content slices.
// System messages are extracted separately (Gemini uses a dedicated system instruction field).
// User, assistant, and tool messages become Content entries with the appropriate role.
func messagesToContents(msgs []models.Message) (system *genai.Content, contents []*genai.Content) {
	var systemParts []*genai.Part

	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleSystem:
			parts := textPartsFromMessage(msg)
			systemParts = append(systemParts, parts...)

		case models.RoleUser:
			parts := contentPartsToGeminiParts(msg.ContentParts)
			if len(parts) == 0 {
				text := msg.TextContent()
				if text != "" {
					parts = []*genai.Part{{Text: text}}
				}
			}
			if len(parts) > 0 {
				contents = append(contents, &genai.Content{Role: "user", Parts: parts})
			}

		case models.RoleAssistant:
			parts := assistantMessageToParts(msg)
			if len(parts) > 0 {
				contents = append(contents, &genai.Content{Role: "model", Parts: parts})
			}

		case models.RoleTool:
			part := toolMessageToPart(msg)
			if part != nil {
				contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{part}})
			}
		}
	}

	if len(systemParts) > 0 {
		system = &genai.Content{Parts: systemParts}
	}
	return system, contents
}

// textPartsFromMessage extracts text from a message as Gemini parts.
func textPartsFromMessage(msg models.Message) []*genai.Part {
	if len(msg.ContentParts) > 0 {
		var parts []*genai.Part
		for _, p := range msg.ContentParts {
			if tp, ok := p.(models.TextPart); ok && tp.Text != "" {
				parts = append(parts, &genai.Part{Text: tp.Text})
			}
		}
		return parts
	}
	text := msg.TextContent()
	if text != "" {
		return []*genai.Part{{Text: text}}
	}
	return nil
}

// contentPartsToGeminiParts converts gateway ContentParts to Gemini Part slices.
// Supports text, image (inline bytes or URL not supported natively), and audio content.
func contentPartsToGeminiParts(parts []models.ContentPart) []*genai.Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]*genai.Part, 0, len(parts))
	for _, p := range parts {
		switch v := p.(type) {
		case models.TextPart:
			if v.Text != "" {
				out = append(out, &genai.Part{Text: v.Text})
			}
		case models.ImagePart:
			if len(v.Bytes) > 0 {
				mediaType := v.MediaType
				if mediaType == "" {
					mediaType = "image/png"
				}
				out = append(out, &genai.Part{
					InlineData: &genai.Blob{Data: v.Bytes, MIMEType: mediaType},
				})
			}
		case models.AudioPart:
			if len(v.Bytes) > 0 {
				mediaType := v.MediaType
				if mediaType == "" {
					mediaType = "audio/wav"
				}
				out = append(out, &genai.Part{
					InlineData: &genai.Blob{Data: v.Bytes, MIMEType: mediaType},
				})
			}
		}
	}
	return out
}

// assistantMessageToParts converts a gateway assistant message to Gemini parts.
// Text content and tool calls are both represented as parts.
func assistantMessageToParts(msg models.Message) []*genai.Part {
	var parts []*genai.Part

	// Text content
	if len(msg.ContentParts) > 0 {
		for _, p := range msg.ContentParts {
			if tp, ok := p.(models.TextPart); ok && tp.Text != "" {
				parts = append(parts, &genai.Part{Text: tp.Text})
			}
		}
	} else if msg.TextContent() != "" {
		parts = append(parts, &genai.Part{Text: msg.TextContent()})
	}

	// Tool calls become FunctionCall parts
	for _, tc := range msg.ToolCalls {
		args := map[string]any{}
		if tc.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				Name: tc.Name,
				Args: args,
			},
		})
	}
	return parts
}

// toolMessageToPart converts a gateway tool result message to a Gemini FunctionResponse part.
func toolMessageToPart(msg models.Message) *genai.Part {
	response := map[string]any{"result": msg.TextContent()}
	return &genai.Part{
		FunctionResponse: &genai.FunctionResponse{
			Name:     msg.Name,
			Response: response,
		},
	}
}

// toolsToGeminiTools converts gateway tool definitions to Gemini Tool with FunctionDeclarations.
func toolsToGeminiTools(tools []models.ToolDefinition) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
		}
		if schema, ok := completeToolParameterJSONSchema(tool.ParameterSchema); ok {
			decl.ParametersJsonSchema = schema
		} else {
			decl.Parameters = toolParametersToSchema(tool.Parameters)
		}
		decls = append(decls, decl)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

func completeToolParameterJSONSchema(raw json.RawMessage) (map[string]any, bool) {
	var schema map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &schema) != nil || schema == nil {
		return nil, false
	}
	return schema, true
}

// toolParametersToSchema converts gateway ToolParameters to a Gemini Schema.
func toolParametersToSchema(params []models.ToolParameter) *genai.Schema {
	if len(params) == 0 {
		return &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{},
		}
	}
	properties := map[string]*genai.Schema{}
	var required []string
	for _, p := range params {
		properties[p.Name] = &genai.Schema{
			Type:        mapParamType(p.Type),
			Description: p.Description,
		}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return &genai.Schema{
		Type:       genai.TypeObject,
		Properties: properties,
		Required:   required,
	}
}

// mapParamType maps gateway parameter type strings to Gemini schema types.
func mapParamType(t string) genai.Type {
	switch t {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	default:
		return genai.TypeString
	}
}

// responseToMessage converts a Gemini GenerateContentResponse to a gateway message.
func responseToMessage(resp *genai.GenerateContentResponse) models.Message {
	result := models.Message{
		Role: models.RoleAssistant,
	}
	if resp == nil || len(resp.Candidates) == 0 {
		return result
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil {
		return result
	}

	var contentParts []models.ContentPart
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			contentParts = append(contentParts, models.TextPart{Text: part.Text})
		}
		if part.FunctionCall != nil {
			argsJSON, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				argsJSON = []byte("{}")
			}
			result.ToolCalls = append(result.ToolCalls, models.ToolCall{
				ID:        part.FunctionCall.ID,
				Name:      part.FunctionCall.Name,
				Arguments: string(argsJSON),
			})
		}
	}
	if len(contentParts) > 0 {
		result.ContentParts = contentParts
	}
	return result
}

// responseUsage extracts token usage from a Gemini response.
func responseUsage(resp *genai.GenerateContentResponse) models.TokenUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return models.TokenUsage{}
	}
	u := resp.UsageMetadata
	prompt := u.PromptTokenCount
	completion := u.CandidatesTokenCount
	total := prompt + completion
	return models.TokenUsage{
		PromptTokens:     int(prompt),
		CompletionTokens: int(completion),
		TotalTokens:      int(total),
	}
}

// buildConfig constructs a GenerateContentConfig from the inference request.
func buildConfig(req providers.InferenceRequest, system *genai.Content) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{}

	if system != nil {
		config.SystemInstruction = system
	}

	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(*req.MaxTokens)
	}
	if req.Temperature != nil {
		v := float32(*req.Temperature)
		config.Temperature = &v
	}
	if len(req.StopSequences) > 0 {
		config.StopSequences = req.StopSequences
	}
	if len(req.Tools) > 0 {
		config.Tools = toolsToGeminiTools(req.Tools)
	}

	return config
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
