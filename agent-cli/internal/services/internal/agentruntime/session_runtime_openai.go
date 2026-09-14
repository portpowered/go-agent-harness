// This file owns OpenAI-specific session-runtime recording, websocket replay planning, and realtime session inferencer construction.
package agentruntime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// SessionReplayCompleteClassification is the terminal classification emitted
// after a capture-derived replay consumes the complete ordered event stream.
const SessionReplayCompleteClassification = "replay_complete"

const (
	pcmHighByteShift                = 8
	sessionReplayDefaultMaxDuration = 3 * time.Second
)

func planOpenAIRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	if liveDialer == nil {
		return sessionRuntimePlan{}, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	liveDialer = observeSessionWire(liveDialer, opts)
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderOpenAI, sessionCfg.Model)
	clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
	inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, sessionProviderOpenAI, clientOwnedAudio || opts.RTCDeviceBinding.inputSelected())
	sessionInferencer, err := factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, recordingDialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if configurer, ok := sessionInferencer.(interface {
		SetSessionTurnDetection(*models.TurnDetectionConfig)
	}); ok {
		turnDetection := cloneSessionTurnDetection(opts.TurnDetection)
		if turnDetection == nil && opts.RTCDeviceBinding.inputSelected() && !clientOwnedAudio {
			turnDetection = &models.TurnDetectionConfig{Type: "semantic_vad"}
		}
		configurer.SetSessionTurnDetection(turnDetection)
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordOpenAI,
		provider:    sessionProviderOpenAI,
		model:       sessionCfg.Model,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		inferencer:  sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0,
		},
		flushCapture: func() error {
			return recordingDialer.FlushToFile(opts.RecordPath)
		},
		flushCaptureTo: func(path string) error {
			return recordingDialer.FlushToFile(path)
		},
		finalize: func(_ context.Context, out io.Writer) error {
			_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return err
		},
	}, nil
}

func planOpenAIReplayRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	prepared, replayDialer, inspection, err := prepareReplayLiveContext(ctx, opts, factory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	model := openAIReplayModel(inspection, replayDialer)
	prompt, promptProvided, barePromptReplay, bareAudioTurns := openAIReplayOpening(opts, inspection)
	bareAudioTurnReplay := len(bareAudioTurns) > 0
	scheduledAudio := len(opts.AudioInputs) > 0 || bareAudioTurnReplay
	// The initial provider configuration is captured wire data. The current
	// tool definitions remain on plan.loop for local execution, but are not
	// used to rebuild the provider handshake.
	sessionInferencer, err := newOpenAIReplayInferencer(factory, model, opts.Voice, replayDialer, scheduledAudio)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	if prepared != nil {
		sessionInferencer = prepared.WrapInferencer(sessionInferencer)
	}
	replayDone, replayErr := openAIReplayCompletion(prepared, replayDialer)
	plan := newOpenAIReplayPlan(opts, inspection, prepared, sessionInferencer, model, prompt, promptProvided, replayDone, replayErr)
	if bareAudioTurnReplay {
		// The recorded client frames are entirely self-driving: no
		// caller-supplied audio, --record-dir, or --max-duration bound is
		// needed to reach the recorded scheduled-audio turns.
		plan.audioInputs = bareAudioTurns
	}
	if barePromptReplay || bareAudioTurnReplay {
		plan.replayCompletion = func(reporter *sessionTerminalReporter) {
			reporter.markReplayComplete()
		}
	}
	return plan, nil
}

func openAIReplayModel(inspection runtimereplay.CaptureInspection, dialer transport.Dialer) string {
	model := strings.TrimSpace(inspection.Model)
	if model == "" {
		if replayDialer, ok := dialer.(sessionReplayDialer); ok {
			model = replayDialer.Model()
		}
	}
	if model == "" {
		return openAIRealtimeModel
	}
	return model
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

func newOpenAIReplayInferencer(factory sessionRuntimeFactory, model, voice string, dialer transport.Dialer, scheduledAudio bool) (messages.SessionInferencer, error) {
	return factory.newOpenAISessionInferencerForTools(config.OpenAIConfig{APIKey: "replay", Model: model}, voice, dialer, nil, scheduledAudio, models.InputAudioTranscriptionConfig{})
}

func openAIReplayCompletion(prepared runtimereplay.LivePrepared, dialer transport.Dialer) (<-chan struct{}, func() error) {
	if prepared != nil {
		return prepared.Done(), prepared.Err
	}
	if replayDialer, ok := dialer.(sessionReplayDialer); ok {
		return replayDialer.Done(), replayDialer.Err
	}
	return nil, nil
}

func newOpenAIReplayPlan(opts SessionRunOptions, inspection runtimereplay.CaptureInspection, prepared runtimereplay.LivePrepared, inferencer messages.SessionInferencer, model, prompt string, promptProvided bool, replayDone <-chan struct{}, replayErr func() error) sessionRuntimePlan {
	return sessionRuntimePlan{
		mode:                  sessionRuntimeModeReplayOpenAI,
		provider:              sessionProviderOpenAI,
		model:                 model,
		inputAudioSampleRate:  replayInputAudioSampleRate(inspection),
		outputAudioSampleRate: replayOutputAudioSampleRate(inspection),
		inferencer:            inferencer,
		replayPrepared:        prepared,
		announceTools:         replayAnnouncementToolDefinitions(opts.ToolDefinitions, inspection.InitialTools, inspection.InitialToolsKnown),
		loop: sessionLoopOptions{
			Prompt: prompt, PromptProvided: promptProvided,
			WaitForClose:             opts.WaitForClose || replayProviderCloseExpected(inspection),
			MaxDuration:              replayMaxDuration(inspection),
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			Done:                     replayDone, DoneErr: replayErr,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			if replayErr != nil {
				if err := replayErr(); err != nil {
					return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
				}
			}
			return nil
		},
	}
}

func flattenReplayAudioTurn(turn session.LiveReplayAudioTurn) []byte {
	var pcm []byte
	for _, chunk := range turn.Chunks {
		for _, sample := range chunk {
			pcm = append(pcm, byte(sample), byte(sample>>pcmHighByteShift))
		}
	}
	return pcm
}

func replayInputAudioSampleRate(inspection runtimereplay.CaptureInspection) int {
	if inspection.LivePlan == nil {
		return 0
	}
	return inspection.LivePlan.InputAudioSampleRate
}

func replayOutputAudioSampleRate(inspection runtimereplay.CaptureInspection) int {
	if inspection.LivePlan == nil {
		return 0
	}
	return inspection.LivePlan.OutputAudioSampleRate
}

func replayProviderCloseExpected(inspection runtimereplay.CaptureInspection) bool {
	return inspection.LivePlan != nil && inspection.LivePlan.ProviderCloseExpected
}

func replayMaxDuration(inspection runtimereplay.CaptureInspection) time.Duration {
	if inspection.LivePlan == nil || inspection.LivePlan.MaxDuration <= 0 {
		return sessionReplayDefaultMaxDuration
	}
	return inspection.LivePlan.MaxDuration
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

func buildOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(sessionCfg, voice, dialer, models.InputAudioTranscriptionConfig{})
}

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
