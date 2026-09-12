package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	grokprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	openaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	defaultOpenAIModel = "gpt-realtime"
	defaultOpenAIURL   = "wss://api.openai.com/v1/realtime"
	defaultGrokModel   = "grok-realtime"
)

type Service struct{ deps providersession.Dependencies }

func New(deps providersession.Dependencies) *Service { return &Service{deps: deps} }

func (s *Service) PlanRecord(ctx context.Context, req providersession.RecordRequest) (providersession.Plan, error) {
	if err := checkContext(ctx); err != nil {
		return providersession.Plan{}, err
	}
	provider, err := providerName(req.Provider)
	if err != nil {
		return providersession.Plan{}, err
	}
	model, err := recordModel(provider, req.Model)
	if err != nil {
		return providersession.Plan{}, err
	}
	clientOwned := req.ClientOwnsAudioTurnBoundaries || len(req.AudioInputs) > 0
	transcription := recordTranscription(req, provider, clientOwned || req.AudioInputAvailable)
	recorder, err := s.recordingDialerForPlan(req, provider, model)
	if err != nil {
		return providersession.Plan{}, err
	}
	turnDetection := recordTurnDetection(req, provider, clientOwned)
	build := recordBuildRequest(req, provider, model, recorder, transcription, clientOwned)
	plan := providersession.Plan{
		Mode: recordMode(provider), Provider: provider, Model: model, CapturePath: req.RecordPath,
		Announcement: fmt.Sprintf("Starting %s realtime session recording to %s", displayProvider(provider), req.RecordPath),
		Build:        build, Dialer: recorder, TurnDetection: turnDetection, Prompt: req.Prompt,
		CloseAfterOpen:           !req.WaitForClose && len(req.AudioInputs) == 0,
		WaitForClose:             req.WaitForClose || len(req.AudioInputs) > 0,
		CloseAfterScheduledAudio: len(req.AudioInputs) > 0, RequireSessionUpdated: len(req.AudioInputs) > 0 && provider == providersession.ProviderOpenAI,
		AudioInputs: cloneAudioInputs(req.AudioInputs), FlushCapture: func() error { return recorder.FlushToFile(req.RecordPath) },
		FlushCaptureTo: recorder.FlushToFile,
		Finalize: func(_ context.Context, out io.Writer) error {
			_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", req.RecordPath)
			return err
		},
	}
	inferencer, err := s.newInferencer(ctx, build)
	if err != nil {
		return providersession.Plan{}, err
	}
	plan.Inferencer = inferencer
	plan.ApplyTurnDetection(inferencer)
	return plan, nil
}

func recordModel(provider, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" && provider == providersession.ProviderOpenAI {
		model = defaultOpenAIModel
	}
	if model == "" {
		return "", fmt.Errorf("%s session runtime requires a model", provider)
	}
	return model, nil
}

func (s *Service) recordingDialerForPlan(req providersession.RecordRequest, provider, model string) (providersession.RecordingDialer, error) {
	live := req.WebSocketDialer
	if live == nil {
		live = s.defaultDialer(provider)
	}
	if live == nil {
		return nil, fmt.Errorf("%s %w", provider, providersession.ErrMissingDialer)
	}
	if req.ObserveDialer != nil {
		if observed := req.ObserveDialer(live); observed != nil {
			live = observed
		}
	}
	recorder := s.recordingDialer(live, provider, model)
	if recorder == nil {
		return nil, fmt.Errorf("%s session runtime recording dialer is nil", provider)
	}
	return recorder, nil
}

func recordTurnDetection(req providersession.RecordRequest, provider string, clientOwned bool) *models.TurnDetectionConfig {
	turnDetection := cloneTurnDetection(req.TurnDetection)
	if turnDetection == nil && req.AudioInputAvailable && !clientOwned && provider == providersession.ProviderOpenAI {
		turnDetection = &models.TurnDetectionConfig{Type: "semantic_vad"}
	}
	return turnDetection
}

func recordBuildRequest(req providersession.RecordRequest, provider, model string, recorder providersession.RecordingDialer, transcription models.InputAudioTranscriptionConfig, clientOwned bool) providersession.BuildRequest {
	return providersession.BuildRequest{
		Provider: provider, Model: model, APIKey: req.APIKey, BaseURL: req.BaseURL,
		ReasoningEffort: req.ReasoningEffort, Voice: req.Voice, Dialer: recorder,
		ToolDefinitions: cloneTools(req.ToolDefinitions), InputAudioTranscription: transcription,
		ClientOwnsAudioTurnBoundaries: clientOwned,
	}
}

