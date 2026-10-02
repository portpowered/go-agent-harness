package webmcp

func NewToolResultSuccess(data any) (ToolResultEnvelope, error) {
	return browserContract().NewToolResultSuccess(data)
}

func NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope {
	return browserContract().NewToolResultFailure(resultError)
}

func MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error) {
	return browserContract().MarshalToolResult(envelope)
}
