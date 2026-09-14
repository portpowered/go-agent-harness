package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func sessionVoiceGainDB(voice string) float64 {
	return wire.NewService().VoiceGainDB(voice)
}

func sessionInputTranscriptionPolicy(opts SessionRunOptions, provider string, acceptsAudioInput bool) models.InputAudioTranscriptionConfig {
	if opts.InputAudioTranscription != nil {
		return *opts.InputAudioTranscription
	}
	resolved := wire.NewService().ResolveTranscription(audioio.TranscriptionRequest{
		Provider:          provider,
		Replay:            opts.ReplayPath != "",
		AcceptsAudioInput: acceptsAudioInput,
		Enabled:           !opts.NoInputTranscription,
	})
	if !resolved.Enabled {
		return models.InputAudioTranscriptionConfig{}
	}
	return models.InputAudioTranscriptionConfig{Enabled: resolved.Enabled, Model: resolved.Model}
}
