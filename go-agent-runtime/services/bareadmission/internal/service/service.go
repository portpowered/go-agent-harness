// Package service owns bare-session policy decisions behind the public
// services/bareadmission contract. It has no host discovery or acquisition
// dependencies.
package service

import (
	"context"
	"fmt"
	"strings"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission"
)

type Service struct{}

var _ public.Service = (*Service)(nil)

func New() *Service { return &Service{} }

func (s *Service) Resolve(ctx context.Context, request public.Request) (public.Result, error) {
	if err := ctx.Err(); err != nil {
		return public.Result{}, err
	}
	request.Config = cloneConfig(request.Config)
	request.Catalog = cloneCatalog(request.Catalog)

	provider := resolveProvider(request)
	if err := validateProvider(provider); err != nil {
		return public.Result{}, err
	}
	providerConfig := configuredProvider(request.Config, provider)
	model, err := resolveModel(request, provider, providerConfig)
	if err != nil {
		return public.Result{}, err
	}
	apiKey, err := resolveCredential(request, providerConfig, provider)
	if err != nil {
		return public.Result{}, err
	}
	baseURL := resolveBaseURL(request, providerConfig)
	transport, err := resolveTransport(request)
	if err != nil {
		return public.Result{}, err
	}
	turnDetection, err := resolveVAD(provider, sessionConfig(request.Config))
	if err != nil {
		return public.Result{}, err
	}
	transcription := resolveTranscription(request)
	device := resolveDevice(request)

	if err := ctx.Err(); err != nil {
		return public.Result{}, err
	}
	return public.Result{
		BareLive: true, Provider: provider, Model: model, APIKey: apiKey,
		BaseURL: baseURL, Transport: transport, TurnDetection: turnDetection,
		InputAudioTranscription: transcription, Device: device,
	}, nil
}

func validateProvider(provider string) error {
	if provider == public.ProviderOpenAI || provider == public.ProviderGrok {
		return nil
	}
	return fmt.Errorf("%w: %q (bare sessions support %q and %q)", public.ErrUnsupportedProvider, provider, public.ProviderOpenAI, public.ProviderGrok)
}

func resolveModel(request public.Request, provider string, providerConfig *public.ProviderConfig) (string, error) {
	model := strings.TrimSpace(request.Model)
	if request.ModelProvided && model == "" {
		return "", unsupportedModel(request.Catalog, model)
	}
	if model == "" {
		if session := sessionConfig(request.Config); session != nil {
			model = strings.TrimSpace(session.Model)
		}
	}
	if model == "" && providerConfig != nil {
		model = strings.TrimSpace(providerConfig.Model)
	}
	if model == "" && provider == public.ProviderOpenAI {
		model = public.DefaultOpenAIModel
	}
	if model == "" {
		return "", fmt.Errorf("%s session model is required for bare live session (configure model.%s.model or session.model in %s)", provider, provider, configPath(request.Config))
	}
	if err := validateModel(request.Catalog, provider, model); err != nil {
		return "", err
	}
	return model, nil
}

func resolveProvider(request public.Request) string {
	if provider := strings.ToLower(strings.TrimSpace(request.Provider)); provider != "" {
		return provider
	}
	if session := sessionConfig(request.Config); session != nil {
		if provider := strings.ToLower(strings.TrimSpace(session.Provider)); provider != "" {
			return provider
		}
	}
	if request.Config != nil {
		switch provider := strings.ToLower(strings.TrimSpace(request.Config.ModelProvider)); provider {
		case public.ProviderOpenAI, public.ProviderGrok:
			return provider
		}
	}
	return public.ProviderOpenAI
}

