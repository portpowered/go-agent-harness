package gateway

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// Gateway is the unified interface for sending inference requests to LLM providers.
type Gateway interface {
	Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error)
	InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error)
	Interact(ctx context.Context, req InteractionRequest) (<-chan InteractionEvent, error)
}

// InferenceRequest is the input to the gateway.
type InferenceRequest struct {
	Messages []models.Message
	Tools    []models.ToolDefinition
	Model    string

	MaxTokens        *int
	Temperature      *float64
	StopSequences    []string
	FrequencyPenalty *float64
	Thinking         *providers.ThinkingConfig
	CacheControl     *providers.CacheControlConfig
	Config           json.RawMessage
}

// InferenceResponse is the gateway output.
type InferenceResponse = providers.InferenceResponse
