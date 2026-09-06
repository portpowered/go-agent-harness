package webmcp

import (
	"bytes"
	"encoding/json"

	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

const maxInputValidationIssues = 64

func validatePageToolInput(input, schema json.RawMessage, maxBytes int) []ToolResultIssue {
	return runtimeToolsWire.NewService().BrowserContract().ValidatePageToolInput(input, schema, maxBytes)
}

func invalidPageInputError(ref ToolRef, descriptor ToolDescriptor, issues []ToolResultIssue) error {
	schema := cloneJSON(descriptor.InputSchema)
	if len(bytes.TrimSpace(schema)) == 0 {
		schema = json.RawMessage(`{}`)
	}
	boundedIssues := append([]ToolResultIssue(nil), issues...)
	if len(boundedIssues) > maxInputValidationIssues {
		boundedIssues = boundedIssues[:maxInputValidationIssues]
	}
	return classified(ErrorInvalidToolInput, "Input does not match the selected tool schema.", map[string]any{
		"tool_ref":     string(ref),
		"input_schema": schema,
		"issues":       boundedIssues,
	}, ErrInvalidToolInput)
}
