package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission"
	bareadmissionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission/wire"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

var (
	// ErrBareSessionCredentialMissing classifies the preflight error returned
	// when the default OpenAI live session has no credential. The compatibility
	// adapter returns this before device or provider setup.
	ErrBareSessionCredentialMissing = bareadmission.ErrCredentialMissing
	// ErrUnsupportedBareSessionProvider classifies providers that cannot be
	// used by the live session runtime.
	ErrUnsupportedBareSessionProvider = bareadmission.ErrUnsupportedProvider
)

// BareSessionCredentialError is the redacted, actionable missing-credential
// error retained by the compatibility surface.
type BareSessionCredentialError = bareadmission.CredentialError

// ResolveBareSessionOptions is the legacy CLI-edge adapter for the public
// bareadmission service.
//
// Deprecated: use go-agent-runtime/services/bareadmission through its public
// Wire constructor. This adapter remains only while CLI callers migrate; it
// loads CLI-owned inputs, snapshots them, delegates policy, and maps the
// result back into SessionRunOptions.
func ResolveBareSessionOptions(opts SessionRunOptions) (SessionRunOptions, error) {
	loadedCfg, configPath, err := loadBareSessionConfig(opts)
	if err != nil {
		return SessionRunOptions{}, err
	}

	environmentAPIKey, _ := os.LookupEnv("OPENAI_API_KEY")
	request := bareadmission.Request{
		Provider:                opts.Provider,
		Model:                   opts.Model,
		ModelProvided:           opts.ModelProvided,
		APIKey:                  opts.APIKey,
		BaseURL:                 opts.BaseURL,
		Transport:               opts.Transport,
		TransportProvided:       opts.TransportProvided,
		NoInputTranscription:    opts.NoInputTranscription,
		EnvironmentOpenAIAPIKey: environmentAPIKey,
		Config:                  snapshotBareSessionConfig(loadedCfg, configPath),
		Catalog:                 snapshotBareSessionCatalog(opts.ModelCatalog),
		Device: bareadmission.DeviceSelection{
			InputDevice:   opts.RTCDeviceBinding.InputDevice,
			OutputDevice:  opts.RTCDeviceBinding.OutputDevice,
			InputPresent:  opts.RTCDeviceBinding.InputPresent,
			OutputPresent: opts.RTCDeviceBinding.OutputPresent,
		},
	}

	result, err := bareadmissionwire.NewService().Resolve(context.Background(), request)
	if err != nil {
		return SessionRunOptions{}, mapBareSessionAdmissionError(err)
	}

	resolved := opts
	resolved.LoadedConfig = loadedCfg
	resolved.BareLive = result.BareLive
	resolved.Provider = result.Provider
	resolved.Model = result.Model
	resolved.APIKey = result.APIKey
	resolved.BaseURL = result.BaseURL
	resolved.Transport = result.Transport
	resolved.TurnDetection = mapBareSessionTurnDetection(result.TurnDetection)
	resolved.InputAudioTranscription = mapBareSessionTranscription(result.InputAudioTranscription)
	resolved.RTCDeviceBinding.InputDevice = result.Device.InputDevice
	resolved.RTCDeviceBinding.OutputDevice = result.Device.OutputDevice
	resolved.RTCDeviceBinding.InputPresent = result.Device.InputPresent
	resolved.RTCDeviceBinding.OutputPresent = result.Device.OutputPresent
	return resolved, nil
}

func mapBareSessionAdmissionError(err error) error {
	var invalidTransport *bareadmission.InvalidTransportError
	if errors.As(err, &invalidTransport) {
		return fmt.Errorf("%w: %q (want %q or %q)", ErrInvalidSessionTransport, invalidTransport.Transport, SessionTransportWebSocket, SessionTransportWebRTC)
	}
	var unsupportedModel *bareadmission.UnsupportedRealtimeModelError
	if errors.As(err, &unsupportedModel) {
		return &runtimeproviders.UnsupportedRealtimeModelError{
			Provider:        unsupportedModel.Provider,
			Model:           unsupportedModel.Model,
			SupportedModels: append([]string(nil), unsupportedModel.SupportedModels...),
		}
	}
	if errors.Is(err, bareadmission.ErrModelCatalogRequired) {
		return fmt.Errorf("%w: OpenAI realtime model admission", runtimeproviders.ErrModelCatalogRequired)
	}
	return err
}

