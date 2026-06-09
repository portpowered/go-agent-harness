package gateway

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func validateStatelessRequest(caps ProviderCapabilities, req InferenceRequest, mode string) error {
	stateless := caps.Stateless

	checks := []struct {
		required bool
		feature  capabilities.Feature
		cap      capabilities.FeatureCapability
	}{
		{required: len(req.Tools) > 0, feature: capabilities.FeatureTools, cap: stateless.Tools},
		{required: mode == capabilities.RequestedModeStatelessStream, feature: capabilities.FeatureStreaming, cap: stateless.Streaming},
		{required: hasImageInput(req.Messages), feature: capabilities.FeatureImageInput, cap: stateless.ImageInput},
		{required: hasAudioInput(req.Messages), feature: capabilities.FeatureAudioInput, cap: stateless.AudioInput},
		{required: hasAudioOutput(req.Messages), feature: capabilities.FeatureAudioOutput, cap: stateless.AudioOutput},
		{required: hasVideoOutput(req.Messages), feature: capabilities.FeatureVideoOutput, cap: stateless.VideoOutput},
		{required: requiresReasoning(req.Thinking), feature: capabilities.FeatureReasoning, cap: stateless.Reasoning},
		{required: req.CacheControl != nil, feature: capabilities.FeaturePromptCaching, cap: stateless.PromptCaching},
		{required: hasProviderSpecificConfig(req.Config), feature: capabilities.FeatureProviderSpecificConfig, cap: stateless.ProviderSpecificConfig},
	}

	for _, check := range checks {
		if check.required && check.cap.State == capabilities.CapabilityStateUnsupported {
			return &capabilities.UnsupportedFeatureError{
				Provider:      caps.Provider,
				Feature:       check.feature,
				RequestedMode: mode,
				Capability:    check.cap,
			}
		}
	}

	return nil
}

func validateSessionConfig(caps ProviderCapabilities, config models.SessionConfig) error {
	session := caps.Session

	checks := []struct {
		required bool
		feature  capabilities.Feature
		cap      capabilities.FeatureCapability
	}{
		{required: true, feature: capabilities.FeatureSessions, cap: session.Sessions},
		{required: len(config.Tools) > 0, feature: capabilities.FeatureTools, cap: session.Tools},
		{required: requiresSessionAudioInput(config), feature: capabilities.FeatureAudioInput, cap: session.AudioInput},
		{required: requiresSessionAudioOutput(config), feature: capabilities.FeatureAudioOutput, cap: session.AudioOutput},
		{required: hasProviderSpecificConfig(config.Config), feature: capabilities.FeatureProviderSpecificConfig, cap: session.ProviderSpecificConfig},
	}

	for _, check := range checks {
		if check.required && check.cap.State == capabilities.CapabilityStateUnsupported {
			return &capabilities.UnsupportedFeatureError{
				Provider:      caps.Provider,
				Feature:       check.feature,
				RequestedMode: capabilities.RequestedModeSession,
				Capability:    check.cap,
			}
		}
	}

	return nil
}

func providerInferenceRequest(req InferenceRequest) providers.InferenceRequest {
	return providers.InferenceRequest{
		Messages:         req.Messages,
		Tools:            req.Tools,
		Model:            req.Model,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		StopSequences:    req.StopSequences,
		FrequencyPenalty: req.FrequencyPenalty,
		Thinking:         req.Thinking,
		CacheControl:     req.CacheControl,
		Config:           req.Config,
	}
}

func hasImageInput(messages []models.Message) bool {
	for _, msg := range messages {
		if msg.Role != models.RoleUser {
			continue
		}
		for _, part := range msg.ContentParts {
			if _, ok := part.(models.ImagePart); ok {
				return true
			}
		}
	}
	return false
}

func hasAudioInput(messages []models.Message) bool {
	for _, msg := range messages {
		if msg.Role != models.RoleUser {
			continue
		}
		for _, part := range msg.ContentParts {
			if _, ok := part.(models.AudioPart); ok {
				return true
			}
		}
	}
	return false
}

func hasAudioOutput(messages []models.Message) bool {
	for _, msg := range messages {
		if msg.Role != models.RoleAssistant {
			continue
		}
		for _, part := range msg.ContentParts {
			if _, ok := part.(models.AudioPart); ok {
				return true
			}
		}
	}
	return false
}

func hasVideoOutput(messages []models.Message) bool {
	for _, msg := range messages {
		if msg.Role != models.RoleAssistant {
			continue
		}
		for _, part := range msg.ContentParts {
			if _, ok := part.(models.VideoPart); ok {
				return true
			}
		}
	}
	return false
}

func requiresReasoning(thinking *providers.ThinkingConfig) bool {
	return thinking != nil && thinking.Mode != providers.ThinkingOff
}

func hasProviderSpecificConfig(config json.RawMessage) bool {
	return len(config) > 0 && string(config) != "null"
}

func requiresSessionAudioInput(config models.SessionConfig) bool {
	return config.InputAudioFormat != "" || config.InputAudioSampleRate != 0
}

func requiresSessionAudioOutput(config models.SessionConfig) bool {
	if config.OutputAudioFormat != "" || config.OutputAudioSampleRate != 0 || config.Voice != "" {
		return true
	}
	for _, modality := range config.Modalities {
		if modality == models.SessionModalityAudio {
			return true
		}
	}
	return false
}
