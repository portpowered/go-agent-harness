package anthropic

import (
	"context"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// Default model when none is specified.
const DefaultModel = "claude-sonnet-4-20250514"

// AnthropicProvider implements the Provider interface for Anthropic (Claude) Messages API.
type AnthropicProvider struct {
	client     anthropic.Client
	model      string
	apiKey     string
	httpClient *http.Client
}

// New creates a new Anthropic (Claude) provider.
func New(opts ...Option) *AnthropicProvider {
	p := &AnthropicProvider{
		model: DefaultModel,
	}
	for _, opt := range opts {
		opt(p)
	}

	clientOpts := []option.RequestOption{}
	if p.apiKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(p.apiKey))
	}
	if p.httpClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(p.httpClient))
	}

	p.client = anthropic.NewClient(clientOpts...)
	return p
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) Capabilities() providers.ProviderCapabilities {
	sessionUnsupported := "the Anthropic Messages wrapper does not implement a bidirectional session provider"
	return providers.ProviderCapabilities{
		Provider: p.Name(),
		Stateless: providers.StatelessCapabilities{
			Inference:       providers.Supported(),
			Streaming:       providers.Supported(),
			Tools:           providers.Supported(),
			ImageInput:      providers.Supported(),
			AudioInput:      providers.Unsupported("audio parts are converted to text data references, not native Anthropic audio input"),
			VideoInput:      providers.Unsupported("the Anthropic Messages wrapper does not map video input parts"),
			VideoOutput:     providers.Unsupported("the Anthropic Messages wrapper does not map video output responses"),
			Reasoning:       providers.Supported(),
			PromptCaching:   providers.Supported(),
			ProviderOptions: providers.Unsupported("raw provider Config is not applied by the Anthropic wrapper"),
		},
		Session: providers.SessionCapabilitiesPtr(providers.UnsupportedSessionCapabilities(sessionUnsupported)),
	}
}

func (p *AnthropicProvider) Infer(ctx context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	system, msgParams, err := messagesToParams(req.Messages)
	if err != nil {
		return providers.InferenceResponse{}, err
	}
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model(model),
		Messages: msgParams,
		System:   system,
	}
	applyInferenceRequestOptions(&params, req)
	if len(req.Tools) > 0 {
		params.Tools = toolsToParams(req.Tools)
	}

	message, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return providers.InferenceResponse{}, err
	}

	msg := responseToMessage(*message)
	usage := message.Usage
	return providers.InferenceResponse{
		Message: msg,
		Usage: models.TokenUsage{
			PromptTokens:     int(usage.InputTokens),
			CompletionTokens: int(usage.OutputTokens),
			TotalTokens:      int(usage.InputTokens + usage.OutputTokens),
		},
	}, nil
}

func (p *AnthropicProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	system, msgParams, err := messagesToParams(req.Messages)
	if err != nil {
		return nil, err
	}
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model(model),
		Messages: msgParams,
		System:   system,
	}
	applyInferenceRequestOptions(&params, req)
	if len(req.Tools) > 0 {
		params.Tools = toolsToParams(req.Tools)
	}

	s := p.client.Messages.NewStreaming(ctx, params)
	ch := make(chan messages.StreamMessage, 64)
	go func() {
		streamAnthropicToGateway(s, ch)
	}()
	return ch, nil
}
