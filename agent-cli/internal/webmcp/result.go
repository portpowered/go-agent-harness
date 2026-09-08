package webmcp

import runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"

// The reusable tools service owns the browser result envelope. The CLI keeps
// these aliases so broker protocol code remains a host adapter rather than a
// second source of result shape policy.
type ToolResultIssue = runtimeTools.ToolResultIssue
type ToolResultError = runtimeTools.ToolResultError
type ToolResultEnvelope = runtimeTools.ToolResultEnvelope

type ResultEnvelope = runtimeTools.ResultEnvelope
type ResultError = runtimeTools.ResultError
type ResultIssue = runtimeTools.ResultIssue

func NewToolResultSuccess(data any) (ToolResultEnvelope, error) {
	return browserContract().NewToolResultSuccess(data)
}

func NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope {
	return browserContract().NewToolResultFailure(resultError)
}

func MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error) {
	return browserContract().MarshalToolResult(envelope)
}

func EncodeToolResult(data any, resultError *ToolResultError) ([]byte, error) {
	return browserContract().EncodeToolResult(data, resultError)
}

func UnmarshalToolResult(data []byte) (ToolResultEnvelope, error) {
	return browserContract().UnmarshalToolResult(data)
}
