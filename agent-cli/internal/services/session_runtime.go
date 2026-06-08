package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

type sessionRuntimeMode string

const (
	sessionRuntimeModeInjectedLive  sessionRuntimeMode = "injected-live"
	sessionRuntimeModeReplayGeneric sessionRuntimeMode = "replay-generic"
	sessionRuntimeModeReplayGrok    sessionRuntimeMode = "replay-grok-websocket"
	sessionRuntimeModeReplayOpenAI  sessionRuntimeMode = "replay-openai-websocket"
	sessionRuntimeModeRecordGrok    sessionRuntimeMode = "record-grok"
	sessionRuntimeModeRecordOpenAI  sessionRuntimeMode = "record-openai"
)

type sessionRecordingDialer interface {
	grok.WebSocketDialer
	FlushToFile(path string) error
}

type sessionReplayDialer interface {
	grok.WebSocketDialer
	Done() <-chan struct{}
	Err() error
	Model() string
}

type sessionRuntimeFactory struct {
	newDefaultLiveDialer     func() grok.WebSocketDialer
	newRecordingDialer       func(grok.WebSocketDialer, string, string) sessionRecordingDialer
	newReplayDialer          func(string) (sessionReplayDialer, error)
	newReplayInferencer      func(string) messages.SessionInferencer
	newGrokSessionInferencer func(config.GrokConfig, grok.WebSocketDialer) (messages.SessionInferencer, error)
	newOpenAISessionInf      func(config.OpenAIConfig, grok.WebSocketDialer) (messages.SessionInferencer, error)
}

var defaultSessionRuntimeFactory = sessionRuntimeFactory{
	newDefaultLiveDialer: func() grok.WebSocketDialer {
		return grok.NewDefaultWebSocketDialer()
	},
	newRecordingDialer: func(inner grok.WebSocketDialer, providerName string, model string) sessionRecordingDialer {
		return gwtesting.NewRecordingWebSocketDialer(inner, providerName, model)
	},
	newReplayDialer: func(path string) (sessionReplayDialer, error) {
		return gwtesting.NewReplayWebSocketDialer(path)
	},
	newReplayInferencer: func(path string) messages.SessionInferencer {
		return gwtesting.NewReplaySessionInferencer(path)
	},
	newGrokSessionInferencer: func(sessionCfg config.GrokConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencer(sessionCfg, dialer)
	},
	newOpenAISessionInf: func(sessionCfg config.OpenAIConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencer(sessionCfg, dialer)
	},
}

type sessionRuntimePlan struct {
	mode         sessionRuntimeMode
	provider     string
	capturePath  string
	loopOut      io.Writer
	inferencer   messages.SessionInferencer
	loop         sessionLoopOptions
	announce     string
	flushCapture func() error
	finalize     func(context.Context, io.Writer) error
}

func (p sessionRuntimePlan) run(ctx context.Context, out io.Writer) error {
	if p.announce != "" {
		if _, err := fmt.Fprintln(out, p.announce); err != nil {
			return err
		}
	}

	loopOut := out
	if p.loopOut != nil {
		loopOut = p.loopOut
	}
	if p.inferencer != nil {
		if err := runAgentLoopSession(ctx, loopOut, p.inferencer, p.loop); err != nil {
			if p.flushCapture != nil {
				flushErr := p.flushCapture()
				return wrapSessionRuntimeError(p, errors.Join(
					wrapSessionPhaseError("run session loop", err),
					wrapSessionPhaseError("flush capture", flushErr),
				))
			}
			return wrapSessionRuntimeError(p, err)
		}
	}

	if p.flushCapture != nil {
		if err := p.flushCapture(); err != nil {
			return wrapSessionRuntimeError(p, wrapSessionPhaseError("flush capture", err))
		}
	}

	if p.finalize != nil {
		if err := p.finalize(ctx, out); err != nil {
			return wrapSessionRuntimeError(p, err)
		}
	}
	return nil
}

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithFactory(opts, defaultSessionRuntimeFactory)
}

