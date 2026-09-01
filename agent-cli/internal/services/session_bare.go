package services

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

var (
	// ErrBareSessionCredentialMissing classifies the preflight error returned
	// when the default OpenAI live session has no credential. The resolver
	// returns this before device or provider setup.
	ErrBareSessionCredentialMissing = errors.New("bare live session credential is missing")
	// ErrUnsupportedBareSessionProvider classifies providers that cannot be
	// used by the live session runtime.
	ErrUnsupportedBareSessionProvider = errors.New("unsupported bare live session provider")
)

// BareSessionCredentialError is the single actionable missing-credential
// error for a bare OpenAI session. It deliberately contains only the
// effective config path, never a candidate credential.
type BareSessionCredentialError struct {
	ConfigPath string
}

func (e *BareSessionCredentialError) Error() string {
	if e == nil {
		return ErrBareSessionCredentialMissing.Error()
	}
	return fmt.Sprintf(
		"bare live session requires an OpenAI API key: set OPENAI_API_KEY, pass --api-key, or configure model.openai.api_key in %s",
		e.ConfigPath,
	)
}

func (e *BareSessionCredentialError) Unwrap() error {
	return ErrBareSessionCredentialMissing
}

// ResolveBareSessionOptions resolves the request-scoped settings for the
// alternate-free live session. It copies options and policy values rather
// than mutating the loaded configuration snapshot, and performs no device or
// provider acquisition.
//
// The precedence is explicit option values, the session-specific persisted
// block, supported provider values in the ordinary model config, the
// conventional OPENAI_API_KEY fallback, and finally the bare-session
// built-ins. ConfigStorage has already merged AGENT_* environment values above
// the file, so the returned snapshot preserves that ordering.
func ResolveBareSessionOptions(opts SessionRunOptions) (SessionRunOptions, error) {
	loadedCfg, configPath, err := loadBareSessionConfig(opts)
	if err != nil {
		return SessionRunOptions{}, err
	}

	resolved := opts
	resolved.LoadedConfig = loadedCfg
	resolved.BareLive = true

	provider := resolveRealtimeSessionProvider(opts, loadedCfg)
	if provider != sessionProviderOpenAI && provider != sessionProviderGrok {
		return SessionRunOptions{}, fmt.Errorf("%w: %q (bare sessions support %q and %q)", ErrUnsupportedBareSessionProvider, provider, sessionProviderOpenAI, sessionProviderGrok)
	}
	resolved.Provider = provider

	model := strings.TrimSpace(opts.Model)
	if opts.ModelProvided && model == "" {
		return SessionRunOptions{}, unsupportedOpenAIRealtimeModelError(model)
	}
	if model == "" && loadedCfg.Session != nil {
		model = strings.TrimSpace(loadedCfg.Session.Model)
	}
	providerCfg := bareProviderConfig(loadedCfg, provider)
	if model == "" && providerCfg != nil {
		model = strings.TrimSpace(providerCfg.Model)
	}
	if model == "" && provider == sessionProviderOpenAI {
		model = openAIRealtimeModel
	}
	if model == "" {
		return SessionRunOptions{}, fmt.Errorf("%s session model is required for bare live session (configure model.%s.model or session.model in %s)", provider, provider, configPath)
	}
	if provider == sessionProviderOpenAI && !isOpenAIRealtimeModel(model) {
		return SessionRunOptions{}, unsupportedOpenAIRealtimeModelError(model)
	}
	resolved.Model = model

	if strings.TrimSpace(opts.APIKey) == "" {
		if providerCfg != nil && strings.TrimSpace(providerCfg.APIKey) != "" {
			resolved.APIKey = providerCfg.APIKey
		} else if provider == sessionProviderOpenAI {
			if fallback, ok := os.LookupEnv("OPENAI_API_KEY"); ok && strings.TrimSpace(fallback) != "" {
				resolved.APIKey = fallback
			}
		}
	}
	if strings.TrimSpace(resolved.APIKey) == "" {
		if provider == sessionProviderOpenAI {
			return SessionRunOptions{}, &BareSessionCredentialError{ConfigPath: configPath}
		}
		return SessionRunOptions{}, fmt.Errorf("%s API key is required for bare live session (set AGENT_MODEL__%s__API_KEY, pass --api-key, or configure model.%s.api_key in %s)", provider, strings.ToUpper(provider), provider, configPath)
	}
	if strings.TrimSpace(resolved.BaseURL) == "" && providerCfg != nil {
		resolved.BaseURL = providerCfg.BaseURL
	}

	resolved.Transport = resolveBareSessionTransport(opts, loadedCfg)
	if resolved.Transport != SessionTransportWebSocket && resolved.Transport != SessionTransportWebRTC {
		return SessionRunOptions{}, fmt.Errorf("%w: %q (want %q or %q)", ErrInvalidSessionTransport, resolved.Transport, SessionTransportWebSocket, SessionTransportWebRTC)
	}

	resolved.TurnDetection, err = resolveBareSessionTurnDetection(loadedCfg.Session)
	if err != nil {
		return SessionRunOptions{}, err
	}
	transcription := resolveBareSessionTranscription(loadedCfg.Session)
	if opts.NoInputTranscription {
		transcription.Enabled = false
		transcription.Model = ""
	}
	resolved.InputAudioTranscription = &transcription

	if loadedCfg.Session != nil {
		if !resolved.RTCDeviceBinding.InputPresent && resolved.RTCDeviceBinding.InputDevice == "" {
			resolved.RTCDeviceBinding.InputDevice = loadedCfg.Session.InputDevice
		}
		if !resolved.RTCDeviceBinding.OutputPresent && resolved.RTCDeviceBinding.OutputDevice == "" {
			resolved.RTCDeviceBinding.OutputDevice = loadedCfg.Session.OutputDevice
		}
	}
	// Bare mode intentionally selects both directions. Empty and "default"
	// selectors are left intact for the shared registry resolver.
	resolved.RTCDeviceBinding.InputPresent = true
	resolved.RTCDeviceBinding.OutputPresent = true

	return resolved, nil
}