func (s *Service) newInferencer(ctx context.Context, build providersession.BuildRequest) (messages.SessionInferencer, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if s.deps.NewInferencer != nil {
		return s.deps.NewInferencer(build)
	}
	if build.Provider == providersession.ProviderOpenAI {
		return s.BuildOpenAI(ctx, build)
	}
	return s.BuildGrok(ctx, build)
}

func (s *Service) BuildOpenAI(ctx context.Context, req providersession.BuildRequest) (messages.SessionInferencer, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if req.Dialer == nil {
		return nil, fmt.Errorf("%s %w", providersession.ProviderOpenAI, providersession.ErrMissingDialer)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultOpenAIModel
	}
	opts := []openaiprovider.Option{
		openaiprovider.WithAPIKey(req.APIKey), openaiprovider.WithModel(model),
		openaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(req.BaseURL, model)),
		openaiprovider.WithWebSocketDialer(req.Dialer),
	}
	if req.ClientOwnsAudioTurnBoundaries {
		opts = append(opts, openaiprovider.WithClientOwnedAudioTurnBoundaries())
	}
	providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(openaiprovider.New(opts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(model),
		inference.WithSessionInputAudioTranscription(req.InputAudioTranscription),
	}
	if req.Voice != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionVoice(req.Voice))
	}
	if len(req.ToolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(req.ToolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(providerGateway, inferenceOpts...), nil
}

func (s *Service) BuildGrok(ctx context.Context, req providersession.BuildRequest) (messages.SessionInferencer, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if req.Dialer == nil {
		return nil, fmt.Errorf("%s %w", providersession.ProviderGrok, providersession.ErrMissingDialer)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultGrokModel
	}
	opts := []grokprovider.Option{grokprovider.WithAPIKey(req.APIKey), grokprovider.WithWebSocketDialer(req.Dialer)}
	if strings.TrimSpace(req.BaseURL) != "" {
		opts = append(opts, grokprovider.WithBaseURL(req.BaseURL))
	}
	providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grokprovider.New(opts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{inference.WithSessionModel(model)}
	if len(req.ToolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(req.ToolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(providerGateway, inferenceOpts...), nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("provider session requires a context")
	}
	return ctx.Err()
}

func providerName(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider != providersession.ProviderOpenAI && provider != providersession.ProviderGrok {
		return "", fmt.Errorf("realtime sessions do not support provider %q", provider)
	}
	return provider, nil
}

func recordMode(provider string) string {
	if provider == providersession.ProviderOpenAI {
		return providersession.ModeRecordOpenAI
	}
	return providersession.ModeRecordGrok
}

func displayProvider(provider string) string {
	if provider == providersession.ProviderOpenAI {
		return "OpenAI"
	}
	return "Grok"
}

func (s *Service) defaultDialer(provider string) transport.Dialer {
	if s.deps.NewDefaultDialer != nil {
		return s.deps.NewDefaultDialer(provider)
	}
	if provider == providersession.ProviderOpenAI {
		return openaiprovider.NewDefaultWebSocketDialer()
	}
	return grokprovider.NewDefaultWebSocketDialer()
}

func (s *Service) recordingDialer(inner transport.Dialer, provider, model string) providersession.RecordingDialer {
	if s.deps.NewRecordingDialer != nil {
		return s.deps.NewRecordingDialer(inner, provider, model)
	}
	return gwtesting.NewRecordingWebSocketDialer(inner, provider, model)
}

func recordTranscription(req providersession.RecordRequest, provider string, audio bool) models.InputAudioTranscriptionConfig {
	if !audio || req.NoInputTranscription || provider != providersession.ProviderOpenAI {
		return models.InputAudioTranscriptionConfig{}
	}
	if req.InputAudioTranscription != nil {
		return *req.InputAudioTranscription
	}
	return models.InputAudioTranscriptionConfig{Enabled: true, Model: models.DefaultInputAudioTranscriptionModel}
}

func openAIRealtimeURL(base, model string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultOpenAIURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func cloneTools(tools []messages.ToolDefinition) []messages.ToolDefinition {
	if tools == nil {
		return nil
	}
	return append([]messages.ToolDefinition(nil), tools...)
}

func cloneAudioInputs(inputs []providersession.AudioInput) []providersession.AudioInput {
	if inputs == nil {
		return nil
	}
	copy := make([]providersession.AudioInput, len(inputs))
	for i, input := range inputs {
		copy[i] = input
		copy[i].PCM = append([]byte(nil), input.PCM...)
	}
	return copy
}

func cloneTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.CreateResponse != nil {
		value := *policy.CreateResponse
		copy.CreateResponse = &value
	}
	if policy.InterruptResponse != nil {
		value := *policy.InterruptResponse
		copy.InterruptResponse = &value
	}
	return &copy
}