func configuredProvider(config *public.ConfigSnapshot, provider string) *public.ProviderConfig {
	if config == nil {
		return nil
	}
	var source *public.ProviderConfig
	switch provider {
	case public.ProviderOpenAI:
		source = config.OpenAI
	case public.ProviderGrok:
		source = config.Grok
	}
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func resolveCredential(request public.Request, providerConfig *public.ProviderConfig, provider string) (string, error) {
	apiKey := resolveAPIKey(request, providerConfig, provider)
	if strings.TrimSpace(apiKey) == "" {
		return "", &public.CredentialError{Provider: provider, ConfigPath: configPath(request.Config)}
	}
	return apiKey, nil
}

func resolveAPIKey(request public.Request, providerConfig *public.ProviderConfig, provider string) string {
	if strings.TrimSpace(request.APIKey) != "" {
		return request.APIKey
	}
	if providerConfig != nil && strings.TrimSpace(providerConfig.APIKey) != "" {
		return providerConfig.APIKey
	}
	if provider == public.ProviderOpenAI && strings.TrimSpace(request.EnvironmentOpenAIAPIKey) != "" {
		return request.EnvironmentOpenAIAPIKey
	}
	return ""
}

func resolveBaseURL(request public.Request, providerConfig *public.ProviderConfig) string {
	if strings.TrimSpace(request.BaseURL) != "" || providerConfig == nil {
		return request.BaseURL
	}
	return providerConfig.BaseURL
}

func validateModel(catalog *public.ModelCatalog, provider, model string) error {
	if provider != public.ProviderOpenAI {
		return nil
	}
	returnModel := unsupportedModel(catalog, model)
	if catalog != nil {
		if _, ok := catalog.LookupRealtimeModel(provider, strings.TrimSpace(model)); ok {
			return nil
		}
	}
	return returnModel
}

func unsupportedModel(catalog *public.ModelCatalog, model string) error {
	if catalog == nil {
		return fmt.Errorf("%w: OpenAI realtime model admission", public.ErrModelCatalogRequired)
	}
	return &public.UnsupportedRealtimeModelError{
		Provider:        "OpenAI",
		Model:           model,
		SupportedModels: catalog.SupportedRealtimeModelIDs(public.ProviderOpenAI),
	}
}

func resolveTransport(request public.Request) (string, error) {
	transport := strings.ToLower(strings.TrimSpace(request.Transport))
	if !request.TransportProvided && (transport == "" || transport == public.TransportWebSocket) {
		if session := sessionConfig(request.Config); session != nil {
			if configured := strings.ToLower(strings.TrimSpace(session.Transport)); configured != "" {
				transport = configured
			}
		}
	}
	if transport == "" {
		transport = public.TransportWebSocket
	}
	if transport != public.TransportWebSocket && transport != public.TransportWebRTC {
		return "", &public.InvalidTransportError{Transport: transport}
	}
	return transport, nil
}

func resolveVAD(provider string, config *public.SessionConfig) (*public.TurnDetection, error) {
	defaultType := "server_vad"
	if provider == public.ProviderOpenAI {
		defaultType = "semantic_vad"
	}
	turnDetection := &public.TurnDetection{Type: defaultType}
	if config == nil || config.VAD == nil {
		return turnDetection, nil
	}
	vad := config.VAD
	if vad.Enabled != nil && !*vad.Enabled {
		return nil, nil
	}
	if err := applyVADType(turnDetection, provider, vad.Type); err != nil {
		return nil, err
	}
	if turnDetection.Type == "semantic_vad" {
		if err := applySemanticVAD(turnDetection, vad); err != nil {
			return nil, err
		}
	} else {
		if err := applyServerVAD(turnDetection, vad); err != nil {
			return nil, err
		}
	}
	turnDetection.CreateResponse = cloneBool(vad.CreateResponse)
	turnDetection.InterruptResponse = cloneBool(vad.InterruptResponse)
	return turnDetection, nil
}

func applyVADType(turnDetection *public.TurnDetection, provider, configured string) error {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == "" {
		return nil
	}
	if configured != "server_vad" && (provider != public.ProviderOpenAI || configured != "semantic_vad") {
		return fmt.Errorf("bare live session VAD type %q is unsupported for %s", configured, provider)
	}
	turnDetection.Type = configured
	return nil
}

func applySemanticVAD(turnDetection *public.TurnDetection, vad *public.VADConfig) error {
	if vad.Threshold != 0 || vad.PrefixPaddingMs != 0 || vad.SilenceDurationMs != 0 {
		return fmt.Errorf("semantic_vad does not support threshold, prefix_padding_ms, or silence_duration_ms")
	}
	eagerness := strings.ToLower(strings.TrimSpace(vad.Eagerness))
	switch eagerness {
	case "", "auto", "low", "medium", "high":
		turnDetection.Eagerness = eagerness
		return nil
	default:
		return fmt.Errorf("semantic_vad eagerness %q is unsupported; want auto, low, medium, or high", vad.Eagerness)
	}
}

func applyServerVAD(turnDetection *public.TurnDetection, vad *public.VADConfig) error {
	if strings.TrimSpace(vad.Eagerness) != "" {
		return fmt.Errorf("server_vad does not support eagerness")
	}
	turnDetection.Threshold = vad.Threshold
	turnDetection.PrefixPaddingMs = vad.PrefixPaddingMs
	turnDetection.SilenceDurationMs = vad.SilenceDurationMs
	return nil
}

func resolveTranscription(request public.Request) *public.Transcription {
	transcription := &public.Transcription{Enabled: true, Model: public.DefaultTranscriptionModel}
	config := sessionConfig(request.Config)
	if config == nil || config.InputTranscription == nil {
		if request.NoInputTranscription {
			return &public.Transcription{}
		}
		return transcription
	}
	if config.InputTranscription.Enabled != nil {
		transcription.Enabled = *config.InputTranscription.Enabled
	}
	if model := strings.TrimSpace(config.InputTranscription.Model); model != "" {
		transcription.Model = model
	}
	if request.NoInputTranscription {
		return &public.Transcription{}
	}
	return transcription
}

func resolveDevice(request public.Request) public.DeviceSelection {
	device := request.Device
	if session := sessionConfig(request.Config); session != nil {
		if !device.InputPresent && device.InputDevice == "" {
			device.InputDevice = session.InputDevice
		}
		if !device.OutputPresent && device.OutputDevice == "" {
			device.OutputDevice = session.OutputDevice
		}
	}
	device.InputPresent = true
	device.OutputPresent = true
	return device
}

func sessionConfig(config *public.ConfigSnapshot) *public.SessionConfig {
	if config == nil || config.Session == nil {
		return nil
	}
	return config.Session
}

func configPath(config *public.ConfigSnapshot) string {
	if config == nil {
		return ""
	}
	return config.Path
}

func cloneConfig(config *public.ConfigSnapshot) *public.ConfigSnapshot {
	if config == nil {
		return nil
	}
	copy := *config
	copy.OpenAI = cloneProvider(config.OpenAI)
	copy.Grok = cloneProvider(config.Grok)
	copy.Session = cloneSession(config.Session)
	return &copy
}

func cloneCatalog(catalog *public.ModelCatalog) *public.ModelCatalog {
	if catalog == nil {
		return nil
	}
	return &public.ModelCatalog{Models: append([]public.Model(nil), catalog.Models...)}
}

func cloneProvider(config *public.ProviderConfig) *public.ProviderConfig {
	if config == nil {
		return nil
	}
	copy := *config
	return &copy
}

func cloneSession(config *public.SessionConfig) *public.SessionConfig {
	if config == nil {
		return nil
	}
	copy := *config
	copy.VAD = cloneVAD(config.VAD)
	copy.InputTranscription = cloneTranscription(config.InputTranscription)
	return &copy
}

func cloneVAD(config *public.VADConfig) *public.VADConfig {
	if config == nil {
		return nil
	}
	copy := *config
	copy.Enabled = cloneBool(config.Enabled)
	copy.CreateResponse = cloneBool(config.CreateResponse)
	copy.InterruptResponse = cloneBool(config.InterruptResponse)
	return &copy
}

func cloneTranscription(config *public.TranscriptionConfig) *public.TranscriptionConfig {
	if config == nil {
		return nil
	}
	copy := *config
	copy.Enabled = cloneBool(config.Enabled)
	return &copy
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
