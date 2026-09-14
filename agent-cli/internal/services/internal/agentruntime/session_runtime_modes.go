package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	injectedSessionDefaultMaxDuration = 3 * time.Second
	turnReplayMaxDuration             = 200 * time.Millisecond
)

func planSessionRuntimeModeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.ReplayPath != "" {
		return planReplaySessionRuntimeContext(ctx, opts, factory)
	}
	if opts.SessionInferencer != nil {
		return planInjectedLiveSessionRuntime(opts)
	}
	switch {
	case opts.BareLive:
		return planBareLiveSessionRuntime(opts, factory)
	case opts.RecordPath != "":
		return planRecordSessionRuntime(opts, factory)
	case opts.BrowserToolsEnabled:
		return planBrowserLiveSessionRuntime(opts, factory)
	default:
		return planLiveSessionRuntime(opts, factory)
	}
}

func planInjectedLiveSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	if err := validateInjectedLiveSession(opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	provider := strings.ToLower(effectiveSessionProvider(opts))
	model, err := injectedSessionModel(opts, provider)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	interactive := browserToolsInteractiveLive(opts)
	return sessionRuntimePlan{
		mode: sessionRuntimeModeInjectedLive, provider: provider, model: model, inferencer: opts.SessionInferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.BareLive && !interactive && !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.BareLive || interactive || opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			MaxDuration:              injectedSessionMaxDuration(opts.BareLive || interactive),
			AdvertiseToolDefinitions: true,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0 && strings.EqualFold(provider, sessionProviderOpenAI),
			BareLive:                 opts.BareLive, BrowserToolsInteractive: interactive,
		},
	}, nil
}

func injectedSessionModel(opts SessionRunOptions, provider string) (string, error) {
	model := strings.TrimSpace(opts.Model)
	if model != "" {
		return model, nil
	}
	switch provider {
	case sessionProviderOpenAI:
		resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
		if err != nil {
			return "", err
		}
		return resolved.Model, nil
	case sessionProviderGrok:
		resolved, err := resolveGrokSessionConfig(opts)
		if err != nil {
			return "", err
		}
		return resolved.Model, nil
	default:
		return model, nil
	}
}

func injectedSessionMaxDuration(bareLive bool) time.Duration {
	if bareLive {
		return 0
	}
	return injectedSessionDefaultMaxDuration
}

func planReplaySessionRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.SessionInferencer != nil {
		return genericInjectedReplayPlan(opts), nil
	}
	service := opts.replayService
	if service == nil {
		service = replaywire.NewService()
	}
	inspection, err := service.InspectCapture(ctx, opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inspection.IsRealtime() {
		return planRealtimeReplay(ctx, opts, factory, inspection)
	}
	return planTurnReplay(opts, factory, service, inspection)
}

func genericInjectedReplayPlan(opts SessionRunOptions) sessionRuntimePlan {
	return sessionRuntimePlan{
		mode: sessionRuntimeModeReplayGeneric, capturePath: opts.ReplayPath,
		provider: strings.ToLower(strings.TrimSpace(opts.Provider)), model: opts.Model, inferencer: opts.SessionInferencer,
		loop: sessionLoopOptions{Prompt: opts.Prompt, WaitForClose: opts.WaitForClose, MaxDuration: injectedSessionDefaultMaxDuration, AdvertiseToolDefinitions: true},
	}
}

func planRealtimeReplay(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory, inspection runtimereplay.CaptureInspection) (sessionRuntimePlan, error) {
	if strings.EqualFold(inspection.Provider, sessionProviderOpenAI) {
		plan, err := planOpenAIReplayRuntimeContext(ctx, opts, factory)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		plan.replayIntegrityWarning = inspection.IntegrityWarning
		return plan, nil
	}
	plan, err := planGrokReplayRuntimeContext(ctx, opts, factory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.replayIntegrityWarning = inspection.IntegrityWarning
	return plan, nil
}

func planTurnReplay(opts SessionRunOptions, factory sessionRuntimeFactory, service runtimereplay.Service, inspection runtimereplay.CaptureInspection) (sessionRuntimePlan, error) {
	var inferencer messages.SessionInferencer
	if factory.newReplayInferencer != nil {
		inferencer = factory.newReplayInferencer(opts.ReplayPath)
	}
	return sessionRuntimePlan{
		mode: sessionRuntimeModeReplayGeneric, capturePath: opts.ReplayPath,
		replayIntegrityWarning: inspection.IntegrityWarning, loopOut: io.Discard, inferencer: inferencer,
		loop: sessionLoopOptions{Prompt: opts.Prompt, MaxDuration: turnReplayMaxDuration},
		finalize: func(ctx context.Context, out io.Writer) error {
			return runReplayCapture(ctx, out, service, opts.ReplayPath)
		},
	}, nil
}

func prepareReplayLiveContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (runtimereplay.LivePrepared, transport.Dialer, runtimereplay.CaptureInspection, error) {
	service := opts.replayService
	if service == nil {
		service = replaywire.NewService()
	}
	timing := session.LiveReplayTimingFast
	if normalizedSessionReplayTiming(opts.ReplayTiming) == sessionReplayTimingRecorded {
		timing = session.LiveReplayTimingRealtime
	}
	prepared, err := service.PrepareLive(ctx, runtimereplay.LiveRequest{SourcePath: opts.ReplayPath, Timing: timing})
	if err != nil {
		return nil, nil, runtimereplay.CaptureInspection{}, err
	}
	inspection := prepared.Inspection()
	inner, err := replayDialerForOptions(opts, factory)
	if err != nil {
		if closeErr := prepared.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, nil, runtimereplay.CaptureInspection{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	return prepared, prepared.WrapDialer(inner), inspection, nil
}

func replayDialerForOptions(opts SessionRunOptions, factory sessionRuntimeFactory) (transport.Dialer, error) {
	if opts.replayService != nil || factory.newReplayDialer == nil {
		return nil, nil
	}
	if normalizedSessionReplayTiming(opts.ReplayTiming) == sessionReplayTimingRecorded && factory.newRecordedTimingReplayDialer != nil {
		return factory.newRecordedTimingReplayDialer(opts.ReplayPath)
	}
	return factory.newReplayDialer(opts.ReplayPath)
}

func runReplayCapture(ctx context.Context, out io.Writer, service runtimereplay.Service, path string) error {
	replay, err := service.Replay(ctx, path)
	if err != nil {
		return err
	}
	renderer := newSessionReplayRenderer(out, sessionTerminalReporterFromContext(ctx))
	defer func() { _ = replay.Close() }() //nolint:errcheck // bounded best-effort close
	return drainReplayCapture(ctx, replay, renderer)
}

func drainReplayCapture(ctx context.Context, replay runtimereplay.CaptureReplay, renderer *sessionReplayRenderer) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-replay.Done():
			return drainReplayMessages(replay, renderer)
		case msg, ok := <-replay.Receive():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(renderer, msg); err != nil {
				return err
			}
		}
	}
}

func drainReplayMessages(replay runtimereplay.CaptureReplay, renderer *sessionReplayRenderer) error {
	for msg := range replay.Receive() {
		if err := writeSessionReplayMessage(renderer, msg); err != nil {
			return err
		}
	}
	return replay.Err()
}
