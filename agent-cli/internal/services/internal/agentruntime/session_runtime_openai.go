// This file owns OpenAI-specific session-runtime recording, websocket replay planning, and realtime session inferencer construction.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// SessionReplayCompleteClassification is the terminal classification emitted
// after a capture-derived replay consumes the complete ordered event stream.
const SessionReplayCompleteClassification = "replay_complete"

func planOpenAIRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
	inputAudioTranscription, err := resolveSessionTranscription(opts, sessionProviderOpenAI, clientOwnedAudio || opts.RTCBinding.HasInput())
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	build := openAIRecordingSessionBuilder(opts, factory, sessionCfg, clientOwnedAudio, inputAudioTranscription)
	dialer := func() transport.Dialer {
		if opts.WebSocketDialer != nil {
			return opts.WebSocketDialer
		}
		return oaiprovider.NewDefaultWebSocketDialer()
	}
	loop := sessionLoopOptions{
		Prompt:                   opts.Prompt,
		CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
		WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
		CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
		RequireSessionUpdated:    len(opts.AudioInputs) > 0,
	}
	return providerRecordingPlan(opts, sessionProviderOpenAI, sessionCfg.Model,
		fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		sessionRuntimeModeRecordOpenAI, loop, dialer, build)
}

func planOpenAIRTCRecording(opts SessionRunOptions, factory sessionRuntimeFactory, runtime SessionRTCRuntime) (sessionRTCProviderRecording, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return sessionRTCProviderRecording{}, err
	}
	transcription, err := resolveSessionTranscription(opts, sessionProviderOpenAI, opts.RTCBinding.HasInput())
	if err != nil {
		return sessionRTCProviderRecording{}, err
	}
	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		return factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, dialer, opts.ToolDefinitions, false, transcription)
	}
	capture, err := opts.RecordingService.RecordProviderSession(opts.ProviderCaptureService, recording.ProviderSessionOptions{
		Destination: opts.RecordPath, Provider: sessionProviderOpenAI, Model: sessionCfg.Model,
		Dialer: &sessionRTCLazyDialer{runtime: runtime}, Clock: opts.Clock, Build: build,
	})
	if err != nil {
		return sessionRTCProviderRecording{}, err
	}
	return sessionRTCProviderRecording{
		provider: sessionProviderOpenAI, model: sessionCfg.Model, mode: sessionRuntimeModeRecordOpenAI,
		inferencer: capture, flush: capture.FlushCapture, flushTo: capture.FlushToFile,
		announce: fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		finalize: sessionCaptureFinalizer(opts.RecordPath),
	}, nil
}

func openAIRecordingSessionBuilder(opts SessionRunOptions, factory sessionRuntimeFactory, sessionCfg config.OpenAIConfig, clientOwnedAudio bool, inputAudioTranscription models.InputAudioTranscriptionConfig) func(transport.Dialer) (messages.SessionInferencer, error) {
	return func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		inferencer, err := factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, dialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
		if err != nil {
			return nil, err
		}
		if configurer, ok := inferencer.(interface {
			SetSessionTurnDetection(*models.TurnDetectionConfig)
		}); ok {
			turnDetection := cloneSessionTurnDetection(opts.TurnDetection)
			if turnDetection == nil && opts.RTCBinding.HasInput() && !clientOwnedAudio {
				turnDetection = &models.TurnDetectionConfig{Type: "semantic_vad"}
			}
			configurer.SetSessionTurnDetection(turnDetection)
		}
		return inferencer, nil
	}
}

func replayAnnouncementToolDefinitions(definitions []messages.ToolDefinition, names []string, known bool) []messages.ToolDefinition {
	if !known {
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	selected := make([]messages.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if _, ok := allowed[strings.TrimSpace(definition.Name)]; ok {
			selected = append(selected, definition)
		}
	}
	return selected
}

func (p sessionRuntimePlan) toolDefinitionsForAnnouncement() []messages.ToolDefinition {
	if p.announceTools != nil {
		return p.announceTools
	}
	return p.loop.ToolDefinitions
}

func replaySessionToolNames(path string, sequence int, session map[string]json.RawMessage) ([]string, bool, error) {
	raw, ok := session["tools"]
	if !ok {
		return []string{}, true, nil
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, true, fmt.Errorf("replay session capture %s: session.tools at sequence %d is invalid: %w", path, sequence, err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, true, nil
}

//lint:ignore U1000 package tests exercise the OpenAI factory seam.
func buildOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(sessionCfg, voice, dialer, models.InputAudioTranscriptionConfig{})
}

//lint:ignore U1000 package tests exercise the OpenAI factory seam.
func buildOpenAIRealtimeSessionInferencerWithTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(sessionCfg, voice, dialer, toolDefinitions, models.InputAudioTranscriptionConfig{})
}

func buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(sessionCfg, voice, dialer, nil, inputAudioTranscription)
}

func buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	opts := make([]oaiprovider.Option, 0, 1)
	opts = append(opts, oaiprovider.WithWebSocketDialer(dialer))
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, toolDefinitions, inputAudioTranscription, opts...)
}

func buildOpenAIRealtimeSessionInferencerWithScheduledAudioAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	opts := []oaiprovider.Option{
		oaiprovider.WithWebSocketDialer(dialer),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	}
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, toolDefinitions, inputAudioTranscription, opts...)
}

func newOpenAILiveSessionInferencer(opts SessionRunOptions, instructions string) (messages.SessionInferencer, string, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return nil, "", err
	}
	model := sessionCfg.Model
	request := deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
	transcription, err := resolveSessionTranscription(opts, sessionProviderOpenAI, true)
	if err != nil {
		return nil, "", err
	}
	request.InputAudioTranscription = &transcription
	request.TurnDetection = cloneSessionTurnDetection(opts.TurnDetection)
	request.Voice = opts.Voice
	request.ReasoningEffort = sessionCfg.ReasoningEffort
	request.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(
			oaiprovider.WithAPIKey(sessionCfg.APIKey),
			oaiprovider.WithModel(sessionCfg.Model),
			oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
			oaiprovider.WithWebSocketDialer(dialer),
		)))
		if err != nil {
			return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
		}
		return inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: request})), nil
	}
	return buildLiveProbeSession(opts, sessionProviderOpenAI, model, defaultDeviceProbeDialer(opts, sessionProviderOpenAI), build)
}

func planReplaySessionRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.SessionInferencer != nil {
		return sessionRuntimePlan{
			mode: sessionRuntimeModeReplayGeneric, capturePath: opts.ReplayPath,
			provider: strings.ToLower(strings.TrimSpace(opts.Provider)), model: opts.Model, inferencer: opts.SessionInferencer,
			announceTools: []messages.ToolDefinition{},
			loop:          sessionLoopOptions{Prompt: opts.Prompt, WaitForClose: opts.WaitForClose, MaxDuration: injectedSessionDefaultMaxDuration},
		}, nil
	}
	if opts.ReplayService == nil {
		return sessionRuntimePlan{}, errors.New("replay service is not configured")
	}
	inspection, err := opts.ReplayService.InspectCapture(ctx, opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inspection.IsRealtime() {
		if strings.EqualFold(inspection.Provider, sessionProviderOpenAI) {
			return planRealtimeReplayForProvider(ctx, opts, factory, inspection, sessionProviderOpenAI)
		}
		return planRealtimeReplayForProvider(ctx, opts, factory, inspection, sessionProviderGrok)
	}
	return sessionRuntimePlan{
		mode: sessionRuntimeModeReplayGeneric, capturePath: opts.ReplayPath,
		replayIntegrityWarning: inspection.IntegrityWarning, loopOut: io.Discard,
		loop: sessionLoopOptions{Prompt: opts.Prompt},
		finalize: func(runCtx context.Context, out io.Writer) error {
			return runReplayCapture(runCtx, out, opts.ReplayService, opts.ReplayPath)
		},
	}, nil
}

