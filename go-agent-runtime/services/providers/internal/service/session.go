package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	grokprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	openaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	providerOpenAI           = "openai"
	defaultRealtimeOpenAIURL = "wss://api.openai.com/v1/realtime"
	defaultRealtimeGrokURL   = "wss://api.x.ai/v1/realtime"
)

var _ runtimeproviders.SessionService = (*Service)(nil)

// BuildSession constructs a gateway-backed persistent session. Provider
// protocol details stay here, behind the provider service contract, so the
// session owner and CLI host do not need to know how realtime transports are
// assembled.
func (s *Service) BuildSession(ctx context.Context, cfg runtimeproviders.SessionConfig) (messages.SessionInferencer, error) {
	if ctx == nil {
		return nil, errors.New("realtime session requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	providerName := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if providerName == "" {
		providerName = providerOpenAI
	}
	model := strings.TrimSpace(cfg.Model)
	if err := s.ValidateSessionModel(providerName, model); err != nil {
		return nil, err
	}
	if model == "" {
		return nil, fmt.Errorf("realtime provider %q requires a model", providerName)
	}

	if err := validateSessionCredential(cfg, providerName); err != nil {
		return nil, err
	}

	dialer, recorder, err := s.sessionDialer(cfg, providerName, model, s.clock)
	if err != nil {
		return nil, err
	}

	provider, err := buildSessionProvider(cfg, providerName, model, dialer)
	if err != nil {
		return nil, err
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		return nil, fmt.Errorf("create realtime session gateway: %w", err)
	}
	inferencer := inference.NewSessionGatewayInferencer(sessionGateway, inference.WithSessionRequest(inference.SessionRequest{Config: sessionConfig(cfg, model)}))
	if recorder != nil {
		return s.recording.TrackSession(inferencer, recorder, cfg.RecordPath)
	}
	return inferencer, nil
}

func buildSessionProvider(cfg runtimeproviders.SessionConfig, providerName, model string, dialer transport.Dialer) (llmproviders.SessionProvider, error) {
	switch providerName {
	case providerOpenAI, "openrouter", "local", "":
		options := []openaiprovider.Option{
			openaiprovider.WithAPIKey(cfg.APIKey),
			openaiprovider.WithModel(model),
			openaiprovider.WithWebSocketDialer(dialer),
		}
		if cfg.ReplayPath != "" {
			options = append(options, openaiprovider.WithSessionWriteBackpressure())
		}
		if cfg.ClientOwnsAudioTurnBoundaries {
			options = append(options, openaiprovider.WithClientOwnedAudioTurnBoundaries())
		}
		realtimeURL := strings.TrimSpace(cfg.RealtimeURL)
		if realtimeURL == "" {
			realtimeURL = strings.TrimSpace(cfg.BaseURL)
		}
		if realtimeURL == "" {
			realtimeURL = defaultRealtimeOpenAIURL
		}
		options = append(options, openaiprovider.WithRealtimeBaseURL(realtimeURL))
		return openaiprovider.New(options...), nil
	case "grok":
		options := []grokprovider.Option{
			grokprovider.WithAPIKey(cfg.APIKey),
			grokprovider.WithWebSocketDialer(dialer),
		}
		baseURL := strings.TrimSpace(cfg.RealtimeURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(cfg.BaseURL)
		}
		if baseURL == "" {
			baseURL = defaultRealtimeGrokURL
		}
		options = append(options, grokprovider.WithBaseURL(baseURL))
		return grokprovider.New(options...), nil
	default:
		return nil, fmt.Errorf("realtime sessions do not support provider %q", providerName)
	}

}

func sessionConfig(cfg runtimeproviders.SessionConfig, model string) models.SessionConfig {
	inputFormat := cfg.InputAudioFormat
	if inputFormat == "" {
		inputFormat = models.AudioFormatPCM16
	}
	outputFormat := cfg.OutputAudioFormat
	if outputFormat == "" {
		outputFormat = models.AudioFormatPCM16
	}
	inputRate := cfg.InputAudioSampleRate
	if inputRate <= 0 {
		inputRate = models.SampleRate24000
	}
	outputRate := cfg.OutputAudioSampleRate
	if outputRate <= 0 {
		outputRate = models.SampleRate24000
	}
	config := models.SessionConfig{
		Model:                   model,
		Modalities:              []models.SessionModality{models.SessionModalityAudio},
		Voice:                   cfg.Voice,
		Instructions:            cfg.Instructions,
		ReasoningEffort:         cfg.ReasoningEffort,
		InputAudioFormat:        inputFormat,
		OutputAudioFormat:       outputFormat,
		InputAudioSampleRate:    inputRate,
		OutputAudioSampleRate:   outputRate,
		TurnDetection:           cloneTurnDetection(cfg.TurnDetection),
		InputAudioTranscription: cloneInputTranscription(cfg.InputTranscription),
		Tools:                   messages.CanonicalToolDefinitions(cfg.Tools),
	}
	return config
}

func cloneTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.CreateResponse != nil {
		createResponse := *policy.CreateResponse
		copy.CreateResponse = &createResponse
	}
	if policy.InterruptResponse != nil {
		interruptResponse := *policy.InterruptResponse
		copy.InterruptResponse = &interruptResponse
	}
	return &copy
}

func cloneInputTranscription(policy *models.InputAudioTranscriptionConfig) *models.InputAudioTranscriptionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	return &copy
}

func (s *Service) sessionDialer(cfg runtimeproviders.SessionConfig, provider, model string, source clock.TimerSource) (transport.Dialer, recording.Writer, error) {
	dialer := cfg.WebSocketDialer
	if strings.TrimSpace(cfg.ReplayPath) != "" {
		configuration, err := loadReplaySessionConfiguration(cfg.ReplayPath)
		if err != nil {
			return nil, nil, err
		}
		replayOptions := []gatewaytesting.ReplayWebSocketDialerOption{gatewaytesting.WithReplayClock(source)}
		if strings.EqualFold(strings.TrimSpace(cfg.ReplayTiming), "realtime") || strings.EqualFold(strings.TrimSpace(cfg.ReplayTiming), "recorded") {
			replayOptions = append(replayOptions, gatewaytesting.WithRecordedSessionTiming())
		}
		replay, err := gatewaytesting.NewReplayWebSocketDialer(cfg.ReplayPath, replayOptions...)
		if err != nil {
			return nil, nil, fmt.Errorf("load realtime session replay %s: %w", cfg.ReplayPath, err)
		}
		dialer = &replayInitialSessionUpdateDialer{inner: replay, payload: configuration.payload}
	}
	if dialer == nil {
		switch provider {
		case providerOpenAI, "openrouter", "local", "":
			dialer = openaiprovider.NewDefaultWebSocketDialer()
		case "grok":
			dialer = grokprovider.NewDefaultWebSocketDialer()
		default:
			return nil, nil, fmt.Errorf("realtime sessions do not support provider %q", provider)
		}
	}
	if strings.TrimSpace(cfg.RecordPath) == "" {
		return dialer, nil, nil
	}
	if s.recording == nil {
		return nil, nil, errors.New("recording service is required")
	}
	recorder := gatewaytesting.NewRecordingWebSocketDialer(dialer, provider, model, source)
	return recorder, recorder, nil
}
