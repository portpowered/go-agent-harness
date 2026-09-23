// This file owns shared recording/replay mode composition and Grok-specific runtime construction.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
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
	dialer := func() transport.Dialer {
		if opts.WebSocketDialer != nil {
			return opts.WebSocketDialer
		}
		return grok.NewDefaultWebSocketDialer()
	}
	loop := sessionLoopOptions{
		Prompt:                   opts.Prompt,
		CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
		WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
		CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
	}
	return providerRecordingPlan(opts, sessionProviderGrok, sessionCfg.Model,
		fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath),
		sessionRuntimeModeRecordGrok, loop, dialer, build)
}

func planInjectedLiveSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	if err := validateInjectedLiveSession(opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	inferencer, capture, err := trackInjectedSession(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	provider, model, err := injectedSessionProviderModel(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	interactive := browserToolsInteractiveLive(opts)
	plan := sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		provider:   provider,
		model:      model,
		inferencer: inferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.BareLive && !interactive && !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.BareLive || interactive || opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			MaxDuration:              injectedSessionMaxDuration(opts.BareLive || interactive),
			AdvertiseToolDefinitions: true,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0 && strings.EqualFold(provider, sessionProviderOpenAI),
			BareLive:                 opts.BareLive,
			BrowserToolsInteractive:  interactive,
		},
	}
	if capture != nil {
		plan.capturePath = opts.RecordPath
		plan.recordingSession = capture
		plan.flushCapture = capture.FlushCapture
		plan.flushCaptureTo = capture.FlushToFile
		plan.finalize = sessionCaptureFinalizer(opts.RecordPath)
	}
	return plan, nil
}

func trackInjectedSession(opts SessionRunOptions) (messages.SessionInferencer, recording.SessionCapture, error) {
	inferencer := opts.SessionInferencer
	if opts.RecordPath == "" {
		return inferencer, nil, nil
	}
	if opts.RecordingService == nil {
		return nil, nil, errors.New("recording service is not configured")
	}
	capture, err := opts.RecordingService.TrackInjectedSession(inferencer, opts.RecordPath)
	if err != nil {
		return nil, nil, err
	}
	return capture, capture, nil
}

func injectedSessionProviderModel(opts SessionRunOptions) (string, string, error) {
	provider := strings.ToLower(effectiveSessionProvider(opts))
	model := strings.TrimSpace(opts.Model)
	if model != "" {
		return provider, model, nil
	}
	switch provider {
	case sessionProviderOpenAI:
		resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
		return provider, resolved.Model, err
	case sessionProviderGrok:
		resolved, err := resolveGrokSessionConfig(opts)
		return provider, resolved.Model, err
	default:
		return provider, model, nil
	}
}

func planGrokRTCRecording(opts SessionRunOptions, factory sessionRuntimeFactory, runtime SessionRTCRuntime) (sessionRTCProviderRecording, error) {
	sessionCfg, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return sessionRTCProviderRecording{}, err
	}
	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		return factory.newGrokSessionInferencerForTools(sessionCfg, dialer, opts.ToolDefinitions)
	}
	capture, err := opts.RecordingService.RecordProviderSession(opts.ProviderCaptureService, recording.ProviderSessionOptions{
		Destination: opts.RecordPath, Provider: sessionProviderGrok, Model: sessionCfg.Model,
		Dialer: &sessionRTCLazyDialer{runtime: runtime}, Clock: opts.Clock, Build: build,
	})
	if err != nil {
		return sessionRTCProviderRecording{}, err
	}
	return sessionRTCProviderRecording{
		provider: sessionProviderGrok, model: sessionCfg.Model, mode: sessionRuntimeModeRecordGrok,
		inferencer: capture, flush: capture.FlushCapture, flushTo: capture.FlushToFile,
		announce: fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath),
		finalize: sessionCaptureFinalizer(opts.RecordPath),
	}, nil
}

func planGrokReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planGrokReplayRuntimeContext(context.Background(), opts, factory)
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

