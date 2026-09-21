package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
	if cfg.SessionMessageReplay {
		if strings.TrimSpace(cfg.ReplayPath) == "" {
			return nil, errors.New("session message replay requires a capture path")
		}
		if s.replay == nil {
			return nil, errors.New("replay service is required for session message replay")
		}
		return s.replay.NewSessionInferencer(ctx, cfg.ReplayPath)
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
	var prepared runtimeReplay.LivePrepared
	var err error
	if strings.TrimSpace(cfg.ReplayPath) != "" {
		if s.replay == nil {
			return nil, errors.New("replay service is required for realtime session replay")
		}
		prepared, err = s.replay.PrepareLive(ctx, runtimeReplay.LiveRequest{
			SourcePath: cfg.ReplayPath,
			Timing:     providerReplayTiming(cfg.ReplayTiming),
		})
		if err != nil {
			return nil, err
		}
	}

	dialer, recorder, err := s.sessionDialer(cfg, providerName, model, s.clock, prepared)
	if err != nil {
		return nil, closeProviderReplay(prepared, err)
	}

	provider, err := buildSessionProvider(cfg, providerName, model, dialer)
	if err != nil {
		return nil, closeProviderReplay(prepared, err)
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		return nil, closeProviderReplay(prepared, fmt.Errorf("create realtime session gateway: %w", err))
	}
	var inferencer messages.SessionInferencer = inference.NewSessionGatewayInferencer(sessionGateway, inference.WithSessionRequest(inference.SessionRequest{Config: sessionConfig(cfg, model)}))
	if prepared != nil {
		inferencer = prepared.WrapInferencer(inferencer)
	}
	if recorder != nil {
		return s.recording.TrackSession(inferencer, recorder, cfg.RecordPath)
	}
	return inferencer, nil
}

func providerReplayTiming(value string) runtimeSession.LiveReplayTiming {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "realtime", "recorded":
		return runtimeSession.LiveReplayTimingRealtime
	default:
		return runtimeSession.LiveReplayTimingFast
	}
}

func closeProviderReplay(prepared runtimeReplay.LivePrepared, err error) error {
	if prepared == nil {
		return err
	}
	return errors.Join(err, prepared.Close())
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

func (s *Service) sessionDialer(cfg runtimeproviders.SessionConfig, provider, model string, source clock.TimerSource, preparedValue ...runtimeReplay.LivePrepared) (transport.Dialer, recording.Writer, error) {
	var prepared runtimeReplay.LivePrepared
	if len(preparedValue) > 0 {
		prepared = preparedValue[0]
	}
	dialer := cfg.WebSocketDialer
	if strings.TrimSpace(cfg.ReplayPath) != "" {
		if prepared == nil {
			return nil, nil, errors.New("prepared replay session is unavailable")
		}
		dialer = prepared.WrapDialer(nil)
		if dialer == nil {
			return nil, nil, errors.New("replay service returned an unavailable session dialer")
		}
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
	return s.recordedSessionDialer(dialer, provider, model, cfg.RecordPath, source)
}

func (s *Service) recordedSessionDialer(dialer transport.Dialer, provider, model, destination string, source clock.TimerSource) (transport.Dialer, recording.Writer, error) {
	if s.providerCapture == nil {
		return nil, nil, errors.New("provider capture service is required")
	}
	if s.recording == nil {
		return nil, nil, errors.New("recording service is required")
	}
	sink, err := s.providerCapture.OpenProviderCapture(recording.ProviderCaptureOptions{Destination: destination})
	if err != nil {
		return nil, nil, fmt.Errorf("open provider capture: %w", err)
	}
	if sink == nil {
		return nil, nil, errors.New("provider capture service returned a nil sink")
	}
	recorder, err := gatewaytesting.NewRecordingWebSocketDialerWithSink(dialer, provider, model, sink, source)
	if err != nil {
		return nil, nil, errors.Join(err, sink.Abort())
	}
	return recorder, recorder, nil
}
