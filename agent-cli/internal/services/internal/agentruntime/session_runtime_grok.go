package agentruntime

import (
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Deprecated: the CLI adapter only maps options into providersession.
func planGrokRecordRuntime(o SessionRunOptions, f sessionRuntimeFactory) (sessionRuntimePlan, error) {
	c, err := resolveGrokSessionConfig(o)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	p, err := newProviderSessionService(f).PlanRecord(context.Background(), providersession.RecordRequest{Provider: sessionProviderGrok, Model: c.Model, APIKey: c.APIKey, BaseURL: c.BaseURL, Prompt: o.Prompt, RecordPath: o.RecordPath, WaitForClose: o.WaitForClose, AudioInputs: providerAudioInputs(o.AudioInputs), ToolDefinitions: o.ToolDefinitions, AudioInputAvailable: o.RTCDeviceBinding.inputSelected(), ClientOwnsAudioTurnBoundaries: o.ClientOwnsAudioTurnBoundaries, WebSocketDialer: o.WebSocketDialer, ObserveDialer: func(d transport.Dialer) transport.Dialer { return observeSessionWire(d, o) }})
	return adaptProviderSessionPlan(p), err
}

func planGrokReplayRuntime(o SessionRunOptions, f sessionRuntimeFactory) (sessionRuntimePlan, error) {
	p, err := newProviderSessionService(f).PlanReplay(context.Background(), providersession.ReplayRequest{Provider: sessionProviderGrok, ReplayPath: o.ReplayPath, ReplayTiming: o.ReplayTiming, Prompt: o.Prompt, PromptProvided: o.PromptProvided, WaitForClose: o.WaitForClose, AudioInputs: providerAudioInputs(o.AudioInputs), ClientOwnsAudioTurnBoundaries: o.ClientOwnsAudioTurnBoundaries, RoomReplay: o.roomReplay, ToolDefinitions: o.ToolDefinitions})
	return adaptProviderSessionPlan(p), err
}
func buildGrokSessionInferencer(c config.GrokConfig, d transport.Dialer) (messages.SessionInferencer, error) {
	return buildGrokSessionInferencerWithTools(c, d, nil)
}
func buildGrokSessionInferencerWithTools(c config.GrokConfig, d transport.Dialer, t []messages.ToolDefinition) (messages.SessionInferencer, error) {
	return newProviderSessionService(sessionRuntimeFactory{}).BuildGrok(context.Background(), providersession.BuildRequest{Provider: sessionProviderGrok, Model: c.Model, APIKey: c.APIKey, BaseURL: c.BaseURL, Dialer: d, ToolDefinitions: t})
}
