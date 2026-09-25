package wire

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// deviceProbeProvider is the host-resolved realtime provider selection for
// one device probe. Values come from the CLI configuration snapshot and the
// probe's explicit overrides.
type deviceProbeProvider struct {
	provider        string
	model           string
	apiKey          string
	baseURL         string
	reasoningEffort string
}

// NewDeviceProbeSessionFactory resolves a device probe's provider values at
// the CLI configuration edge and delegates realtime session construction to
// the provider service. Probe sessions exchange PCM16 audio in both
// directions at the realtime default rate so a device bridge never depends on
// a later control message to change the wire contract.
func NewDeviceProbeSessionFactory(providerService runtimeProviders.SessionService, audioService audioio.Service) serviceDevices.DeviceProbeSessionFactory {
	return func(request serviceDevices.DeviceProbeRequest, instructions string) (messages.SessionInferencer, string, error) {
		if providerService == nil || audioService == nil {
			return nil, "", errors.New("device probe provider and audio services are required")
		}
		resolved, err := resolveDeviceProbeProvider(request)
		if err != nil {
			return nil, "", err
		}
		transcription := audioService.ResolveTranscription(audioio.TranscriptionRequest{Provider: resolved.provider, AcceptsAudioInput: true})
		inferencer, err := providerService.BuildSession(context.Background(), runtimeProviders.SessionConfig{
			Provider: resolved.provider, Model: resolved.model, APIKey: resolved.apiKey, BaseURL: resolved.baseURL,
			Instructions: instructions, ReasoningEffort: resolved.reasoningEffort,
			InputAudioFormat: models.AudioFormatPCM16, OutputAudioFormat: models.AudioFormatPCM16,
			InputAudioSampleRate: models.SampleRate24000, OutputAudioSampleRate: models.SampleRate24000,
			InputTranscription: &models.InputAudioTranscriptionConfig{Enabled: transcription.Enabled, Model: transcription.Model},
			WebSocketDialer:    request.WebSocketDialer,
		})
		if err != nil {
			return nil, "", err
		}
		return inferencer, resolved.model, nil
	}
}

func resolveDeviceProbeProvider(request serviceDevices.DeviceProbeRequest) (deviceProbeProvider, error) {
	storage, err := config.NewDefaultConfigStorage(request.ConfigDir)
	if err != nil {
		return deviceProbeProvider{}, fmt.Errorf("failed to initialize config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return deviceProbeProvider{}, fmt.Errorf("failed to load config: %w", err)
	}
	provider := deviceProbeProviderName(request.Provider, loaded)
	effective := loaded.ApplyOverrides(request.APIKey, request.Model, provider, request.BaseURL)
	switch provider {
	case config.ProviderOpenAI:
		return openAIDeviceProbeProvider(effective, loaded, request.Model)
	case config.ProviderGrok:
		if err := effective.ValidateGrokSession(); err != nil {
			return deviceProbeProvider{}, err
		}
		active, err := effective.ActiveGrokConfig()
		if err != nil {
			return deviceProbeProvider{}, err
		}
		return deviceProbeProvider{provider: provider, model: active.Model, apiKey: active.APIKey, baseURL: active.BaseURL}, nil
	default:
		return deviceProbeProvider{}, fmt.Errorf("--devices real supports realtime providers %q and %q; got %q", config.ProviderOpenAI, config.ProviderGrok, provider)
	}
}

// deviceProbeProviderName applies the live-session provider precedence: an
// explicit provider, the session-specific persisted provider, a
// realtime-capable ordinary model provider, and finally OpenAI.
func deviceProbeProviderName(requested string, cfg *config.Config) string {
	if provider := strings.ToLower(strings.TrimSpace(requested)); provider != "" {
		return provider
	}
	if cfg.Session != nil {
		if provider := strings.ToLower(strings.TrimSpace(cfg.Session.Provider)); provider != "" {
			return provider
		}
	}
	if provider := strings.ToLower(strings.TrimSpace(cfg.Model.Provider)); provider == config.ProviderGrok {
		return provider
	}
	return config.ProviderOpenAI
}

func openAIDeviceProbeProvider(effective config.Config, loaded *config.Config, requestedModel string) (deviceProbeProvider, error) {
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return deviceProbeProvider{}, err
	}
	model := strings.TrimSpace(active.Model)
	if requestedModel == "" && (loaded.Model.OpenAI == nil || model == "") {
		model = runtimeProviders.OpenAIRealtimeDefaultModel
	}
	if strings.TrimSpace(active.APIKey) == "" {
		return deviceProbeProvider{}, fmt.Errorf("openai realtime api key is missing: OpenAI API key is required for live realtime session mode (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s)", config.ConfigFileName)
	}
	resolved := deviceProbeProvider{provider: config.ProviderOpenAI, model: model, apiKey: active.APIKey, baseURL: active.BaseURL}
	if loaded.Session != nil {
		resolved.reasoningEffort = strings.TrimSpace(loaded.Session.ReasoningEffort)
	}
	return resolved, nil
}
