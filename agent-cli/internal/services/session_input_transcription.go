package services

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// resolveInputAudioTranscriptionPolicy resolves the request-scoped policy
// before an OpenAI provider is constructed. The returned value is explicit
// even when disabled, so each participant/session has an isolated decision
// and provider adapters never need package-global state or raw JSON merges.
func resolveInputAudioTranscriptionPolicy(opts SessionRunOptions, provider string, acceptsAudioInput bool) models.InputAudioTranscriptionConfig {
	if !acceptsAudioInput || opts.NoInputTranscription || opts.ReplayPath != "" || !strings.EqualFold(strings.TrimSpace(provider), sessionProviderOpenAI) {
		return models.InputAudioTranscriptionConfig{}
	}
	return models.InputAudioTranscriptionConfig{
		Enabled: true,
		Model:   models.DefaultInputAudioTranscriptionModel,
	}
}
