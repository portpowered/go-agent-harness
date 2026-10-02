package tools

import "encoding/json"

// ToolResult represents the structured return value from tool execution.
// It provides clear semantics for different types of results and supports
// async operations, user-facing messages, and error handling.
type ToolResult struct {
	// ForLLM is the content sent to the LLM for context.
	// Required for all results.
	ForLLM string `json:"for_llm"`

	// ForUser is the content sent directly to the user.
	// If empty, no user message is sent.
	// Silent=true overrides this field.
	ForUser string `json:"for_user,omitempty"`

	// Silent suppresses sending any message to the user.
	// When true, ForUser is ignored even if set.
	Silent bool `json:"silent"`

	// IsError indicates whether the tool execution failed.
	// When true, the result should be treated as an error.
	IsError bool `json:"is_error"`

	// Async indicates whether the tool is running asynchronously.
	// When true, the tool will complete later and notify via callback.
	Async bool `json:"async"`

	// Err is the underlying error (not JSON serialized).
	// Used for internal error handling and logging.
	Err error `json:"-"`
}

// MarshalJSON implements custom JSON serialization.
// The Err field is excluded from JSON output via the json:"-" tag.
func (tr *ToolResult) MarshalJSON() ([]byte, error) {
	type Alias ToolResult
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(tr),
	})
}

// WithError sets the Err field and returns the result for chaining.
// This preserves the error for logging while keeping it out of JSON.
//
// Example:
//
//	result := ErrorResult("Operation failed").WithError(err)
func (tr *ToolResult) WithError(err error) *ToolResult {
	tr.Err = err
	return tr
}
