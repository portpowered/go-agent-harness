package openaichatgpt

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// defaultInstructions is sent when the conversation has no system prompt.
// OpenClaw sends the same fallback because the backend requires instructions.
const defaultInstructions = "You are a helpful assistant."

// includeEncryptedReasoning keeps reasoning across turns without server-side
// storage (store is always false on this backend).
const includeEncryptedReasoning = "reasoning.encrypted_content"

const (
	itemTypeMessage            = "message"
	itemTypeFunctionCall       = "function_call"
	itemTypeFunctionCallOutput = "function_call_output"
	itemTypeReasoning          = "reasoning"
	partTypeInputText          = "input_text"
	partTypeInputImage         = "input_image"
	partTypeOutputText         = "output_text"
	partTypeRefusal            = "refusal"
	roleUser                   = "user"
	roleAssistant              = "assistant"
	statusCompleted            = "completed"
	toolChoiceAuto             = "auto"
	reasoningSummaryAuto       = "auto"
	imageDetailAuto            = "auto"
	defaultImageMediaType      = "image/png"
)

// responsesRequest is the Responses body the ChatGPT Codex backend accepts.
// The field set is the intersection of Codex's ResponsesApiRequest
// (codex-rs/codex-api/src/common.rs) and OpenClaw's buildRequestBody
// (packages/ai/src/providers/openai-chatgpt-responses.ts).
type responsesRequest struct {
	Model             string         `json:"model"`
	Instructions      string         `json:"instructions"`
	Input             []inputItem    `json:"input"`
	Tools             []functionTool `json:"tools,omitempty"`
	ToolChoice        string         `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	Reasoning         *reasoning     `json:"reasoning,omitempty"`
	Store             bool           `json:"store"`
	Stream            bool           `json:"stream"`
	Include           []string       `json:"include"`
	PromptCacheKey    string         `json:"prompt_cache_key,omitempty"`
}

type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// inputItem is one Responses input item: a message, a function call the
// model made earlier, or a function call's output.
type inputItem struct {
	Type      string        `json:"type"`
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	Status    string        `json:"status,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments *string       `json:"arguments,omitempty"`
	Output    *string       `json:"output,omitempty"`
}

type contentPart struct {
	Type        string    `json:"type"`
	Text        *string   `json:"text,omitempty"`
	ImageURL    string    `json:"image_url,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Annotations *[]string `json:"annotations,omitempty"`
}

type functionTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

// requestOptions are the per-provider values a request needs besides the
// gateway request itself.
type requestOptions struct {
	model           string
	reasoningEffort string
	sessionID       string
}

// requestConfig is the provider-specific InferenceRequest.Config this
// provider reads (for example `yui ask --model-config
// '{"reasoning_effort":"high"}'`). Other keys are ignored.
type requestConfig struct {
	ReasoningEffort string `json:"reasoning_effort"`
}

// marshalRequest builds and encodes the Responses body for req.
func marshalRequest(req providers.InferenceRequest, model, sessionID string) ([]byte, error) {
	var config requestConfig
	if len(bytes.TrimSpace(req.Config)) > 0 {
		if err := json.Unmarshal(req.Config, &config); err != nil {
			return nil, providers.NewInvalidRequestError(ProviderName, "config", ProviderName+": model config must be a JSON object")
		}
	}
	body, err := json.Marshal(buildRequest(req, requestOptions{model: model, reasoningEffort: strings.TrimSpace(config.ReasoningEffort), sessionID: sessionID}))
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", ProviderName, err)
	}
	return body, nil
}

func buildRequest(req providers.InferenceRequest, opts requestOptions) responsesRequest {
	instructions, input := conversationToInput(req.Messages)
	if instructions == "" {
		instructions = defaultInstructions
	}
	body := responsesRequest{
		Model:          opts.model,
		Instructions:   instructions,
		Input:          input,
		Store:          false,
		Stream:         true,
		Include:        []string{includeEncryptedReasoning},
		PromptCacheKey: opts.sessionID,
	}
	if len(req.Tools) > 0 {
		parallel := true
		body.Tools = toolsToFunctions(req.Tools)
		body.ToolChoice = toolChoiceAuto
		body.ParallelToolCalls = &parallel
	}
	if opts.reasoningEffort != "" {
		body.Reasoning = &reasoning{Effort: opts.reasoningEffort, Summary: reasoningSummaryAuto}
	}
	return body
}

