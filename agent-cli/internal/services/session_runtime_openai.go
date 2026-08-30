// This file owns OpenAI-specific session-runtime recording, websocket replay planning, and realtime session inferencer construction.
package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
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
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderOpenAI, sessionCfg.Model)
	clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
	inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, sessionProviderOpenAI, clientOwnedAudio)
	sessionInferencer, err := factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, recordingDialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
	if err != nil {
		return sessionRuntimePlan{}, err
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
		finalize: func(_ context.Context, out io.Writer) error {
			_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return err
		},
	}, nil
}

func planOpenAIReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	replayDialer, err := factory.newReplayDialer(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	configuration, err := loadReplaySessionConfiguration(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	model := configuration.model
	if strings.TrimSpace(model) == "" {
		model = replayDialer.Model()
	}
	if strings.TrimSpace(model) == "" {
		model = openAIRealtimeModel
	}
	prompt := opts.Prompt
	promptProvided := opts.PromptProvided || prompt != ""
	barePromptReplay := false
	var bareAudioTurns []ScheduledAudioInput
	if !promptProvided {
		capturedPrompt, promptErr := loadReplaySessionPrompt(opts.ReplayPath)
		if promptErr != nil {
			return sessionRuntimePlan{}, promptErr
		}
		if capturedPrompt != nil {
			prompt = capturedPrompt.text
			promptProvided = true
			barePromptReplay = true
		} else if len(opts.AudioInputs) == 0 && !opts.ClientOwnsAudioTurnBoundaries {
			// No recorded text-prompt shape, no caller-supplied audio turns,
			// and no caller-owned streaming --audio-in source (which drives
			// its own committed audio independently of ScheduledAudioInput
			// and must keep reaching the strict replay dialer unchanged):
			// this may be the scheduled-audio-turn shape recorded by
			// --audio-in-turn/--record-dir. Reconstruct its turns directly from
			// the recorded client frames so a bare replay never needs the
			// caller to re-supply the original audio files.
			audioTurns, audioErr := loadReplaySessionAudioTurns(opts.ReplayPath)
			if audioErr != nil {
				return sessionRuntimePlan{}, audioErr
			}
			bareAudioTurns = audioTurns
		}
	}
	bareAudioTurnReplay := len(bareAudioTurns) > 0
	scheduledAudio := len(opts.AudioInputs) > 0 || bareAudioTurnReplay
	// The initial provider configuration is captured wire data. The current
	// tool definitions remain on plan.loop for local execution, but are not
	// used to rebuild the provider handshake.
	replayDialerWithConfiguration := newReplayInitialSessionUpdateDialer(replayDialer, configuration)
	sessionInferencer, err := factory.newOpenAISessionInferencerForTools(config.OpenAIConfig{
		APIKey: "replay",
		Model:  model,
	}, opts.Voice, replayDialerWithConfiguration, nil, scheduledAudio, models.InputAudioTranscriptionConfig{})
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	sessionInferencer = newWebSocketReplaySessionInferencer(sessionInferencer)
	plan := sessionRuntimePlan{
		mode:       sessionRuntimeModeReplayOpenAI,
		provider:   sessionProviderOpenAI,
		model:      model,
		inferencer: sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:                   prompt,
			PromptProvided:           promptProvided,
			WaitForClose:             opts.WaitForClose || captureHasEvent(opts.ReplayPath, sessionClosedEventType),
			MaxDuration:              3 * time.Second,
			CloseAfterScheduledAudio: scheduledAudio,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			if err := replayDialer.Err(); err != nil {
				return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
			}
			return nil
		},
	}
	if bareAudioTurnReplay {
		// The recorded client frames are entirely self-driving: no
		// caller-supplied audio, --record-dir, or --max-duration bound is
		// needed to reach the recorded scheduled-audio turns.
		plan.audioInputs = bareAudioTurns
	}
	if barePromptReplay || bareAudioTurnReplay {
		plan.replayCompletion = func(out io.Writer) error {
			_, err := fmt.Fprintln(out, "\n[session replay complete]")
			return err
		}
	}
	return plan, nil
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