func loadBareSessionConfig(opts SessionRunOptions) (*config.Config, string, error) {
	if opts.LoadedConfig != nil {
		configPath := opts.LoadedConfig.ConfigPath
		if configPath == "" {
			storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
			if err != nil {
				return nil, "", fmt.Errorf("failed to initialize config: %w", err)
			}
			configPath = storage.Path()
		}
		return opts.LoadedConfig, configPath, nil
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err != nil {
		return nil, "", fmt.Errorf("failed to initialize config: %w", err)
	}
	loadedCfg, err := storage.Load()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load config: %w", err)
	}
	return loadedCfg, storage.Path(), nil
}

func bareProviderConfig(cfg *config.Config, provider string) *config.OpenAIConfig {
	if cfg == nil {
		return nil
	}
	switch provider {
	case sessionProviderOpenAI:
		return cfg.Model.OpenAI
	case sessionProviderGrok:
		if cfg.Model.Grok == nil {
			return nil
		}
		return &config.OpenAIConfig{
			Model:   cfg.Model.Grok.Model,
			APIKey:  cfg.Model.Grok.APIKey,
			BaseURL: cfg.Model.Grok.BaseURL,
		}
	default:
		return nil
	}
}

func resolveBareSessionTransport(opts SessionRunOptions, cfg *config.Config) string {
	transport := strings.ToLower(strings.TrimSpace(opts.Transport))
	if !opts.TransportProvided && (transport == "" || transport == SessionTransportWebSocket) && cfg != nil && cfg.Session != nil {
		if configured := strings.ToLower(strings.TrimSpace(cfg.Session.Transport)); configured != "" {
			transport = configured
		}
	}
	if transport == "" {
		transport = SessionTransportWebSocket
	}
	return transport
}

func resolveBareSessionTurnDetection(cfg *config.SessionConfig) (*models.TurnDetectionConfig, error) {
	turnDetection := &models.TurnDetectionConfig{Type: "server_vad"}
	if cfg == nil || cfg.VAD == nil {
		return turnDetection, nil
	}
	if cfg.VAD.Enabled != nil && !*cfg.VAD.Enabled {
		return nil, nil
	}
	if configuredType := strings.TrimSpace(cfg.VAD.Type); configuredType != "" {
		if !strings.EqualFold(configuredType, "server_vad") {
			return nil, fmt.Errorf("bare live session VAD type %q is unsupported; want %q", configuredType, "server_vad")
		}
		turnDetection.Type = configuredType
	}
	turnDetection.Threshold = cfg.VAD.Threshold
	turnDetection.PrefixPaddingMs = cfg.VAD.PrefixPaddingMs
	turnDetection.SilenceDurationMs = cfg.VAD.SilenceDurationMs
	if cfg.VAD.CreateResponse != nil {
		createResponse := *cfg.VAD.CreateResponse
		turnDetection.CreateResponse = &createResponse
	}
	return turnDetection, nil
}

func resolveBareSessionTranscription(cfg *config.SessionConfig) models.InputAudioTranscriptionConfig {
	transcription := models.InputAudioTranscriptionConfig{
		Enabled: true,
		Model:   models.DefaultInputAudioTranscriptionModel,
	}
	if cfg == nil || cfg.InputTranscription == nil {
		return transcription
	}
	if cfg.InputTranscription.Enabled != nil {
		transcription.Enabled = *cfg.InputTranscription.Enabled
	}
	if model := strings.TrimSpace(cfg.InputTranscription.Model); model != "" {
		transcription.Model = model
	}
	return transcription
}
