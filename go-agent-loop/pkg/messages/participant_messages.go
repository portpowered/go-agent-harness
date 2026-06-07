package messages

import (
	"context"
	"fmt"
)

// KernelMessageRequest is a full message dispatched to the kernel for IO delivery.
// Populated from SYSTEM.FULL_MESSAGE delta events that flow through DeltaInbox.
type KernelMessageRequest struct {
	Source  ParticipantID
	Message Message
	// IsLoopEnd is retained for backward compatibility with ranging consumers.
	// It is never set in the current implementation; the channel is closed on
	// LOOP.END instead of sending a sentinel value.
	IsLoopEnd bool
}

// KernelDeltaRequest is a streaming delta dispatched to the kernel for IO delivery.
type KernelDeltaRequest struct {
	Source ParticipantID
	Delta  StreamMessage
}

// ToolCallResponse is the result of a tool execution.
// Set ContentParts for rich (multimodal) results such as images.
// If ContentParts is non-empty it takes precedence over Content.
type ToolCallResponse struct {
	ToolCallID   string
	Name         string
	Content      string
	ContentParts []ContentPart
}

// Inferencer is implemented by go-llm-gateway to provide model inference.
type Inferencer interface {
	Infer(ctx context.Context, req InferenceRequest) (InferenceResult, error)
	InferStream(ctx context.Context, req InferenceRequest) (<-chan StreamMessage, error)
}

// InferenceResult is the complete result of a non-streaming inference call.
type InferenceResult struct {
	Message    Message
	ToolCalls  []ToolCall
	TokenUsage TokenUsage
}

// ToolExecutor executes tool calls. Implemented by the consumer of the library.
type ToolExecutor interface {
	Execute(ctx context.Context, call ToolCall) (ToolCallResponse, error)
}

// DefaultToolExecutor is the default tool executor that returns an empty response.
type DefaultToolExecutor struct {
}

func (e *DefaultToolExecutor) Execute(ctx context.Context, call ToolCall) (ToolCallResponse, error) {
	return ToolCallResponse{}, fmt.Errorf("default tool executor is not implemented")
}

// ToolBatchRequest is sent to the ToolRunner's inbox to execute a batch of tool calls.
type ToolBatchRequest struct {
	Calls      []ToolCall
	LoopPassID int // Pass ID when this batch was dispatched; used by the runner to tag outgoing deltas.
}

// ToolBatchResponse is written to the ToolRunner's outbox with all results.
type ToolBatchResponse struct {
	Results []ToolCallResponse
	Error   error
}

// InferenceDefaults holds default inference parameters that are applied to every
// InferenceRequest dispatched by the coordinator. Per-request values (non-nil
// fields on InferenceRequest) take precedence over these defaults.
type InferenceDefaults struct {
	MaxTokens        *int
	Temperature      *float64
	StopSequences    []string
	FrequencyPenalty *float64
}

// InferenceRequest is sent to the ModelRunner's inbox to trigger inference.
type InferenceRequest struct {
	Messages   []Message
	Tools      []ToolDefinition
	LoopPassID int // Pass ID when this request was dispatched; used by the runner to tag outgoing deltas.

	// MaxTokens is the maximum number of tokens to generate. Nil = provider default.
	MaxTokens *int
	// Temperature controls randomness (0.0–2.0 for OpenAI, 0.0–1.0 for Anthropic). Nil = provider default.
	Temperature *float64
	// StopSequences cause the model to stop generating when encountered.
	StopSequences []string
	// FrequencyPenalty penalizes repeated tokens. Nil = provider default (typically 0, disabled).
	FrequencyPenalty *float64
}

// NewInferenceRequest creates an InferenceRequest populated with the given
// defaults. Nil defaults are safe (fields remain zero-valued).
func NewInferenceRequest(msgs []Message, tools []ToolDefinition, passID int, defaults *InferenceDefaults) InferenceRequest {
	req := InferenceRequest{
		Messages:   msgs,
		Tools:      tools,
		LoopPassID: passID,
	}
	if defaults != nil {
		req.MaxTokens = defaults.MaxTokens
		req.Temperature = defaults.Temperature
		req.StopSequences = defaults.StopSequences
		req.FrequencyPenalty = defaults.FrequencyPenalty
	}
	return req
}

// InferenceResponse is written to the ModelRunner's outbox with the result.
type InferenceResponse struct {
	Result InferenceResult
	Error  error
}

// UserRequest is sent to the UserRunner's inbox to trigger user input.
type UserRequest struct {
	Message Message
}

// UserResponse is written to the UserRunner's outbox with the result.
type UserResponse struct {
	Message Message
	Error   error
}
