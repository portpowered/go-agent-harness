package anthropic

import (
	"context"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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
	sessionCap := capabilities.Unsupported(sessionUnsupported)
	return capabilities.ProviderCapabilities{
		Provider: p.Name(),
		Stateless: capabilities.StatelessCapabilities{
			Tools:                  capabilities.Supported("Anthropic Messages requests serialize tool definitions"),
			Streaming:              capabilities.Supported("Anthropic Messages streaming is implemented"),
			ImageInput:             capabilities.Supported("image parts are serialized as image content blocks"),
			AudioInput:             capabilities.Unsupported("audio parts are converted to text data references, not native Anthropic audio input"),
			AudioOutput:            capabilities.Unsupported("the Anthropic Messages wrapper does not normalize audio output"),
			VideoOutput:            capabilities.Unsupported("the Anthropic Messages wrapper does not normalize video output"),
			Reasoning:              capabilities.Supported("Anthropic thinking options are mapped to the Messages API"),
			PromptCaching:          capabilities.Supported("Anthropic cache-control blocks are mapped by the wrapper"),
			ProviderSpecificConfig: capabilities.Unsupported("InferenceRequest Config is not merged by the Anthropic wrapper"),
		},
		Session: capabilities.SessionCapabilities{
			Sessions:               sessionCap,
			Tools:                  sessionCap,
			AudioInput:             sessionCap,
			AudioOutput:            sessionCap,
			ProviderSpecificConfig: sessionCap,
		},
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
