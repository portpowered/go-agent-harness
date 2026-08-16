// This file owns the shared session-runtime modes, factories, plan state, generic planning and dispatch, execution, and cross-provider error handling.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

func missingOwnedSessionDialerError(provider string) error {
	return fmt.Errorf("%s session runtime requires an injected websocket dialer", provider)
}
