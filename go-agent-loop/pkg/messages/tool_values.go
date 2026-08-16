package messages

// Tool definitions, parameters, and stream values for model tool calls live in this file.

// ToolCall represents a request from the model to invoke a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-encoded arguments
}

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []ToolParameter
}

// ToolParameter describes a single parameter of a tool.
type ToolParameter struct {
	Name        string
	Type        string // "string", "number", "boolean", "object"
	Description string
	Required    bool
}

// ToolCallStartValue is the value for TOOLCALL.START (inner type "tool_use_start").
type ToolCallStartValue struct {
	Type       string `json:"type"` // "tool_use_start"
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
}

func (*ToolCallStartValue) streamMessageValue() {}

// NewToolCallStartValue returns a value for TOOLCALL.START.
func NewToolCallStartValue(id, name string) *ToolCallStartValue {
	return &ToolCallStartValue{Type: "tool_use_start", ToolCallID: id, Name: name}
}

// ToolCallDeltaValue is the value for TOOLCALL.DELTA (inner type "input_json_delta").
type ToolCallDeltaValue struct {
	Type        string `json:"type"`         // "input_json_delta"
	PartialJSON string `json:"partial_json"` // incremental JSON for tool arguments
}

func (*ToolCallDeltaValue) streamMessageValue() {}

// NewToolCallDeltaValue returns a value for TOOLCALL.DELTA.
func NewToolCallDeltaValue(partialJSON string) *ToolCallDeltaValue {
	return &ToolCallDeltaValue{Type: "input_json_delta", PartialJSON: partialJSON}
}

// ToolCallEndValue is the value for TOOLCALL.END (inner type "tool_use_end").
type ToolCallEndValue struct {
	Type       string `json:"type"`      // "tool_use_end"
	Arguments  string `json:"arguments"` // full JSON arguments when block is complete
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
}

func (*ToolCallEndValue) streamMessageValue() {}

// NewToolCallEndValue returns a value for TOOLCALL.END.
func NewToolCallEndValue(toolCallID, name, arguments string) *ToolCallEndValue {
	return &ToolCallEndValue{Type: "tool_use_end", ToolCallID: toolCallID, Name: name, Arguments: arguments}
}