func newGrokLiveSessionInferencer(opts SessionRunOptions, instructions string) (messages.SessionInferencer, string, error) {
	sessionCfg, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return nil, "", err
	}
	model := sessionCfg.Model
	request := deviceProbeSessionConfig(model, instructions, models.AudioFormatPCM16, models.AudioFormatPCM16)
	request.TurnDetection = cloneSessionTurnDetection(opts.TurnDetection)
	transcription, err := resolveSessionTranscription(opts, sessionProviderGrok, true)
	if err != nil {
		return nil, "", err
	}
	request.InputAudioTranscription = &transcription
	request.Tools = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	build := func(dialer transport.Dialer) (messages.SessionInferencer, error) {
		providerOptions := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey), grok.WithWebSocketDialer(dialer)}
		if strings.TrimSpace(sessionCfg.BaseURL) != "" {
			providerOptions = append(providerOptions, grok.WithBaseURL(sessionCfg.BaseURL))
		}
		providerGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOptions...)))
		if err != nil {
			return nil, fmt.Errorf("create Grok realtime session gateway: %w", err)
		}
		return inference.NewSessionGatewayInferencer(providerGateway, inference.WithSessionRequest(inference.SessionRequest{Config: request})), nil
	}
	return buildLiveProbeSession(opts, sessionProviderGrok, model, defaultDeviceProbeDialer(opts, sessionProviderGrok), build)
}

func defaultDeviceProbeDialer(opts SessionRunOptions, provider string) transport.Dialer {
	if opts.WebSocketDialer != nil {
		return opts.WebSocketDialer
	}
	if provider == sessionProviderOpenAI {
		return oaiprovider.NewDefaultWebSocketDialer()
	}
	return grok.NewDefaultWebSocketDialer()
}

func buildLiveProbeSession(opts SessionRunOptions, provider, model string, dialer transport.Dialer, build func(transport.Dialer) (messages.SessionInferencer, error)) (messages.SessionInferencer, string, error) {
	destination := strings.TrimSpace(opts.RecordSessionCapturePath)
	if destination == "" {
		inferencer, err := build(dialer)
		return inferencer, model, err
	}
	if opts.RecordingService == nil || opts.ProviderCaptureService == nil {
		return nil, "", fmt.Errorf("recording services are not configured")
	}
	capture, err := opts.RecordingService.RecordProviderSession(opts.ProviderCaptureService, recording.ProviderSessionOptions{
		Destination: destination, Provider: provider, Model: model, Dialer: dialer, Clock: opts.Clock, Build: build,
	})
	if err != nil {
		return nil, "", err
	}
	return capture, model, nil
}

type sessionDialerFactory func() transport.Dialer

func providerRecordingPlan(opts SessionRunOptions, provider, model, announcement string, mode sessionRuntimeMode, loop sessionLoopOptions, dialer sessionDialerFactory, build func(transport.Dialer) (messages.SessionInferencer, error)) (sessionRuntimePlan, error) {
	plan := sessionRuntimePlan{
		mode: mode, provider: provider, model: model, capturePath: opts.RecordPath,
		announce: announcement, loop: loop,
	}
	plan.recordingSetup = func(plan *sessionRuntimePlan) error {
		destination := opts.RecordPath
		if destination == "" {
			providerCapture, ok := plan.liveEvidence.(recording.ProviderCapture)
			if !ok {
				return errors.New("recording service does not expose its provider capture path")
			}
			destination = providerCapture.ProviderCapturePath()
		}
		capture, err := opts.RecordingService.RecordProviderSession(opts.ProviderCaptureService, recording.ProviderSessionOptions{
			Destination: destination, Provider: provider, Model: model,
			Dialer: dialer(), Clock: opts.Clock, Build: build,
		})
		if err != nil {
			return err
		}
		configureRecordedAudio(capture, plan.inputAudioSampleRate, plan.outputAudioSampleRate)
		plan.inferencer = capture
		plan.recordingSession = capture
		plan.flushCapture = capture.FlushCapture
		plan.flushCaptureTo = capture.FlushToFile
		plan.capturePath = destination
		return nil
	}
	if opts.RecordPath == "" {
		return plan, nil
	}
	if err := plan.recordingSetup(&plan); err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.recordingSetup = nil
	plan.finalize = func(_ context.Context, out io.Writer) error {
		_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
		return err
	}
	return plan, nil
}

