package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestWireServiceBuildsIndependentProviderRequests(t *testing.T) {
	first := NewService(providersession.Dependencies{})
	second := NewService(providersession.Dependencies{})
	dialer := noopDialer{}

	openAI, err := first.BuildOpenAI(context.Background(), providersession.BuildRequest{
		Provider: providersession.ProviderOpenAI, Model: "gpt-realtime-1", APIKey: "openai-test-key",
		BaseURL: "ws://openai.test/realtime", ReasoningEffort: "high", Voice: "marin", Dialer: dialer,
		ToolDefinitions:         []messages.ToolDefinition{{Name: "lookup"}},
		InputAudioTranscription: models.InputAudioTranscriptionConfig{Enabled: true, Model: "transcriber-test"},
	})
	if err != nil {
		t.Fatalf("BuildOpenAI: %v", err)
	}
	openAIRequest, ok := openAI.(*inference.SessionGatewayInferencer)
	if !ok {
		t.Fatalf("OpenAI inferencer type = %T", openAI)
	}
	openAIConfig := openAIRequest.Request().Config
	if openAIConfig.Model != "gpt-realtime-1" || openAIConfig.ReasoningEffort != "high" || openAIConfig.Voice != "marin" {
		t.Fatalf("OpenAI request identity = %#v", openAIConfig)
	}
	if len(openAIConfig.Tools) != 1 || openAIConfig.Tools[0].Name != "lookup" || openAIConfig.InputAudioTranscription == nil || openAIConfig.InputAudioTranscription.Model != "transcriber-test" {
		t.Fatalf("OpenAI request options = %#v", openAIConfig)
	}

	grok, err := second.BuildGrok(context.Background(), providersession.BuildRequest{
		Provider: providersession.ProviderGrok, Model: "grok-test-model", APIKey: "grok-test-key",
		BaseURL: "ws://grok.test/realtime", Dialer: dialer,
		ToolDefinitions: []messages.ToolDefinition{{Name: "weather"}},
	})
	if err != nil {
		t.Fatalf("BuildGrok: %v", err)
	}
	grokRequest, ok := grok.(*inference.SessionGatewayInferencer)
	if !ok {
		t.Fatalf("Grok inferencer type = %T", grok)
	}
	grokConfig := grokRequest.Request().Config
	if grokConfig.Model != "grok-test-model" || len(grokConfig.Tools) != 1 || grokConfig.Tools[0].Name != "weather" {
		t.Fatalf("Grok request options = %#v", grokConfig)
	}

	openAIConfig.Model = "mutated-copy"
	if got := openAIRequest.Request().Config.Model; got != "gpt-realtime-1" {
		t.Fatalf("OpenAI service state changed through request copy: %q", got)
	}
}

func TestWireServiceBuildRejectsMissingDialerWithTypedIdentity(t *testing.T) {
	service := NewService(providersession.Dependencies{})
	_, err := service.BuildOpenAI(context.Background(), providersession.BuildRequest{Provider: providersession.ProviderOpenAI})
	if !errors.Is(err, providersession.ErrMissingDialer) {
		t.Fatalf("BuildOpenAI error = %v, want ErrMissingDialer", err)
	}
	_, err = service.BuildGrok(context.Background(), providersession.BuildRequest{Provider: providersession.ProviderGrok})
	if !errors.Is(err, providersession.ErrMissingDialer) {
		t.Fatalf("BuildGrok error = %v, want ErrMissingDialer", err)
	}
}

type noopDialer struct{}

func (noopDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("consumer dialer is not opened during construction")
}

var _ transport.Dialer = noopDialer{}
