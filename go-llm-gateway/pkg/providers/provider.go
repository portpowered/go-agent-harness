package providers

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// Provider is the interface that LLM providers must implement.
type Provider interface {
	// Name returns the provider name (e.g. "openai", "anthropic").
	Name() string
	// Infer sends messages to the model and returns a complete response.
	Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error)
	// InferStream sends messages and returns a channel of streaming messages (heterogeneous: TEXT/TOOLCALL/AUDIO/IMAGE start/delta/end).
	InferStream(ctx context.Context, req InferenceRequest) (<-chan messages.StreamMessage, error)
}

// CapabilityReporter is implemented by providers that can report their public
// capability contract without network access or live credentials. Providers
// that do not implement this interface default to unknown capabilities.
type CapabilityReporter interface {
	Capabilities() capabilities.ProviderCapabilities
}

// ProviderCapabilities re-exports the public capability contract from the
// providers package for callers already importing this surface.
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

// UnknownProviderCapabilities returns the documented fallback for providers
// without explicit capability reporting.
func UnknownProviderCapabilities(provider string) capabilities.ProviderCapabilities {
	return capabilities.UnknownProviderCapabilities(provider)
}

// ThinkingMode configures extended thinking (Anthropic). Ignored by other providers.
type ThinkingMode int

const (
	ThinkingOff ThinkingMode = iota
	// ThinkingEnabled enables thinking with the given token budget (min 1024 for Anthropic).
	ThinkingEnabled
	// ThinkingAdaptive lets the model decide thinking budget (Anthropic).
	ThinkingAdaptive
)

// ThinkingConfig configures extended thinking. Only Anthropic uses this; other providers ignore it.
type ThinkingConfig struct {
	Mode         ThinkingMode
	BudgetTokens int64 // used when Mode == ThinkingEnabled; must be >= 1024 for Anthropic
}

// Cache retention policy for prompt caching. Caching is enabled by default for supported models.
// - https://developers.openai.com/api/docs/guides/prompt-caching
// - https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
const (
	// Only supported by anthropic
	CacheControlTTL5m = "5m" // 5 minutes
	// Only supported by anthropic
	CacheControlTTL1h = "1h" // 1 hour
	// Only supported by openai
	CacheRetentionInMemory = "in_memory"
	// Only supported by openai
	CacheRetention24h = "24h" // 24 hours.
)

// CacheControlConfig configures prompt cache retention. Nil = no explicit top-level cache control.
// Anthropic: sets cache_control on the last cacheable block. OpenAI: prompt_cache_retention when supported by SDK.
type CacheControlConfig struct {
	CacheRetentionPolicy string // "in_memory" or "24h"; default "in_memory" if empty
}

// InferenceRequest is the input to an inference call.
type InferenceRequest struct {
	Messages []models.Message
	Tools    []models.ToolDefinition
	Model    string // optional model override

	// MaxTokens is the maximum number of tokens to generate. Nil/zero = provider default.
	MaxTokens *int
	// Temperature: 0.0–2.0 (OpenAI) / 0.0–1.0 (Anthropic). Nil = provider default.
	Temperature *float64
	// StopSequences cause the model to stop when encountered.
	StopSequences []string
	// FrequencyPenalty penalizes repeated tokens. Nil = provider default (typically 0).
	// OpenAI-compatible: mapped to frequency_penalty. Range varies by provider.
	FrequencyPenalty *float64
	// Thinking configures extended thinking (Anthropic only). Nil = provider default (off).
	Thinking *ThinkingConfig
	// CacheControl sets prompt cache retention (Anthropic: cache_control block; OpenAI: when SDK supports it). Nil = no explicit control.
	CacheControl *CacheControlConfig
	// Config holds model-specific parameters as raw JSON. Providers that support
	// extra configuration (e.g. fal.ai aspect_ratio, duration, cfg_scale) merge
	// these fields into the outgoing request body. Nil = no extra config.
	Config json.RawMessage
}

// InferenceResponse is the complete result of an inference call.
type InferenceResponse struct {
	Message models.Message
	Usage   models.TokenUsage
}
