// This file owns Grok-specific session-runtime recording, websocket replay planning, and session inferencer construction.
package agentruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func newGrokDeviceProbeSessionInferencer(opts SessionRunOptions, instructions string) (messages.SessionInferencer, string, error) {
	sessionConfig, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return nil, "", err
	}
	model := sessionConfig.Model
	request := deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
	request.TurnDetection = cloneSessionTurnDetection(opts.TurnDetection)
	transcription, err := resolveSessionTranscription(opts, sessionProviderGrok, true)
	if err != nil {
		return nil, "", err
	}
	request.InputAudioTranscription = &transcription
	request.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	dialer, recorder := resolveSessionWebSocketDialer(opts, sessionProviderGrok, model, func() transport.Dialer { return grok.NewDefaultWebSocketDialer() })
	providerOpts := []grok.Option{grok.WithAPIKey(sessionConfig.APIKey), grok.WithWebSocketDialer(dialer)}
	if strings.TrimSpace(sessionConfig.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionConfig.BaseURL))
	}
	providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, "", fmt.Errorf("create Grok realtime session gateway: %w", err)
	}
	inferencer := inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: request}))
	return wrapSessionInferencerCaptureFlush(inferencer, recorder, opts.RecordSessionCapturePath), model, nil
}

func planGrokRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	if liveDialer == nil {
		return sessionRuntimePlan{}, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	liveDialer = observeSessionWire(liveDialer, opts)
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderGrok, sessionCfg.Model)
	sessionInferencer, err := factory.newGrokSessionInferencerForTools(sessionCfg, recordingDialer, opts.ToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		provider:    sessionProviderGrok,
		model:       sessionCfg.Model,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath),
		inferencer:  sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
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

func planGrokReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	configuration, err := loadReplaySessionConfiguration(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	replayDialer, err := factory.replayDialer(opts.ReplayPath, opts.ReplayTiming)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	model := configuration.model
	if strings.TrimSpace(model) == "" {
		model = replayDialer.Model()
	}
	if strings.TrimSpace(model) == "" {
		model = "grok-replay"
	}
	// The initial provider configuration is captured wire data. The current
	// tool definitions remain on plan.loop for local execution, but are not
	// used to rebuild the provider handshake.
	replayDialerWithConfiguration := newReplayInitialSessionUpdateDialer(replayDialer, configuration)
	sessionInferencer, err := factory.newGrokSessionInferencerForTools(config.GrokConfig{
		APIKey: "replay",
		Model:  model,
	}, replayDialerWithConfiguration, nil)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	sessionInferencer = newWebSocketReplaySessionInferencer(sessionInferencer)
	return sessionRuntimePlan{
		mode:                  sessionRuntimeModeReplayGrok,
		provider:              sessionProviderGrok,
		model:                 model,
		inputAudioSampleRate:  configuration.inputAudioSampleRate,
		outputAudioSampleRate: configuration.outputAudioSampleRate,
		inferencer:            sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: opts.WaitForClose || grokReplayCaptureHasSessionClose(opts.ReplayPath),
			MaxDuration:  replayLoopMaxDuration(opts.ReplayPath, opts.ReplayTiming),
			Done:         replayDialer.Done(),
			DoneErr:      replayDialer.Err,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			if err := replayDialer.Err(); err != nil {
				return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
			}
			return nil
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