func planRealtimeReplayForProvider(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory, inspection replay.CaptureInspection, provider string) (sessionRuntimePlan, error) {
	var plan sessionRuntimePlan
	var err error
	if provider == sessionProviderOpenAI {
		plan, err = planOpenAIReplayRuntimeContext(ctx, opts, factory)
	} else {
		plan, err = planGrokReplayRuntimeContext(ctx, opts, factory)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.replayIntegrityWarning = inspection.IntegrityWarning
	return plan, nil
}

func planOpenAIReplayRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	prepared, dialer, inspection, err := prepareReplayLiveContext(ctx, opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	model := strings.TrimSpace(inspection.Model)
	if model == "" {
		model = openAIRealtimeModel
	}
	prompt, promptProvided := replayOpeningPrompt(opts, inspection)
	bareAudioTurns := []ScheduledAudioInput(nil)
	if replayUsesCapturedAudioTurns(opts, inspection) {
		bareAudioTurns = replayScheduledAudioInputs(inspection.LivePlan.AudioTurns)
	}
	scheduledAudio := len(opts.AudioInputs) > 0 || len(bareAudioTurns) > 0
	inferencer, err := factory.newOpenAISessionInferencerForTools(config.OpenAIConfig{APIKey: "replay", Model: model}, opts.Voice, dialer, nil, scheduledAudio, models.InputAudioTranscriptionConfig{})
	if err != nil {
		return sessionRuntimePlan{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err))
	}
	inferencer = prepared.WrapInferencer(inferencer)
	planned := replayInspectionPlan(inspection)
	plan := sessionRuntimePlan{
		mode: sessionRuntimeModeReplayOpenAI, provider: sessionProviderOpenAI, model: model,
		inputAudioSampleRate: planned.inputRate, outputAudioSampleRate: planned.outputRate, inferencer: inferencer,
		announceTools: []messages.ToolDefinition{},
		loop:          sessionLoopOptions{Prompt: prompt, PromptProvided: promptProvided, WaitForClose: opts.WaitForClose || planned.providerClose, MaxDuration: planned.maxDuration, CloseAfterScheduledAudio: len(opts.AudioInputs) > 0, Done: prepared.Done(), DoneErr: prepared.Err},
		finalize:      func(_ context.Context, _ io.Writer) error { return errors.Join(prepared.Err(), prepared.Close()) },
	}
	if len(bareAudioTurns) > 0 {
		plan.audioInputs = bareAudioTurns
	}
	if replayCompletesAfterScheduledAudio(bareAudioTurns, opts, promptProvided) {
		plan.replayCompletion = func(reporter sessionterminal.Reporter) { reporter.MarkReplayComplete() }
	}
	return plan, nil
}

func replayOpeningPrompt(opts SessionRunOptions, inspection replay.CaptureInspection) (string, bool) {
	prompt := opts.Prompt
	promptProvided := opts.PromptProvided || prompt != ""
	if !promptProvided && inspection.LivePlan != nil && inspection.LivePlan.OpeningPromptPresent {
		return inspection.LivePlan.OpeningPrompt, true
	}
	return prompt, promptProvided
}

func replayUsesCapturedAudioTurns(opts SessionRunOptions, inspection replay.CaptureInspection) bool {
	return len(opts.AudioInputs) == 0 && !opts.ClientOwnsAudioTurnBoundaries && !opts.roomReplay && inspection.LivePlan != nil
}

func replayCompletesAfterScheduledAudio(turns []ScheduledAudioInput, opts SessionRunOptions, promptProvided bool) bool {
	return len(turns) > 0 || (promptProvided && !opts.PromptProvided && opts.Prompt == "")
}

type replayInspectionRuntimePlan struct {
	inputRate, outputRate int
	maxDuration           time.Duration
	providerClose         bool
}

func replayInspectionPlan(inspection replay.CaptureInspection) replayInspectionRuntimePlan {
	if inspection.LivePlan == nil {
		return replayInspectionRuntimePlan{}
	}
	return replayInspectionRuntimePlan{inputRate: inspection.LivePlan.InputAudioSampleRate, outputRate: inspection.LivePlan.OutputAudioSampleRate, maxDuration: inspection.LivePlan.MaxDuration, providerClose: inspection.LivePlan.ProviderCloseExpected}
}

func replayScheduledAudioInputs(turns []session.LiveReplayAudioTurn) []ScheduledAudioInput {
	inputs := make([]ScheduledAudioInput, 0, len(turns))
	for index, turn := range turns {
		var pcm []byte
		for _, chunk := range turn.Chunks {
			pcm = append(pcm, codec.EncodePCM16(chunk)...)
		}
		if len(pcm) == 0 {
			continue
		}
		inputs = append(inputs, ScheduledAudioInput{AfterCompletedTurns: index, PCM: pcm, EndOfTurn: true})
	}
	return inputs
}

func runReplayCapture(ctx context.Context, out io.Writer, service replay.Service, path string) (runErr error) {
	capture, err := service.Replay(ctx, path)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, capture.Close()) }()
	runErr = capture.Drain(ctx, func(msg messages.StreamMessage) error { return writeSessionReplayMessage(out, msg) })
	return errors.Join(runErr, capture.Err())
}
