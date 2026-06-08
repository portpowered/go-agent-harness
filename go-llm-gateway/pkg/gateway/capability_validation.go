package gateway

import (
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const (
	statelessMode = "stateless"

	featureInference       = "inference"
	featureStreaming       = "streaming"
	featureTools           = "tools"
	featureImageInput      = "imageInput"
	featureAudioInput      = "audioInput"
	featureVideoInput      = "videoInput"
	featureReasoning       = "reasoning"
	featurePromptCaching   = "promptCaching"
	featureProviderOptions = "providerOptions"
)

func validateStatelessRequest(p providers.Provider, req providers.InferenceRequest, streaming bool) error {
	reporter, ok := p.(providers.CapabilityReporter)
	if !ok {
		return nil
	}

	caps := reporter.Capabilities()
	stateless := caps.Stateless
	if streaming {
		if err := unsupportedFeatureError(caps.Provider, featureStreaming, stateless.Streaming); err != nil {
			return err
		}
	} else if err := unsupportedFeatureError(caps.Provider, featureInference, stateless.Inference); err != nil {
		return err
	}

	if len(req.Tools) > 0 {
		if err := unsupportedFeatureError(caps.Provider, featureTools, stateless.Tools); err != nil {
			return err
		}
	}
	if requestHasImageInput(req) {
		if err := unsupportedFeatureError(caps.Provider, featureImageInput, stateless.ImageInput); err != nil {
			return err
		}
	}
	if requestHasAudioInput(req) {
		if err := unsupportedFeatureError(caps.Provider, featureAudioInput, stateless.AudioInput); err != nil {
			return err
		}
	}
	if requestHasVideoInput(req) {
		if err := unsupportedFeatureError(caps.Provider, featureVideoInput, stateless.VideoInput); err != nil {
			return err
		}
	}
	if requestHasReasoning(req) {
		if err := unsupportedFeatureError(caps.Provider, featureReasoning, stateless.Reasoning); err != nil {
			return err
		}
	}
	if req.CacheControl != nil {
		if err := unsupportedFeatureError(caps.Provider, featurePromptCaching, stateless.PromptCaching); err != nil {
			return err
		}
	}
	if len(req.Config) > 0 {
		if err := unsupportedFeatureError(caps.Provider, featureProviderOptions, stateless.ProviderOptions); err != nil {
			return err
		}
	}
	return nil
}

func unsupportedFeatureError(provider, feature string, capability providers.Capability) error {
	if capability.State != providers.CapabilityUnsupported {
		return nil
	}
	return &providers.UnsupportedFeatureError{
		Provider:   provider,
		Feature:    feature,
		Mode:       statelessMode,
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
