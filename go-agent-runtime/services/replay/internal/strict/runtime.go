package strict

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const replayBufferCapacity = 128

// NewOpenAIRuntimeFactory returns the production headless core runtime
// factory. It composes the same AgentLoop and OpenAI realtime adapter used by
// live sessions, but receives only Prepared's strict in-memory dialer and
// recorded tool executor.
func NewOpenAIRuntimeFactory() publicreplay.RuntimeFactory { return openAIRuntimeFactory{} }

type openAIRuntimeFactory struct{}

func (openAIRuntimeFactory) New(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
	if prepared == nil {
		return nil, publicreplay.ErrBundleIncomplete
	}
	if prepared.Clock() == nil {
		return nil, publicreplay.ErrDeterministicClockRequired
	}
	capture := prepared.Capture()
	providerName := strings.TrimSpace(capture.Provider.Name)
	if providerName != "" && !strings.EqualFold(providerName, "openai") {
		return nil, fmt.Errorf("%w: production offline replay factory supports provider %q, got %q", publicreplay.ErrBundleMismatch, "openai", providerName)
	}
	model := strings.TrimSpace(capture.Provider.Model)
	if model == "" {
		return nil, fmt.Errorf("%w: replay handshake has no model", publicreplay.ErrBundleMismatch)
	}
	initialUpdate, err := initialSessionUpdate(capture)
	if err != nil {
		return nil, err
	}
	dialer := &initialUpdateDialer{inner: prepared.Dialer(), payload: initialUpdate}
	provider := oaiprovider.New(
		oaiprovider.WithAPIKey("offline-replay"),
		oaiprovider.WithModel(model),
		oaiprovider.WithRealtimeBaseURL("ws://offline.invalid/realtime"),
		oaiprovider.WithWebSocketDialer(dialer),
	)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		return nil, fmt.Errorf("construct offline OpenAI gateway: %w", err)
	}
	inferencer := replayMediaInferencer{inner: inference.NewSessionGatewayInferencer(sessionGateway, inference.WithSessionModel(model))}
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
		agentloop.WithToolExecutor(prepared.ToolExecutor()),
		agentloop.WithTools(replayToolDefinitions(capture)),
		agentloop.WithClock(prepared.Clock()),
		agentloop.WithBufferCapacity(replayBufferCapacity),
	)
	if err != nil {
		return nil, fmt.Errorf("construct offline agent loop: %w", err)
	}
	actions, err := deriveInputActions(capture)
	if err != nil {
		return nil, err
	}
	return &coreRuntime{loop: loop, actions: actions}, nil
}

func replayToolDefinitions(capture gwtesting.SessionCapture) []messages.ToolDefinition {
	seen := make(map[string]struct{})
	var definitions []messages.ToolDefinition
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient || record.Type != "response.function_call_arguments.done" {
			continue
		}
		var event struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(record.Payload, &event) != nil || strings.TrimSpace(event.Name) == "" {
			continue
		}
		if _, ok := seen[event.Name]; ok {
			continue
		}
		seen[event.Name] = struct{}{}
		definitions = append(definitions, messages.ToolDefinition{Name: event.Name, ParametersClosed: true})
	}
	return definitions
}