func configureRecordedAudio(capture messages.SessionInferencer, inputRate, outputRate int) {
	if configurer, ok := capture.(runtimeAudioOutputConfigurer); ok && outputRate > 0 {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(outputRate))
	}
	if configurer, ok := capture.(runtimeAudioInputConfigurer); ok && inputRate > 0 {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(inputRate))
	}
}

func planGrokReplayRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	prepared, dialer, inspection, err := prepareReplayLiveContext(ctx, opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	model := strings.TrimSpace(inspection.Model)
	if model == "" {
		model = "grok-replay"
	}
	inferencer, err := factory.newGrokSessionInferencerForTools(config.GrokConfig{APIKey: "replay", Model: model}, dialer, nil)
	if err != nil {
		return sessionRuntimePlan{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err))
	}
	inferencer = prepared.WrapInferencer(inferencer)
	plan := replayInspectionPlan(inspection)
	return sessionRuntimePlan{
		mode: sessionRuntimeModeReplayGrok, provider: sessionProviderGrok, model: model,
		inputAudioSampleRate: plan.inputRate, outputAudioSampleRate: plan.outputRate, inferencer: inferencer,
		announceTools: []messages.ToolDefinition{},
		loop:          sessionLoopOptions{Prompt: opts.Prompt, WaitForClose: opts.WaitForClose || plan.providerClose, MaxDuration: plan.maxDuration, Done: prepared.Done(), DoneErr: prepared.Err},
		finalize:      func(_ context.Context, _ io.Writer) error { return errors.Join(prepared.Err(), prepared.Close()) },
	}, nil
}

func prepareReplayLiveContext(ctx context.Context, opts SessionRunOptions) (replay.LivePrepared, transport.Dialer, replay.CaptureInspection, error) {
	if opts.ReplayService == nil {
		return nil, nil, replay.CaptureInspection{}, errors.New("replay service is not configured")
	}
	timing := session.LiveReplayTimingFast
	if normalizedSessionReplayTiming(opts.ReplayTiming) == sessionReplayTimingRecorded {
		timing = session.LiveReplayTimingRealtime
	}
	prepared, err := opts.ReplayService.PrepareLive(ctx, replay.LiveRequest{SourcePath: opts.ReplayPath, Timing: timing})
	if err != nil {
		return nil, nil, replay.CaptureInspection{}, err
	}
	inspection := prepared.Inspection()
	if inspection.LivePlan == nil {
		return nil, nil, replay.CaptureInspection{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s has no admitted live plan", opts.ReplayPath))
	}
	return prepared, prepared.WrapDialer(nil), inspection, nil
}

func closePreparedReplay(prepared replay.LivePrepared, primary error) error {
	if prepared == nil {
		return primary
	}
	return errors.Join(primary, prepared.Close())
}

func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := sessionterminalwire.NewReporter()
	opts.terminalReporter = reporter
	reporter.MarkRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.finishTranscript())
		return errors.Join(runErr, reporter.Publish(out, runErr))
	}
}

func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.SetLivenessClock(opts.livenessClock)
		opts.observer.SetToolResultsEnabled(opts.ToolExecutor != nil)
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observed, opts)...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create session agent loop: %w", err)
	}
	var deviceErrors <-chan error
	if opts.rtcDeviceBinding != nil {
		deviceErrors = opts.rtcDeviceBinding.Errors()
	}
	return loop, observed, deviceErrors, nil
}

func sessionStreamDeadline(opts sessionLoopOptions) (<-chan time.Time, func(), error) {
	if opts.MaxDuration <= 0 {
		return nil, func() {}, nil
	}
	if opts.audioService == nil {
		return nil, nil, errors.New("audio service is required for session duration timing")
	}
	timer, err := opts.audioService.NewTimer(opts.clockSource, opts.MaxDuration)
	if err != nil {
		return nil, nil, err
	}
	return timer.C(), func() { timer.Stop() }, nil
}

func bindSessionLoopInputs(runCtx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) error {
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	return nil
}

func stopAndDrainSessionTimer(timer platformclock.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C():
	default:
	}
}