// conversationToInput joins the system messages into instructions and maps
// every other message to Responses input items.
func conversationToInput(msgs []models.Message) (string, []inputItem) {
	var instructions []string
	input := make([]inputItem, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleSystem:
			if text := msg.TextContent(); text != "" {
				instructions = append(instructions, text)
			}
		case models.RoleUser:
			input = append(input, userItem(msg))
		case models.RoleAssistant:
			input = append(input, assistantItems(msg)...)
		case models.RoleTool:
			output := msg.TextContent()
			input = append(input, inputItem{Type: itemTypeFunctionCallOutput, CallID: msg.ToolCallID, Output: &output})
		}
	}
	return strings.Join(instructions, "\n\n"), input
}

func userItem(msg models.Message) inputItem {
	parts := make([]contentPart, 0, len(msg.ContentParts)+1)
	for _, part := range msg.ContentParts {
		switch v := part.(type) {
		case models.TextPart:
			text := v.Text
			parts = append(parts, contentPart{Type: partTypeInputText, Text: &text})
		case models.ImagePart:
			if url := imageURL(v); url != "" {
				parts = append(parts, contentPart{Type: partTypeInputImage, ImageURL: url, Detail: imageDetailAuto})
			}
		}
	}
	if len(parts) == 0 {
		text := msg.TextContent()
		parts = append(parts, contentPart{Type: partTypeInputText, Text: &text})
	}
	return inputItem{Type: itemTypeMessage, Role: roleUser, Content: parts}
}

func imageURL(part models.ImagePart) string {
	if len(part.Bytes) == 0 {
		return part.URL
	}
	mediaType := part.MediaType
	if mediaType == "" {
		mediaType = defaultImageMediaType
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Bytes)
}

// assistantItems replays an earlier model turn: its text as a completed
// assistant message, then each tool call as a function_call item.
func assistantItems(msg models.Message) []inputItem {
	items := make([]inputItem, 0, len(msg.ToolCalls)+1)
	if text := msg.TextContent(); text != "" {
		annotations := []string{}
		items = append(items, inputItem{
			Type:    itemTypeMessage,
			Role:    roleAssistant,
			Content: []contentPart{{Type: partTypeOutputText, Text: &text, Annotations: &annotations}},
			Status:  statusCompleted,
		})
	}
	for _, call := range msg.ToolCalls {
		arguments := call.Arguments
		if arguments == "" {
			arguments = "{}"
		}
		items = append(items, inputItem{Type: itemTypeFunctionCall, CallID: call.ID, Name: call.Name, Arguments: &arguments})
	}
	return items
}

// toolsToFunctions maps tool definitions to Responses function tools with
// strict false, which OpenClaw sends explicitly so the backend does not turn
// optional properties into required ones.
func toolsToFunctions(tools []models.ToolDefinition) []functionTool {
	tools = messages.CanonicalToolDefinitions(tools)
	out := make([]functionTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, functionTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  toolParameters(tool),
			Strict:      false,
		})
	}
	return out
}

// toolParameters returns the tool's complete JSON Schema when it has one and
// otherwise builds an object schema from the flat parameter list.
func toolParameters(tool models.ToolDefinition) map[string]any {
	if schema, ok := decodeObjectSchema(tool.ParameterSchema); ok {
		return schema
	}
	properties := map[string]any{}
	required := []string{}
	for _, param := range tool.Parameters {
		properties[param.Name] = map[string]any{"type": param.Type, "description": param.Description}
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	if tool.ParametersClosed {
		schema["additionalProperties"] = false
	}
	return schema
}

func decodeObjectSchema(raw json.RawMessage) (map[string]any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil || schema == nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return schema, true
}
