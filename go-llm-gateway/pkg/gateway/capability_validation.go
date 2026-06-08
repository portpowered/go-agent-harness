package gateway

import (
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const (
	statelessMode = "stateless"
	sessionMode   = "session"

	featureInference       = "inference"
	featureStreaming       = "streaming"
	featureTools           = "tools"
	featureImageInput      = "imageInput"
	featureAudioInput      = "audioInput"
	featureVideoInput      = "videoInput"
	featureReasoning       = "reasoning"
	featurePromptCaching   = "promptCaching"
	featureProviderOptions = "providerOptions"
	featureSessions        = "sessions"
	featureTextModality    = "textModality"
	featureAudioModality   = "audioModality"
	featureInputAudio      = "inputAudioFormat"
	featureOutputAudio     = "outputAudioFormat"
	featureTurnDetection   = "turnDetection"
)

func validateStatelessRequest(p providers.Provider, req providers.InferenceRequest, streaming bool) error {
	reporter, ok := p.(providers.CapabilityReporter)
	if !ok {
		return nil
	}

	caps := reporter.Capabilities()
	stateless := caps.Stateless
	if streaming {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureStreaming, stateless.Streaming); err != nil {
			return err
		}
	} else if err := unsupportedFeatureError(caps.Provider, statelessMode, featureInference, stateless.Inference); err != nil {
		return err
	}

	if len(req.Tools) > 0 {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureTools, stateless.Tools); err != nil {
			return err
		}
	}
	if requestHasImageInput(req) {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureImageInput, stateless.ImageInput); err != nil {
			return err
		}
	}
	if requestHasAudioInput(req) {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureAudioInput, stateless.AudioInput); err != nil {
			return err
		}
	}
	if requestHasVideoInput(req) {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureVideoInput, stateless.VideoInput); err != nil {
			return err
		}
	}
	if requestHasReasoning(req) {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureReasoning, stateless.Reasoning); err != nil {
			return err
		}
	}
	if req.CacheControl != nil {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featurePromptCaching, stateless.PromptCaching); err != nil {
			return err
		}
	}
	if len(req.Config) > 0 {
		if err := unsupportedFeatureError(caps.Provider, statelessMode, featureProviderOptions, stateless.ProviderOptions); err != nil {
			return err
		}
	}
	return nil
}

func validateSessionRequest(p providers.SessionProvider, config models.SessionConfig) error {
	reporter, ok := p.(providers.CapabilityReporter)
	if !ok {
		return nil
	}

	caps := reporter.Capabilities()
	if caps.Session == nil {
		return nil
	}

	session := *caps.Session
	if err := unsupportedFeatureError(caps.Provider, sessionMode, featureSessions, session.Sessions); err != nil {
		return err
	}
	if len(config.Tools) > 0 {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, featureTools, session.Tools); err != nil {
			return err
		}
	}
	if sessionHasTextModality(config) {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, featureTextModality, session.TextModality); err != nil {
			return err
		}
	}
	if sessionHasAudioModality(config) {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, featureAudioModality, session.AudioModality); err != nil {
			return err
		}
	}
	if config.InputAudioFormat != "" {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, audioFormatFeature(featureInputAudio, config.InputAudioFormat), session.InputAudioFormats[config.InputAudioFormat]); err != nil {
			return err
		}
	}
	if config.OutputAudioFormat != "" {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, audioFormatFeature(featureOutputAudio, config.OutputAudioFormat), session.OutputAudioFormats[config.OutputAudioFormat]); err != nil {
			return err
		}
	}
	if config.TurnDetection != nil {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, featureTurnDetection, session.TurnDetection); err != nil {
			return err
		}
	}
	if len(config.Config) > 0 {
		if err := unsupportedFeatureError(caps.Provider, sessionMode, featureProviderOptions, session.ProviderOptions); err != nil {
			return err
		}
	}
	return nil
}

func unsupportedFeatureError(provider, mode, feature string, capability providers.Capability) error {
	if capability.State != providers.CapabilityUnsupported {
		return nil
	}
	return &providers.UnsupportedFeatureError{
		Provider:   provider,
		Feature:    feature,
		Mode:       mode,
		Capability: capability,
	}
}

func requestHasImageInput(req providers.InferenceRequest) bool {
	return requestHasContentPart(req, func(part models.ContentPart) bool {
		_, ok := part.(models.ImagePart)
		return ok
	})
}

func requestHasAudioInput(req providers.InferenceRequest) bool {
	return requestHasContentPart(req, func(part models.ContentPart) bool {
		_, ok := part.(models.AudioPart)
		return ok
	})
}

func requestHasVideoInput(req providers.InferenceRequest) bool {
	return requestHasContentPart(req, func(part models.ContentPart) bool {
		_, ok := part.(models.VideoPart)
		return ok
	})
}

func requestHasReasoning(req providers.InferenceRequest) bool {
	if req.Thinking != nil && req.Thinking.Mode != providers.ThinkingOff {
		return true
	}
	return requestHasContentPart(req, func(part models.ContentPart) bool {
		_, ok := part.(messages.ReasoningPart)
		return ok
	})
}

func requestHasContentPart(req providers.InferenceRequest, match func(models.ContentPart) bool) bool {
	for _, msg := range req.Messages {
		for _, part := range msg.ContentParts {
			if match(part) {
				return true
			}
		}
	}
	return false
}

func sessionHasTextModality(config models.SessionConfig) bool {
	for _, modality := range config.Modalities {
		if modality == models.SessionModalityText {
			return true
		}
	}
	return false
}

func sessionHasAudioModality(config models.SessionConfig) bool {
	if config.InputAudioFormat != "" || config.OutputAudioFormat != "" ||
		config.InputAudioSampleRate != 0 || config.OutputAudioSampleRate != 0 ||
		config.Voice != "" {
		return true
	}
	for _, modality := range config.Modalities {
		if modality == models.SessionModalityAudio {
			return true
		}
	}
	return false
}

func audioFormatFeature(prefix string, format models.AudioFormat) string {
	return prefix + ":" + string(format)
}
