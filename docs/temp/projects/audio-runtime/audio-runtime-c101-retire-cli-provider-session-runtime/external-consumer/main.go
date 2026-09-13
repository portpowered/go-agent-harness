package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	providersessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func run() error {
	first := providersessionwire.NewService(providersession.Dependencies{})
	second := providersessionwire.NewService(providersession.Dependencies{})
	dialer := consumerDialer{}

	openAI, err := first.BuildOpenAI(context.Background(), providersession.BuildRequest{
		Provider: providersession.ProviderOpenAI, Model: "gpt-realtime-external",
		APIKey: "external-consumer-test-key", BaseURL: "ws://openai.external/realtime",
		ReasoningEffort: "high", Dialer: dialer,
		ToolDefinitions: []messages.ToolDefinition{{Name: "lookup"}},
	})
	if err != nil {
		return fmt.Errorf("build OpenAI session: %w", err)
	}
	grok, err := second.BuildGrok(context.Background(), providersession.BuildRequest{
		Provider: providersession.ProviderGrok, Model: "grok-realtime-external",
		APIKey: "external-consumer-test-key", BaseURL: "ws://grok.external/realtime",
		Dialer: dialer, ToolDefinitions: []messages.ToolDefinition{{Name: "weather"}},
	})
	if err != nil {
		return fmt.Errorf("build Grok session: %w", err)
	}

	openAIInferencer, ok := openAI.(*inference.SessionGatewayInferencer)
	if !ok {
		return fmt.Errorf("OpenAI inferencer type = %T", openAI)
	}
	grokInferencer, ok := grok.(*inference.SessionGatewayInferencer)
	if !ok {
		return fmt.Errorf("Grok inferencer type = %T", grok)
	}
	openAIConfig := openAIInferencer.Request().Config
	grokConfig := grokInferencer.Request().Config
	if openAIConfig.Model != "gpt-realtime-external" || openAIConfig.ReasoningEffort != "high" || len(openAIConfig.Tools) != 1 || openAIConfig.Tools[0].Name != "lookup" {
		return fmt.Errorf("unexpected OpenAI config: %#v", openAIConfig)
	}
	if grokConfig.Model != "grok-realtime-external" || len(grokConfig.Tools) != 1 || grokConfig.Tools[0].Name != "weather" {
		return fmt.Errorf("unexpected Grok config: %#v", grokConfig)
	}
	if openAIConfig.Model == grokConfig.Model {
		return errors.New("provider service instances share model state")
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

type consumerDialer struct{}

func (consumerDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("external consumer dialer is construction-only")
}

var _ transport.Dialer = consumerDialer{}
