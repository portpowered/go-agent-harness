// This file owns Grok-specific session-runtime recording, websocket replay planning, and session inferencer construction.
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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func planGrokRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		return factory.newGrokSessionInferencerForTools(sessionCfg, dialer, opts.ToolDefinitions)
	}
	plan := sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		provider:    sessionProviderGrok,
		model:       sessionCfg.Model,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath),
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
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
				dialer = grok.NewDefaultWebSocketDialer()
			}
			capture, err := opts.recordingService.RecordProviderSession(opts.providerCaptureService, runtimerecording.ProviderSessionOptions{
				Destination: destination, Provider: sessionProviderGrok, Model: sessionCfg.Model,
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

func planGrokReplayRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	prepared, replayDialer, inspection, err := prepareReplayLiveContext(ctx, opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	model := inspection.Model
	if strings.TrimSpace(model) == "" {
		model = "grok-replay"
	}
	sessionInferencer, err := factory.newGrokSessionInferencerForTools(config.GrokConfig{
		APIKey: "replay",
		Model:  model,
	}, replayDialer, nil)
	if err != nil {
		return sessionRuntimePlan{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err))
	}
	sessionInferencer = prepared.WrapInferencer(sessionInferencer)
	replayDone, replayErr := prepared.Done(), prepared.Err
	return sessionRuntimePlan{
		mode:                  sessionRuntimeModeReplayGrok,
		provider:              sessionProviderGrok,
		model:                 model,
		inputAudioSampleRate:  replayInputAudioSampleRate(inspection),
		outputAudioSampleRate: replayOutputAudioSampleRate(inspection),
		inferencer:            sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: opts.WaitForClose || replayProviderCloseExpected(inspection),
			MaxDuration:  replayMaxDuration(inspection),
			Done:         replayDone,
			DoneErr:      replayErr,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			var err error
			if replayErr != nil {
				if replayFailure := replayErr(); replayFailure != nil {
					err = fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, replayFailure)
				}
			}
			return errors.Join(err, prepared.Close())
		},
	}, nil
}

func buildGrokSessionInferencer(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
	return buildGrokSessionInferencerWithTools(sessionCfg, dialer, nil)
}

func buildGrokSessionInferencerWithTools(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	opts := make([]grok.Option, 0, 1)
	opts = append(opts, grok.WithWebSocketDialer(dialer))
	return NewGrokSessionInferencerWithToolsAndOptions(sessionCfg, toolDefinitions, opts...)
}
