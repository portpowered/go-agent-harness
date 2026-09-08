package browser

import (
	"encoding/json"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// Contract exposes the pure WebMCP protocol policy through the tools service
// wire. It has no browser connection or host/platform state.
type Contract struct{}

var _ public.BrowserContract = Contract{}

func (Contract) StableToolNames() []string {
	return stableToolNames()
}

func (Contract) StableBrokerToolDefinitions() []BrokerToolDefinition {
	return StableBrokerToolDefinitions()
}

func (Contract) StableBrokerToolSchemas() []map[string]any {
	return StableBrokerToolSchemas()
}

func (Contract) BrowserToolDefinitions(webCast ...bool) []BrokerToolDefinition {
	return BrowserToolDefinitions(webCast...)
}

func (Contract) BrowserToolSchemas(webCast ...bool) []map[string]any {
	return BrowserToolSchemas(webCast...)
}

func (Contract) BrokerToolDefinitions() []BrokerToolDefinition {
	return StableBrokerToolDefinitions()
}

func (Contract) NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError {
	return NewClassifiedError(code, message, details)
}

func (Contract) ResultErrorFor(err error, fallback ErrorCode, details map[string]any) ToolResultError {
	return ResultErrorFor(err, fallback, details)
}

func (Contract) DefaultErrorMessage(code ErrorCode) string {
	return DefaultErrorMessage(code)
}

func (Contract) ContextErrorCode(err error) ErrorCode {
	return ContextErrorCode(err)
}

func (Contract) NewToolResultSuccess(data any) (ToolResultEnvelope, error) {
	return NewToolResultSuccess(data)
}

func (Contract) NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope {
	return NewToolResultFailure(resultError)
}

func (Contract) MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error) {
	return MarshalToolResult(envelope)
}

func (Contract) EncodeToolResult(data any, resultError *ToolResultError) ([]byte, error) {
	return EncodeToolResult(data, resultError)
}

func (Contract) UnmarshalToolResult(data []byte) (ToolResultEnvelope, error) {
	return UnmarshalToolResult(data)
}

func (Contract) NormalizeBrowserParameterSchema(schema json.RawMessage) (json.RawMessage, string, bool) {
	return NormalizeBrowserParameterSchema(schema)
}

func (Contract) ValidatePageToolInput(input, schema json.RawMessage, maxBytes int) []ToolResultIssue {
	return validatePageToolInput(input, schema, maxBytes)
}

func (Contract) ValidatePageScreenshot(screenshot public.PageScreenshot) (public.ValidatedPageScreenshot, error) {
	return ValidatePageScreenshot(screenshot)
}

func stableToolNames() []string {
	return []string{
		GetContextToolName,
		ListTabsToolName,
		SelectTabToolName,
		ListToolsToolName,
		InvokeToolName,
		CancelToolName,
	}
}