func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.ReplayPath != "" {
		return planReplaySessionRuntime(opts, factory)
	}
	if opts.SessionInferencer != nil {
		if err := validateInjectedLiveSession(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return sessionRuntimePlan{
			mode:       sessionRuntimeModeInjectedLive,
			provider:   strings.ToLower(effectiveSessionProvider(opts)),
			inferencer: opts.SessionInferencer,
			loop: sessionLoopOptions{
				Prompt:         opts.Prompt,
				CloseAfterOpen: true,
				MaxDuration:    3 * time.Second,
			},
		}, nil
	}
	return planRecordSessionRuntime(opts, factory)
}

func planReplaySessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionInferencer := opts.SessionInferencer
	if sessionInferencer != nil {
		return sessionRuntimePlan{
			mode:        sessionRuntimeModeReplayGeneric,
			capturePath: opts.ReplayPath,
			inferencer:  sessionInferencer,
			loop: sessionLoopOptions{
				Prompt:      opts.Prompt,
				MaxDuration: 3 * time.Second,
			},
		}, nil
	}

	if _, err := os.Stat(opts.ReplayPath); err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}

	if usesWebSocketCapture(opts.ReplayPath) {
		if usesOpenAIWebSocketCapture(opts.ReplayPath) {
			return planOpenAIReplayRuntime(opts, factory)
		}
		return planGrokReplayRuntime(opts, factory)
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeReplayGeneric,
		capturePath: opts.ReplayPath,
		loopOut:     io.Discard,
		inferencer:  factory.newReplayInferencer(opts.ReplayPath),
		loop: sessionLoopOptions{
			Prompt:      opts.Prompt,
			MaxDuration: 200 * time.Millisecond,
		},
		finalize: func(ctx context.Context, out io.Writer) error {
			return replaySessionCapture(ctx, out, opts.ReplayPath)
		},
	}, nil
}

func planRecordSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if strings.EqualFold(effectiveSessionProvider(opts), sessionProviderOpenAI) {
		return planOpenAIRecordRuntime(opts, factory)
	}
	return planGrokRecordRuntime(opts, factory)
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
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderGrok, sessionCfg.Model)
	sessionInferencer, err := factory.newGrokSessionInferencer(sessionCfg, recordingDialer)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordGrok,
		provider:    sessionProviderGrok,
		capturePath: opts.RecordPath,
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

func planOpenAIRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderOpenAI, sessionCfg.Model)
	sessionInferencer, err := factory.newOpenAISessionInf(sessionCfg, recordingDialer)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordOpenAI,
		provider:    sessionProviderOpenAI,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
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
		capturePath: opts.ReplayPath,
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
		mode:        sessionRuntimeModeReplayOpenAI,
		provider:    sessionProviderOpenAI,
		capturePath: opts.ReplayPath,
		inferencer:  sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: captureHasEvent(opts.ReplayPath, sessionClosedEventType),
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

func wrapSessionRuntimeError(plan sessionRuntimePlan, err error) error {
	if err == nil {
		return nil
	}
	switch plan.mode {
	case sessionRuntimeModeRecordGrok, sessionRuntimeModeRecordOpenAI:
		return fmt.Errorf("record session capture %s: %w", plan.capturePath, err)
	case sessionRuntimeModeReplayGeneric, sessionRuntimeModeReplayGrok, sessionRuntimeModeReplayOpenAI:
		return fmt.Errorf("replay session capture %s: %w", plan.capturePath, err)
	default:
		return err
	}
}

func buildGrokSessionInferencer(sessionCfg config.GrokConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
	opts := make([]grok.Option, 0, 1)
	if dialer != nil {
		opts = append(opts, grok.WithWebSocketDialer(dialer))
	}
	return NewGrokSessionInferencerWithOptions(sessionCfg, opts...)
}

func buildOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
	opts := make([]oaiprovider.Option, 0, 1)
	if dialer != nil {
		opts = append(opts, oaiprovider.WithWebSocketDialer(newOpenAIWebSocketDialerAdapter(dialer)))
	}
	return NewOpenAIRealtimeSessionInferencerWithOptions(sessionCfg, opts...)
}
