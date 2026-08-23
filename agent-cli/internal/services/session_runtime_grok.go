// This file owns Grok-specific session-runtime recording, websocket replay planning, and session inferencer construction.
package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

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
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderGrok, sessionCfg.Model)
	sessionInferencer, err := factory.newGrokSessionInferencer(sessionCfg, recordingDialer)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		provider:    sessionProviderGrok,
		model:       sessionCfg.Model,
		announce:    fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath),
		inferencer:  sessionInferencer,
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

func planGrokReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	replayDialer, err := factory.newReplayDialer(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	model := replayDialer.Model()
	if strings.TrimSpace(model) == "" {
		model = "grok-replay"
	}
	sessionInferencer, err := factory.newGrokSessionInferencer(config.GrokConfig{
		APIKey: "replay",
		Model:  model,
	}, replayDialer)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	return sessionRuntimePlan{
		mode:        sessionRuntimeModeReplayGrok,
		provider:    sessionProviderGrok,
		model:       model,
		inferencer:  sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: grokReplayCaptureHasSessionClose(opts.ReplayPath),
			MaxDuration:  3 * time.Second,
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
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	opts := make([]grok.Option, 0, 1)
	opts = append(opts, grok.WithWebSocketDialer(dialer))
	return NewGrokSessionInferencerWithOptions(sessionCfg, opts...)
}
