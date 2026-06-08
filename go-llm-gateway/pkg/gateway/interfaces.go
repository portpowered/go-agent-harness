package gateway

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// Gateway is the unified interface for sending inference requests to LLM providers.
type Gateway interface {
	Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error)
	InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error)
	Interact(ctx context.Context, req InteractionRequest) (<-chan InteractionEvent, error)
}

// CapabilityReporter is implemented by gateway types that can report the
// configured provider's public capability contract.
type CapabilityReporter interface {
	Capabilities() ProviderCapabilities
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

// ProviderCapabilities re-exports the public capability contract from the
// gateway package for callers already importing this surface.
type ProviderCapabilities = capabilities.ProviderCapabilities

// FeatureCapability re-exports one feature capability from the public contract.
type FeatureCapability = capabilities.FeatureCapability

// CapabilityState re-exports the support-state enum from the public contract.
type CapabilityState = capabilities.CapabilityState

const (
	CapabilityStateUnknown     = capabilities.CapabilityStateUnknown
	CapabilityStateSupported   = capabilities.CapabilityStateSupported
	CapabilityStateUnsupported = capabilities.CapabilityStateUnsupported
)
