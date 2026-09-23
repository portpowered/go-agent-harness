// This file owns OpenAI-specific session-runtime recording, websocket replay planning, and realtime session inferencer construction.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
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
	plan := sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordOpenAI,
		provider:    sessionProviderOpenAI,
		model:       sessionCfg.Model,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		inferencer:  nil,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0,
		},
		recordingSetup: func(plan *sessionRuntimePlan) error {
			destination := opts.RecordPath
			if destination == "" {
				providerCapture, ok := plan.liveEvidence.(runtimerecording.ProviderCapture)
				if !ok {
					return errors.New("recording service does not expose its provider capture path")
				}
				destination = providerCapture.ProviderCapturePath()
			}
			dialer := opts.WebSocketDialer
			if dialer == nil {
				dialer = oaiprovider.NewDefaultWebSocketDialer()
			}
			capture, err := opts.RecordingService.RecordProviderSession(opts.ProviderCaptureService, runtimerecording.ProviderSessionOptions{
				Destination: destination, Provider: sessionProviderOpenAI, Model: sessionCfg.Model,
				Dialer: observeSessionWire(dialer, opts), Clock: opts.Clock, Build: build,
			})
			if err != nil {
				return err
			}
			if configurer, ok := capture.(runtimeAudioOutputConfigurer); ok && plan.outputAudioSampleRate > 0 {
				configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(plan.outputAudioSampleRate))
			}
			if configurer, ok := capture.(runtimeAudioInputConfigurer); ok && plan.inputAudioSampleRate > 0 {
				configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(plan.inputAudioSampleRate))
			}
			plan.inferencer = capture
			plan.recordingSession = capture
			plan.flushCapture = capture.FlushCapture
			plan.flushCaptureTo = capture.FlushToFile
			plan.capturePath = destination
			return nil
		},
	}
	if opts.RecordPath != "" {
		if err := plan.recordingSetup(&plan); err != nil {
			return sessionRuntimePlan{}, err
		}
		plan.recordingSetup = nil
	}
	if opts.RecordPath != "" {
		plan.finalize = func(_ context.Context, out io.Writer) error {
			_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return err
		}
	}
	return plan, nil
}

func planOpenAIReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planOpenAIReplayRuntimeContext(context.Background(), opts, factory)
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
	prompt, promptProvided, barePromptReplay, bareAudioTurns := openAIReplayOpening(opts, inspection)
	bareAudioTurnReplay := len(bareAudioTurns) > 0
	scheduledAudio := len(opts.AudioInputs) > 0 || bareAudioTurnReplay
	inferencer, err := factory.newOpenAISessionInferencerForTools(config.OpenAIConfig{
		APIKey: "replay",
		Model:  model,
	}, opts.Voice, dialer, nil, scheduledAudio, models.InputAudioTranscriptionConfig{})
	if err != nil {
		return sessionRuntimePlan{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err))
	}
	inferencer = prepared.WrapInferencer(inferencer)
	replayDone, replayErr := prepared.Done(), prepared.Err
	plan := newOpenAIReplayPlan(opts, inspection, prepared, inferencer, model, prompt, promptProvided, replayDone, replayErr)
	if bareAudioTurnReplay {
		plan.audioInputs = bareAudioTurns
	}
	if barePromptReplay || bareAudioTurnReplay {
		plan.replayCompletion = func(reporter *sessionTerminalReporter) { reporter.markReplayComplete() }
	}
	return plan, nil
}

func openAIReplayOpening(opts SessionRunOptions, inspection runtimereplay.CaptureInspection) (string, bool, bool, []ScheduledAudioInput) {
	prompt := opts.Prompt
	promptProvided := opts.PromptProvided || prompt != ""
	if promptProvided {
		return prompt, true, false, nil
	}
	if inspection.LivePlan != nil && inspection.LivePlan.OpeningPromptPresent {
		return inspection.LivePlan.OpeningPrompt, true, true, nil
	}
	if len(opts.AudioInputs) > 0 || opts.ClientOwnsAudioTurnBoundaries || opts.roomReplay || inspection.LivePlan == nil {
		return prompt, false, false, nil
	}
	turns := make([]ScheduledAudioInput, 0, len(inspection.LivePlan.AudioTurns))
	for _, turn := range inspection.LivePlan.AudioTurns {
		turns = append(turns, ScheduledAudioInput{AfterCompletedTurns: len(turns), PCM: flattenReplayAudioTurn(turn), EndOfTurn: true})
	}
	return prompt, false, false, turns
}

func newOpenAIReplayPlan(opts SessionRunOptions, inspection runtimereplay.CaptureInspection, prepared runtimereplay.LivePrepared, inferencer messages.SessionInferencer, model, prompt string, promptProvided bool, replayDone <-chan struct{}, replayErr func() error) sessionRuntimePlan {
	return sessionRuntimePlan{
		mode:                  sessionRuntimeModeReplayOpenAI,
		provider:              sessionProviderOpenAI,
		model:                 model,
		inputAudioSampleRate:  replayInputAudioSampleRate(inspection),
		outputAudioSampleRate: replayOutputAudioSampleRate(inspection),
		inferencer:            inferencer,
		announceTools:         replayAnnouncementToolDefinitions(opts.ToolDefinitions, inspection.InitialTools, inspection.InitialToolsKnown),
		loop: sessionLoopOptions{
			Prompt: prompt, PromptProvided: promptProvided,
			WaitForClose:             opts.WaitForClose || replayProviderCloseExpected(inspection),
			MaxDuration:              replayMaxDuration(inspection),
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			Done:                     replayDone, DoneErr: replayErr,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			var err error
			if replayErr != nil {
				if replayFailure := replayErr(); replayFailure != nil {
					err = fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, replayFailure)
				}
			}
			return closePreparedReplay(prepared, err)
		},
	}
}

func closePreparedReplay(prepared runtimereplay.LivePrepared, runErr error) error {
	if prepared == nil {
		return runErr
	}
	return errors.Join(runErr, prepared.Close())
}

func flattenReplayAudioTurn(turn session.LiveReplayAudioTurn) []byte {
	var pcm []byte
	for _, chunk := range turn.Chunks {
		for _, sample := range chunk {
			pcm = append(pcm, byte(sample), byte(sample>>8))
		}
	}
	return pcm
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
