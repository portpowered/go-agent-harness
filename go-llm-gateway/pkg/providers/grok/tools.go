package grok

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func grokToolDefinitions(tools []messages.ToolDefinition) []map[string]any {
	canonical := messages.CanonicalToolDefinitions(tools)
	definitions := make([]map[string]any, 0, len(canonical))
	for _, tool := range canonical {
		definitions = append(definitions, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  grokToolParameters(tool),
		})
	}
	return definitions
}

func grokToolParameters(tool messages.ToolDefinition) map[string]any {
	var schema map[string]any
	if len(tool.ParameterSchema) > 0 && json.Unmarshal(tool.ParameterSchema, &schema) == nil && schema != nil {
		return schema
	}

	properties := make(map[string]any, len(tool.Parameters))
	required := make([]string, 0, len(tool.Parameters))
	for _, parameter := range tool.Parameters {
		properties[parameter.Name] = map[string]any{
			"type":        parameter.Type,
			"description": parameter.Description,
		}
		if parameter.Required {
			required = append(required, parameter.Name)
		}
	}
	parameters := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		parameters["required"] = required
	}
	if tool.ParametersClosed {
		parameters["additionalProperties"] = false
	}
	return parameters
}
