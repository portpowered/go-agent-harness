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
	sessionInferencer, err := factory.newOpenAISessionInf(sessionCfg, recordingDialer)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:       sessionRuntimeModeRecordOpenAI,
		provider:   sessionProviderOpenAI,
		model:      sessionCfg.Model,
		announce:   fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		inferencer: sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:         opts.Prompt,
			CloseAfterOpen: true,
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
	model := replayDialer.Model()
	if strings.TrimSpace(model) == "" {
		model = openAIRealtimeModel
	}
	sessionInferencer, err := factory.newOpenAISessionInf(config.OpenAIConfig{
		APIKey: "replay",
		Model:  model,
	}, replayDialer)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	return sessionRuntimePlan{
		mode:       sessionRuntimeModeReplayOpenAI,
		provider:   sessionProviderOpenAI,
		model:      model,
		inferencer: sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: opts.WaitForClose || captureHasEvent(opts.ReplayPath, sessionClosedEventType),
			MaxDuration:  3 * time.Second,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			if err := replayDialer.Err(); err != nil {
				return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
			}
			return nil
		},
	}, nil
}

func buildOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	opts := make([]oaiprovider.Option, 0, 1)
	opts = append(opts, oaiprovider.WithWebSocketDialer(dialer))
	return NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg, opts...)
}
