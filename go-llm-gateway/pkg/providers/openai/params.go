package openai

import "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"

// applyInferenceRequestOptions sets MaxTokens, Temperature, FrequencyPenalty,
// and Stop on the request from the gateway InferenceRequest.
// Thinking is ignored (no OpenAI equivalent). CacheControl uses default in-memory behavior.
func applyInferenceRequestOptions(req *chatRequest, inf providers.InferenceRequest) {
	if inf.MaxTokens != nil && *inf.MaxTokens > 0 {
		req.MaxTokens = inf.MaxTokens
	}
	if inf.Temperature != nil {
		req.Temperature = inf.Temperature
	}
	if inf.FrequencyPenalty != nil {
		req.FrequencyPenalty = inf.FrequencyPenalty
	}
	switch len(inf.StopSequences) {
	case 0:
		// leave nil
	case 1:
		req.Stop = inf.StopSequences[0]
	default:
		req.Stop = inf.StopSequences
	}
}
