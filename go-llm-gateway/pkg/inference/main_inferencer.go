package inference

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
)

// GatewayInferencer is the public stateless bridge from a
// go-llm-gateway gateway.Gateway into the loop-owned messages.Inferencer
// contract.
type GatewayInferencer struct {
	gw          gateway.Gateway
	model       string          // model ID forwarded to every request
	modelConfig json.RawMessage // model-specific config forwarded to every request
}

// Option configures the GatewayInferencer.
type Option func(*GatewayInferencer)

// WithModel sets the model ID forwarded to every inference request.
func WithModel(model string) Option {
	return func(gi *GatewayInferencer) {
		gi.model = model
	}
}

// WithModelConfig sets model-specific config JSON forwarded to every inference request.
// The raw JSON string is parsed once; if invalid JSON it is silently ignored.
func WithModelConfig(raw string) Option {
	return func(gi *GatewayInferencer) {
		if raw == "" {
			return
		}
		gi.modelConfig = json.RawMessage(raw)
	}
}

// Compile-time check that GatewayInferencer satisfies messages.Inferencer.
var _ messages.Inferencer = (*GatewayInferencer)(nil)

func NewGatewayInferencer(gw gateway.Gateway, opts ...Option) *GatewayInferencer {
	gi := &GatewayInferencer{gw: gw}
	for _, opt := range opts {
		opt(gi)
	}
	return gi
}

// Infer forwards a loop-owned inference request into the gateway seam and
// translates the normalized gateway response back into messages-owned result
// shapes.
func (gi *GatewayInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	resp, err := gi.gw.Infer(ctx, gateway.InferenceRequest{
		Messages:         req.Messages,
		Tools:            req.Tools,
		Model:            gi.model,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		StopSequences:    req.StopSequences,
		FrequencyPenalty: req.FrequencyPenalty,
		Config:           gi.modelConfig,
	})
	if err != nil {
		return messages.InferenceResult{}, err
	}

	var toolCalls []messages.ToolCall
	for _, tc := range resp.Message.ToolCalls {
		toolCalls = append(toolCalls, messages.ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}

	return messages.InferenceResult{
		Message:   resp.Message,
		ToolCalls: toolCalls,
		TokenUsage: messages.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			ReasoningTokens:  resp.Usage.ReasoningTokens,
		},
	}, nil
}

// InferStream forwards a loop-owned inference request into the gateway seam
// and returns the loop-owned streaming contract without introducing a second
// shared message surface.
func (gi *GatewayInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return gi.gw.InferStream(ctx, gateway.InferenceRequest{
		Messages:         req.Messages,
		Tools:            req.Tools,
		Model:            gi.model,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		StopSequences:    req.StopSequences,
		FrequencyPenalty: req.FrequencyPenalty,
		Config:           gi.modelConfig,
	})
}
