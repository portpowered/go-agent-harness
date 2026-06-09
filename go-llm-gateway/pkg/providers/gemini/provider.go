package gemini

import (
	"context"
	"net/http"

	"google.golang.org/genai"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// DefaultModel is the default Gemini model when none is specified.
const DefaultModel = "gemini-2.5-flash"

// GeminiProvider implements the Provider interface for Google Gemini using the GenAI SDK.
type GeminiProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// New creates a new Gemini provider.
func New(opts ...Option) *GeminiProvider {
	p := &GeminiProvider{
		model: DefaultModel,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) Capabilities() providers.ProviderCapabilities {
	sessionUnsupported := "the Gemini wrapper does not implement a bidirectional session provider"
	sessionCap := capabilities.Unsupported(sessionUnsupported)
	return capabilities.ProviderCapabilities{
		Provider: p.Name(),
		Stateless: capabilities.StatelessCapabilities{
			Tools:                  capabilities.Supported("Gemini requests serialize function declarations"),
			Streaming:              capabilities.Supported("Gemini streaming is implemented"),
			ImageInput:             capabilities.Supported("image parts are mapped to Gemini content parts"),
			AudioInput:             capabilities.Supported("audio parts are mapped to Gemini content parts"),
			AudioOutput:            capabilities.Unsupported("the Gemini wrapper does not normalize audio output"),
			VideoOutput:            capabilities.Unsupported("the Gemini wrapper does not normalize video output"),
			Reasoning:              capabilities.Unsupported("the Gemini wrapper does not expose a reasoning request option"),
			PromptCaching:          capabilities.Unsupported("the Gemini wrapper does not map cache-control options"),
			ProviderSpecificConfig: capabilities.Unsupported("InferenceRequest Config is not merged by the Gemini wrapper"),
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

// newClient creates a GenAI client configured with the provider's settings.
func (p *GeminiProvider) newClient(ctx context.Context) (*genai.Client, error) {
	cc := &genai.ClientConfig{
		APIKey:  p.apiKey,
		Backend: genai.BackendGeminiAPI,
	}
	if p.httpClient != nil {
		cc.HTTPClient = p.httpClient
	}
	return genai.NewClient(ctx, cc)
}

// Infer sends messages to Gemini and returns a complete response.
func (p *GeminiProvider) Infer(ctx context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	client, err := p.newClient(ctx)
	if err != nil {
		return providers.InferenceResponse{}, err
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	system, contents := messagesToContents(req.Messages)
	config := buildConfig(req, system)

	resp, err := client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		return providers.InferenceResponse{}, err
	}

	msg := responseToMessage(resp)
	usage := responseUsage(resp)

	return providers.InferenceResponse{
		Message: msg,
		Usage:   usage,
	}, nil
}

// InferStream sends messages to Gemini and returns a channel of streaming delta events.
func (p *GeminiProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	client, err := p.newClient(ctx)
	if err != nil {
		return nil, err
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	system, contents := messagesToContents(req.Messages)
	config := buildConfig(req, system)

	iter := client.Models.GenerateContentStream(ctx, model, contents, config)

	ch := make(chan messages.StreamMessage, 64)
	go streamGeminiToGateway(iter, ch)
	return ch, nil
}
