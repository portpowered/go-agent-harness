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

func (Contract) StableBrokerToolDefinitions() []public.BrokerToolDefinition {
	return StableBrokerToolDefinitions()
}

func (Contract) StableBrokerToolSchemas() []map[string]any {
	return StableBrokerToolSchemas()
}

func (Contract) BrowserToolDefinitions(webCast ...bool) []public.BrokerToolDefinition {
	return BrowserToolDefinitions(webCast...)
}

func (Contract) BrowserToolSchemas(webCast ...bool) []map[string]any {
	return BrowserToolSchemas(webCast...)
}

func (Contract) BrokerToolDefinitions() []public.BrokerToolDefinition {
	return StableBrokerToolDefinitions()
}

func (Contract) NewClassifiedError(code ErrorCode, message string, details map[string]any) *public.ClassifiedError {
	return NewClassifiedError(code, message, details)
}

func (Contract) ResultErrorFor(err error, fallback ErrorCode, details map[string]any) public.ToolResultError {
	return ResultErrorFor(err, fallback, details)
}

func (Contract) DefaultErrorMessage(code ErrorCode) string {
	return DefaultErrorMessage(code)
}

func (Contract) ContextErrorCode(err error) ErrorCode {
	return ContextErrorCode(err)
}

func (Contract) NewToolResultSuccess(data any) (public.ToolResultEnvelope, error) {
	return NewToolResultSuccess(data)
}

func (Contract) NewToolResultFailure(resultError public.ToolResultError) public.ToolResultEnvelope {
	return NewToolResultFailure(resultError)
}

func (Contract) MarshalToolResult(envelope public.ToolResultEnvelope) ([]byte, error) {
	return MarshalToolResult(envelope)
}

func (Contract) EncodeToolResult(data any, resultError *public.ToolResultError) ([]byte, error) {
	return EncodeToolResult(data, resultError)
}

func (Contract) UnmarshalToolResult(data []byte) (public.ToolResultEnvelope, error) {
	return UnmarshalToolResult(data)
}

func (Contract) NormalizeBrowserParameterSchema(schema json.RawMessage) (json.RawMessage, string, bool) {
	return NormalizeBrowserParameterSchema(schema)
}

func (Contract) ValidatePageToolInput(input, schema json.RawMessage, maxBytes int) []public.ToolResultIssue {
	return validatePageToolInput(input, schema, maxBytes)
}

func (Contract) ValidatePageScreenshot(screenshot public.PageScreenshot) (public.ValidatedPageScreenshot, error) {
	return ValidatePageScreenshot(screenshot)
}

func stableToolNames() []string {
	return []string{
		public.GetContextToolName,
		public.ListTabsToolName,
		public.SelectTabToolName,
		public.ListToolsToolName,
		public.InvokeToolName,
		public.CancelToolName,
	}
}
