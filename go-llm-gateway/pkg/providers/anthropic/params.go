package anthropic

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const defaultMaxTokens = 4096

// applyInferenceRequestOptions sets MaxTokens, Temperature, StopSequences, Thinking, and CacheControl
// on params from the gateway InferenceRequest.
func applyInferenceRequestOptions(params *anthropic.MessageNewParams, req providers.InferenceRequest) {
	maxTokens := defaultMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	params.MaxTokens = int64(maxTokens)

	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}
	if len(req.StopSequences) > 0 {
		params.StopSequences = req.StopSequences
	}
	if req.Thinking != nil {
		params.Thinking = thinkingConfigToAnthropic(*req.Thinking)
	}
	if req.CacheControl != nil {
		params.CacheControl = cacheControlToAnthropic(*req.CacheControl)
	}
}

// cacheControlToAnthropic maps gateway CacheControlConfig to Anthropic CacheControlEphemeralParam.
// in_memory (or empty) -> 5m; 24h -> 1h.
func cacheControlToAnthropic(c providers.CacheControlConfig) anthropic.CacheControlEphemeralParam {
	p := anthropic.NewCacheControlEphemeralParam()
	if c.CacheRetentionPolicy == providers.CacheRetention24h {
		p.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	} else {
		p.TTL = anthropic.CacheControlEphemeralTTLTTL5m
	}
	return p
}

// ThinkingConfigToAnthropic maps gateway ThinkingConfig to Anthropic ThinkingConfigParamUnion.
// Exported for unit tests.
func ThinkingConfigToAnthropic(c providers.ThinkingConfig) anthropic.ThinkingConfigParamUnion {
	return thinkingConfigToAnthropic(c)
}

func thinkingConfigToAnthropic(c providers.ThinkingConfig) anthropic.ThinkingConfigParamUnion {
	switch c.Mode {
	case providers.ThinkingEnabled:
		budget := c.BudgetTokens
		if budget < 1024 {
			budget = 1024
		}
		return anthropic.ThinkingConfigParamOfEnabled(budget)
	case providers.ThinkingAdaptive:
		adaptive := anthropic.NewThinkingConfigAdaptiveParam()
		return anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	case providers.ThinkingOff:
		fallthrough
	default:
		disabled := anthropic.NewThinkingConfigDisabledParam()
		return anthropic.ThinkingConfigParamUnion{OfDisabled: &disabled}
	}
}
