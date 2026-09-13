package agentruntime

import (
	"context"
	"encoding/json"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	providersessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const SessionReplayCompleteClassification = "replay_complete"

// Deprecated: planOpenAIRecordRuntime and planOpenAIReplayRuntime only map options into providersession.
func planOpenAIRecordRuntime(o SessionRunOptions, f sessionRuntimeFactory) (sessionRuntimePlan, error) {
	c, err := resolveOpenAIRealtimeSessionConfig(o)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	p, err := newProviderSessionService(f).PlanRecord(context.Background(), providersession.RecordRequest{Provider: sessionProviderOpenAI, Model: c.Model, APIKey: c.APIKey, BaseURL: c.BaseURL, ReasoningEffort: c.ReasoningEffort, Voice: o.Voice, Prompt: o.Prompt, RecordPath: o.RecordPath, WaitForClose: o.WaitForClose, AudioInputs: providerAudioInputs(o.AudioInputs), ToolDefinitions: o.ToolDefinitions, AudioInputAvailable: o.RTCDeviceBinding.inputSelected(), ClientOwnsAudioTurnBoundaries: o.ClientOwnsAudioTurnBoundaries, NoInputTranscription: o.NoInputTranscription, InputAudioTranscription: o.InputAudioTranscription, TurnDetection: o.TurnDetection, WebSocketDialer: o.WebSocketDialer, ObserveDialer: func(d transport.Dialer) transport.Dialer { return observeSessionWire(d, o) }})
	return adaptProviderSessionPlan(p), err
}

func planOpenAIReplayRuntime(o SessionRunOptions, f sessionRuntimeFactory) (sessionRuntimePlan, error) {
	p, err := newProviderSessionService(f).PlanReplay(context.Background(), providersession.ReplayRequest{Provider: sessionProviderOpenAI, ReplayPath: o.ReplayPath, ReplayTiming: o.ReplayTiming, Prompt: o.Prompt, PromptProvided: o.PromptProvided, Voice: o.Voice, WaitForClose: o.WaitForClose, AudioInputs: providerAudioInputs(o.AudioInputs), ClientOwnsAudioTurnBoundaries: o.ClientOwnsAudioTurnBoundaries, RoomReplay: o.roomReplay, ToolDefinitions: o.ToolDefinitions})
	return adaptProviderSessionPlan(p), err
}
func buildOpenAIRealtimeSessionInferencer(c config.OpenAIConfig, v string, d transport.Dialer) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(c, v, d, models.InputAudioTranscriptionConfig{})
}
func buildOpenAIRealtimeSessionInferencerWithTools(c config.OpenAIConfig, v string, d transport.Dialer, t []messages.ToolDefinition) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(c, v, d, t, models.InputAudioTranscriptionConfig{})
}
func buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(c config.OpenAIConfig, v string, d transport.Dialer, t models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(c, v, d, nil, t)
}
func buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(c config.OpenAIConfig, v string, d transport.Dialer, t []messages.ToolDefinition, tr models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIProviderSession(c, v, d, t, tr, false)
}
func buildOpenAIRealtimeSessionInferencerWithScheduledAudioAndInputAudioTranscription(c config.OpenAIConfig, v string, d transport.Dialer, t []messages.ToolDefinition, tr models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIProviderSession(c, v, d, t, tr, true)
}
func buildOpenAIProviderSession(c config.OpenAIConfig, v string, d transport.Dialer, t []messages.ToolDefinition, tr models.InputAudioTranscriptionConfig, scheduled bool) (messages.SessionInferencer, error) {
	return newProviderSessionService(sessionRuntimeFactory{}).BuildOpenAI(context.Background(), providersession.BuildRequest{Provider: sessionProviderOpenAI, Model: c.Model, APIKey: c.APIKey, BaseURL: c.BaseURL, ReasoningEffort: c.ReasoningEffort, Voice: v, Dialer: d, ToolDefinitions: t, InputAudioTranscription: tr, ClientOwnsAudioTurnBoundaries: scheduled})
}
func newProviderSessionService(f sessionRuntimeFactory) providersession.Service {
	d := providersession.Dependencies{}
	if f.newDefaultLiveDialer != nil {
		d.NewDefaultDialer = func(string) transport.Dialer { return f.newDefaultLiveDialer() }
	}
	if f.newRecordingDialer != nil {
		d.NewRecordingDialer = func(i transport.Dialer, p, m string) providersession.RecordingDialer {
			return f.newRecordingDialer(i, p, m)
		}
	}
	if f.newReplayDialer != nil || f.newRecordedTimingReplayDialer != nil {
		d.NewReplayDialer = func(p, t string) (providersession.ReplayDialer, error) { r, e := f.replayDialer(p, t); return r, e }
	}
	d.NewInferencer = f.newProviderSessionInferencer
	d.WrapReplayInferencer = newWebSocketReplaySessionInferencer
	return providersessionwire.NewService(d)
}
func (f sessionRuntimeFactory) newProviderSessionInferencer(b providersession.BuildRequest) (messages.SessionInferencer, error) {
	d := b.Dialer
	if len(b.InitialSessionUpdate) > 0 {
		d = newReplayInitialSessionUpdateDialer(d, replaySessionConfiguration{payload: b.InitialSessionUpdate})
	}
	if b.Provider == sessionProviderOpenAI {
		return f.newOpenAISessionInferencerForTools(config.OpenAIConfig{Model: b.Model, APIKey: b.APIKey, BaseURL: b.BaseURL, ReasoningEffort: b.ReasoningEffort}, b.Voice, d, b.ToolDefinitions, b.ClientOwnsAudioTurnBoundaries, b.InputAudioTranscription)
	}
	return f.newGrokSessionInferencerForTools(config.GrokConfig{Model: b.Model, APIKey: b.APIKey, BaseURL: b.BaseURL}, d, b.ToolDefinitions)
}
func adaptProviderSessionPlan(p providersession.Plan) sessionRuntimePlan {
	r := sessionRuntimePlan{mode: sessionRuntimeMode(p.Mode), provider: p.Provider, model: p.Model, capturePath: p.CapturePath, announce: p.Announcement, inferencer: p.Inferencer, inputAudioSampleRate: p.InputAudioSampleRate, outputAudioSampleRate: p.OutputAudioSampleRate, announceTools: p.AnnounceTools, flushCapture: p.FlushCapture, flushCaptureTo: p.FlushCaptureTo, finalize: p.Finalize, audioInputs: sessionAudioInputs(p.AudioInputs), loop: sessionLoopOptions{Prompt: p.Prompt, PromptProvided: p.PromptProvided, CloseAfterOpen: p.CloseAfterOpen, WaitForClose: p.WaitForClose, CloseAfterScheduledAudio: p.CloseAfterScheduledAudio, RequireSessionUpdated: p.RequireSessionUpdated, MaxDuration: p.MaxDuration, Done: p.Done, DoneErr: p.DoneErr}}
	r.replayCompletion = map[bool]func(*sessionTerminalReporter){true: func(t *sessionTerminalReporter) { t.markReplayComplete() }}[p.ReplayComplete]
	return r
}
func providerAudioInputs(in []ScheduledAudioInput) []providersession.AudioInput {
	var out []providersession.AudioInput
	for _, v := range in {
		out = append(out, providersession.AudioInput{AfterCompletedTurns: v.AfterCompletedTurns, PCM: append([]byte(nil), v.PCM...), SourceSampleRate: v.SourceSampleRate, EndOfTurn: v.EndOfTurn})
	}
	return out
}
func sessionAudioInputs(in []providersession.AudioInput) []ScheduledAudioInput {
	var out []ScheduledAudioInput
	for _, v := range in {
		out = append(out, ScheduledAudioInput{AfterCompletedTurns: v.AfterCompletedTurns, PCM: append([]byte(nil), v.PCM...), SourceSampleRate: v.SourceSampleRate, EndOfTurn: v.EndOfTurn})
	}
	return out
}
func (p sessionRuntimePlan) toolDefinitionsForAnnouncement() []messages.ToolDefinition {
	return map[bool][]messages.ToolDefinition{true: p.announceTools, false: p.loop.ToolDefinitions}[p.announceTools != nil]
}

func replaySessionToolNames(path string, sequence int, session map[string]json.RawMessage) ([]string, bool, error) {
	return (providersession.ReplayTools{}).Names(path, sequence, session)
}
