package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const (
	defaultBaseURL         = "https://api.openai.com/v1"
	defaultRealtimeBaseURL = "wss://api.openai.com/v1/realtime"
)

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs
// using direct HTTP calls. By avoiding an SDK, the provider has full control
// over the request/response JSON, enabling compatibility with third-party APIs
// such as OpenRouter that extend the OpenAI schema (e.g. images in tool messages,
// video content parts, etc.).
type OpenAIProvider struct {
	httpClient                  *http.Client
	model                       string
	baseURL                     string
	realtimeBaseURL             string
	logger                      logging.Logger
	apiKey                      string
	realtimeDialer              WebSocketDialer
	realtimeLegacySessionUpdate bool
}

var _ providers.Provider = (*OpenAIProvider)(nil)
var _ providers.SessionProvider = (*OpenAIProvider)(nil)

// New creates a new OpenAI-compatible provider.
func New(opts ...Option) *OpenAIProvider {
	p := &OpenAIProvider{
		model:   "gpt-4o",
		baseURL: defaultBaseURL,
		logger:  logging.DummyLogger(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) httpClientOrDefault() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return http.DefaultClient
}

func (p *OpenAIProvider) completionsURL() string {
	return strings.TrimRight(p.baseURL, "/") + "/chat/completions"
}

func (p *OpenAIProvider) Infer(ctx context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	chatReq := chatRequest{
		Model:    model,
		Messages: messagesToParams(req.Messages, p.logger),
	}
	applyInferenceRequestOptions(&chatReq, req)
	if len(req.Tools) > 0 {
		chatReq.Tools = toolsToParams(req.Tools)
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return providers.InferenceResponse{}, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.completionsURL(), bytes.NewReader(body))
	if err != nil {
		return providers.InferenceResponse{}, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClientOrDefault().Do(httpReq)
	if err != nil {
		if cancellationErr := gateway.CancellationErrorOrNil("openai: chat completions cancelled", err); cancellationErr != nil {
			return providers.InferenceResponse{}, cancellationErr
		}
		return providers.InferenceResponse{}, gateway.NewTransportError(p.Name(), "chat completions", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return providers.InferenceResponse{}, gateway.NewProviderHTTPStatusError(p.Name(), resp.StatusCode, string(errBody), nil)
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return providers.InferenceResponse{}, fmt.Errorf("openai: decode response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return providers.InferenceResponse{}, nil
	}

	msg := responseToMessage(chatResp.Choices[0].Message)
	return providers.InferenceResponse{
		Message: msg,
		Usage: models.TokenUsage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		},
	}, nil
}

func (p *OpenAIProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	chatReq := chatRequest{
		Model:         model,
		Messages:      messagesToParams(req.Messages, p.logger),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	applyInferenceRequestOptions(&chatReq, req)
	if len(req.Tools) > 0 {
		chatReq.Tools = toolsToParams(req.Tools)
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.completionsURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClientOrDefault().Do(httpReq)
	if err != nil {
		p.logger.Error("openai: failed to open request", logging.Field{Key: "error", Value: err})
		if cancellationErr := gateway.CancellationErrorOrNil("openai: chat completions stream cancelled", err); cancellationErr != nil {
			return nil, cancellationErr
		}
		return nil, gateway.NewTransportError(p.Name(), "chat completions stream", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		errBody, _ := io.ReadAll(resp.Body)
		p.logger.Error("openai: api error", logging.Field{Key: "status_code", Value: resp.StatusCode}, logging.Field{Key: "error_body", Value: string(errBody)})
		return nil, gateway.NewProviderHTTPStatusError(p.Name(), resp.StatusCode, string(errBody), nil)
	}

	ch := make(chan messages.StreamMessage, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		streamSSEToGateway(resp.Body, ch)
	}()
	return ch, nil
}
