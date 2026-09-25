package servicetest

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

const openAIRealtimeBaseURL = "wss://api.openai.com/v1/realtime"

// NewOpenAIRealtimeSessionInferencerWithOptions builds a gateway-backed OpenAI
// realtime inferencer for hermetic acceptance tests. Callers inject the
// provider transport through options and pass the result into the composed
// CLI's session-inferencer port.
func NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg config.OpenAIConfig, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	return NewOpenAIRealtimeSessionInferencerWithToolsAndOptions(sessionCfg, nil, opts...)
}

// NewOpenAIRealtimeSessionInferencerWithToolsAndOptions additionally carries
// the selected tool definitions in the initial session configuration.
func NewOpenAIRealtimeSessionInferencerWithToolsAndOptions(sessionCfg config.OpenAIConfig, toolDefinitions []messages.ToolDefinition, opts ...oaiprovider.Option) (messages.SessionInferencer, error) {
	providerOpts := []oaiprovider.Option{
		oaiprovider.WithAPIKey(sessionCfg.APIKey),
		oaiprovider.WithModel(sessionCfg.Model),
		oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
	}
	providerOpts = append(providerOpts, opts...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInputAudioTranscription(models.InputAudioTranscriptionConfig{Enabled: true, Model: models.DefaultInputAudioTranscriptionModel}),
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

func openAIRealtimeURL(sessionCfg config.OpenAIConfig) string {
	base := strings.TrimSpace(sessionCfg.BaseURL)
	if base == "" {
		base = openAIRealtimeBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", sessionCfg.Model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