func mapBareSessionTurnDetection(value *bareadmission.TurnDetection) *models.TurnDetectionConfig {
	if value == nil {
		return nil
	}
	result := &models.TurnDetectionConfig{
		Type:              value.Type,
		Threshold:         value.Threshold,
		PrefixPaddingMs:   value.PrefixPaddingMs,
		SilenceDurationMs: value.SilenceDurationMs,
		Eagerness:         value.Eagerness,
	}
	if value.CreateResponse != nil {
		createResponse := *value.CreateResponse
		result.CreateResponse = &createResponse
	}
	if value.InterruptResponse != nil {
		interruptResponse := *value.InterruptResponse
		result.InterruptResponse = &interruptResponse
	}
	return result
}

func mapBareSessionTranscription(value *bareadmission.Transcription) *models.InputAudioTranscriptionConfig {
	if value == nil {
		return nil
	}
	return &models.InputAudioTranscriptionConfig{Enabled: value.Enabled, Model: value.Model}
}

func snapshotBareSessionCatalog(catalog runtimeproviders.ModelCatalog) *bareadmission.ModelCatalog {
	if catalog == nil {
		return nil
	}
	entries := catalog.RealtimeModels(sessionProviderOpenAI)
	modelsSnapshot := make([]bareadmission.Model, 0, len(entries))
	for _, entry := range entries {
		modelsSnapshot = append(modelsSnapshot, bareadmission.Model{
			Provider:                sessionProviderOpenAI,
			ID:                      entry.ID,
			SupportsAudio:           entry.SupportsAudio,
			SupportsImageInput:      entry.SupportsImageInput,
			SupportsFunctionCalling: entry.SupportsFunctionCalling,
			SupportsReasoning:       entry.SupportsReasoning,
		})
	}
	return &bareadmission.ModelCatalog{Models: modelsSnapshot}
}

func snapshotBareSessionConfig(loadedCfg *config.Config, configPath string) *bareadmission.ConfigSnapshot {
	snapshot := &bareadmission.ConfigSnapshot{Path: configPath}
	if loadedCfg == nil {
		return snapshot
	}
	snapshot.ModelProvider = loadedCfg.Model.Provider
	if loadedCfg.Model.OpenAI != nil {
		snapshot.OpenAI = &bareadmission.ProviderConfig{
			Model: loadedCfg.Model.OpenAI.Model, APIKey: loadedCfg.Model.OpenAI.APIKey, BaseURL: loadedCfg.Model.OpenAI.BaseURL,
		}
	}
	if loadedCfg.Model.Grok != nil {
		snapshot.Grok = &bareadmission.ProviderConfig{
			Model: loadedCfg.Model.Grok.Model, APIKey: loadedCfg.Model.Grok.APIKey, BaseURL: loadedCfg.Model.Grok.BaseURL,
		}
	}
	if loadedCfg.Session != nil {
		snapshot.Session = &bareadmission.SessionConfig{
			Provider: loadedCfg.Session.Provider, Model: loadedCfg.Session.Model, Transport: loadedCfg.Session.Transport,
			InputDevice: loadedCfg.Session.InputDevice, OutputDevice: loadedCfg.Session.OutputDevice,
		}
		if loadedCfg.Session.VAD != nil {
			vad := loadedCfg.Session.VAD
			snapshot.Session.VAD = &bareadmission.VADConfig{
				Enabled: cloneBareBool(vad.Enabled), Type: vad.Type, Threshold: vad.Threshold,
				PrefixPaddingMs: vad.PrefixPaddingMs, SilenceDurationMs: vad.SilenceDurationMs,
				CreateResponse: cloneBareBool(vad.CreateResponse), InterruptResponse: cloneBareBool(vad.InterruptResponse), Eagerness: vad.Eagerness,
			}
		}
		if loadedCfg.Session.InputTranscription != nil {
			transcription := loadedCfg.Session.InputTranscription
			snapshot.Session.InputTranscription = &bareadmission.TranscriptionConfig{Enabled: cloneBareBool(transcription.Enabled), Model: transcription.Model}
		}
	}
	return snapshot
}

func cloneBareBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// loadBareSessionConfig is the sole CLI-owned config/file boundary for the
// compatibility adapter. The public service receives only the resulting
// snapshot and never calls this helper.
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
