package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// RegistryExecutor adapts a ToolRegistry to the subsystems.ToolExecutor interface
// expected by the agent loop.
type RegistryExecutor struct {
	registry *ToolRegistry
}

var _ SessionImagePreparerBinder = (*RegistryExecutor)(nil)

type ToolArgumentError struct {
	Err error
}

func (e *ToolArgumentError) Error() string {
	return fmt.Sprintf("failed to parse tool arguments: %v", e.Err)
}

func (e *ToolArgumentError) Unwrap() error { return e.Err }

// NewRegistryExecutor creates a ToolExecutor backed by the given ToolRegistry.
func NewRegistryExecutor(registry *ToolRegistry) *RegistryExecutor {
	return &RegistryExecutor{registry: registry}
}

// WithSessionImagePreparer returns a registry executor isolated to one
// session's provider-aware image preparation callback. The original executor
// remains safe for other sessions and one-shot calls.
func (re *RegistryExecutor) WithSessionImagePreparer(preparer ImagePartPreparer) messages.ToolExecutor {
	if re == nil {
		return nil
	}
	return &RegistryExecutor{registry: re.registry.cloneWithSessionImagePreparer(preparer)}
}

// Execute implements subsystems.ToolExecutor.
func (re *RegistryExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	var args map[string]any
	if call.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return messages.ToolCallResponse{}, &ToolArgumentError{Err: err}
		}
	}

	msgs, err := re.registry.ExecuteWithContext(ctx, call.Name, args)
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	return messagesToToolCallResponse(call.ID, msgs), nil
}

// messagesToToolCallResponse merges tool result messages into a single ToolCallResponse
// (one per tool call), preserving text and ContentParts for streaming and multimodal content.
func messagesToToolCallResponse(toolCallID string, msgs []messages.Message) messages.ToolCallResponse {
	var content strings.Builder
	var contentParts []messages.ContentPart
	for _, m := range msgs {
		for _, part := range m.ContentParts {
			if textPart, ok := part.(messages.TextPart); ok {
				content.WriteString(textPart.Text)
			}
			contentParts = append(contentParts, part)
		}
	}
	resp := messages.ToolCallResponse{ToolCallID: toolCallID}
	if len(contentParts) > 0 {
		resp.ContentParts = contentParts
		resp.Content = content.String() // plain-text fallback
	} else {
		resp.Content = content.String()
	}
	return resp
}

// ToAgentLoopDefs converts the registry's tools to subsystems.ToolDefinition format
// for passing to the agent loop.
func (r *ToolRegistry) ToAgentLoopDefs() []messages.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]messages.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, messages.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  convertParameters(tool.Parameters()),
		})
	}
	return messages.CanonicalToolDefinitions(defs)
}

// convertParameters converts a JSON Schema parameters map to a flat list of ToolParameter.
func convertParameters(schema map[string]any) []messages.ToolParameter {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}

	requiredSet := make(map[string]bool)
	if req, ok := schema["required"].([]string); ok {
		for _, name := range req {
			requiredSet[name] = true
		}
	} else if req, ok := schema["required"].([]any); ok {
		for _, v := range req {
			if name, ok := v.(string); ok {
				requiredSet[name] = true
			}
		}
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	params := make([]messages.ToolParameter, 0, len(props))
	for _, name := range names {
		propRaw := props[name]
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}

		paramType, _ := prop["type"].(string)
		desc, _ := prop["description"].(string)

		params = append(params, messages.ToolParameter{
			Name:        name,
			Type:        paramType,
			Description: desc,
			Required:    requiredSet[name],
		})
	}
	return params
}
