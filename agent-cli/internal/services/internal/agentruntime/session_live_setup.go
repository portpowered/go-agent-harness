package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// prepareSessionStreamOutput gives an unowned stream its terminal renderer.
// The returned finalizer preserves transcript errors before publishing status.
func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := terminalwire.NewReporter()
	opts.terminalReporter = reporter
	reporter.MarkRunStarted()
	terminalService := terminalwire.NewService()
	renderer := terminalService.NewTranscriptRenderer(out, reporter.ObserveStreamMessage)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.Finish())
		return errors.Join(runErr, reporter.Publish(out, runErr))
	}
}

func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
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

// configureLoopObserver installs the shared observer for all runners,
// including duration runs that invoke plan.loop directly.
func (p sessionRuntimePlan) configureLoopObserver(loop *sessionLoopOptions) {
	if loop == nil {
		return
	}
	if loop.observer == nil {
		loop.observer = newSessionProgressObserver(p.diagnostics, p.metricsRecorder, p.provider, p.model)
	}
	loop.observer.streamObserver = p.streamObserver
	loop.observer.runtime = p.runtime
	loop.observer.cancellationIntent = loop.cancellationIntent
	loop.observer.requireSessionUpdated = loop.RequireSessionUpdated
	loop.observer.scheduledAudioDispatch = loop.ScheduledAudioDispatch
	if p.turnRuntime != nil && p.turnRuntime.Continuation() != nil {
		loop.observer.lifecycle = p.turnRuntime.Continuation()
	}
}

func (p sessionRuntimePlan) finalizationPorts(artifacts duration.ArtifactLifecycle, recordArtifacts bool) duration.FinalizationPorts {
	ports := duration.FinalizationPorts{
		CloseCapabilities: func() error {
			if p.capabilityCoordinator == nil {
				return nil
			}
			return p.capabilityCoordinator.Close()
		},
		CloseSession: p.closeSession,
		CloseRuntime: func() error {
			if p.rtcRuntime == nil {
				return nil
			}
			return p.rtcRuntime.Close()
		},
		FlushCapture: p.flushCapture,
		ReleaseCapture: func() error {
			if p.captureClaim == nil {
				return nil
			}
			return wrapSessionRuntimeError(p, p.captureClaim.release())
		},
		Artifacts: artifacts,
	}
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = terminalwire.NewReporter()
	}
	if recordArtifacts {
		ports.RecordArtifactFinalization = reporter.RecordArtifactFinalization
	}
	if p.replayCompletion != nil {
		ports.HasIndependentFailure = terminalwire.HasIndependentFailure
		ports.CompleteReplay = func() { p.replayCompletion(reporter) }
	}
	ports.PublishTerminal = reporter.Publish
	if p.finalize != nil {
		ports.Finalize = func(ctx context.Context, out io.Writer) error {
			if reporter := p.loop.terminalReporter; reporter != nil {
				ctx = terminalwire.WithReporter(ctx, reporter)
			}
			return wrapSessionRuntimeError(p, p.finalize(ctx, out))
		}
	}
	return ports
}

func executeSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, clock duration.TimerScheduler, admitted duration.AdmissionInferencer) error {
	service := durationwire.NewService()
	reporter := plan.loop.terminalReporter
	if reporter == nil {
		reporter = terminalwire.NewReporter()
		plan.loop.terminalReporter = reporter
	}
	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	sourceClock, _ := plan.clockSource.(duration.TimerScheduler)
	var invoke func(context.Context, io.Writer, duration.TimerScheduler) error
	if plan.inferencer != nil {
		invoke = func(runCtx context.Context, _ io.Writer, selectedClock duration.TimerScheduler) error {
			reporter.MarkRunStarted()
			if err := runSessionDurationInvocation(runCtx, loopOut, plan.inferencer, plan.loop, maxDuration, selectedClock, admitted); err != nil {
				return wrapSessionRuntimeError(plan, wrapSessionPhaseError("run session loop", err))
			}
			return nil
		}
	}
	return service.Execute(duration.ExecutionRequest{
		Context:       ctx,
		Output:        out,
		MaxDuration:   maxDuration,
		Clock:         clock,
		SourceClock:   sourceClock,
		FallbackClock: plan.loop.livenessClock,
		Prepare: func(runCtx context.Context, runOut io.Writer) error {
			if plan.replayIntegrityWarning != "" {
				if _, err := fmt.Fprintln(runOut, plan.replayIntegrityWarning); err != nil {
					return err
				}
			}
			if err := plan.bindRTC(runCtx, nil); err != nil {
				return err
			}
			if err := plan.writeAnnouncements(runOut, true); err != nil {
				return wrapSessionRuntimeError(plan, err)
			}
			plan.configureLoopObserver(&plan.loop)
			return nil
		},
		Run:          invoke,
		Finalization: plan.finalizationPorts(nil, true),
	})
}

type sessionRuntimeSetup struct {
	options                SessionRunOptions
	recordingClaim         *sessionRecordingClaim
	capabilityCoordinator  SessionCapabilityCoordinator
	selection              SessionRuntimeSelection
	scheduledAudioDispatch ScheduledAudioDispatchPolicy
}

func planSessionRuntimeWithFactory(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	var setup sessionRuntimeSetup
	defer func() {
		if planErr != nil && setup.recordingClaim != nil {
			_ = setup.recordingClaim.release()
		}
		if planErr != nil {
			closeSessionCapabilityIfNeeded(setup.capabilityCoordinator, &planErr)
		}
	}()

	setup, planErr = prepareSessionRuntimeSetup(opts)
	if planErr != nil {
		return sessionRuntimePlan{}, planErr
	}
	plan, planErr = createSessionRuntimePlan(setup, factory)
	if planErr != nil {
		return sessionRuntimePlan{}, planErr
	}
	if planErr = configureSessionRuntimePlan(ctx, setup, &plan); planErr != nil {
		return sessionRuntimePlan{}, planErr
	}
	plan.capabilityCoordinator = setup.capabilityCoordinator
	return wireSessionRecordingClaim(plan, setup.recordingClaim), nil
}

func prepareSessionRuntimeSetup(opts SessionRunOptions) (sessionRuntimeSetup, error) {
	setup := sessionRuntimeSetup{}
	opts.AudioService = sessionAudioService(opts.AudioService)
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return setup, err
	}
	setup.recordingClaim = claim
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	filesystemPolicy := opts.FilesystemPolicy
	if filesystemPolicy == nil {
		filesystemPolicy, err = tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
		if err != nil {
			return setup, fmt.Errorf("resolve filesystem scope: %w", err)
		}
	}
	opts.FilesystemPolicy = filesystemPolicy
	opts.WorkDir = filesystemPolicy.PrimaryRoot()
	opts.AllowPaths = filesystemPolicy.AdditionalRoots()
	opts, setup.capabilityCoordinator = prepareSessionCapabilityCoordinator(opts)
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(opts.AudioInputs)); err != nil {
		return setup, err
	}
	setup.scheduledAudioDispatch = scheduledAudioDispatchPolicyForOptions(opts)
	if opts.ReplayPath == "" {
		opts.Provider = effectiveSessionProvider(opts)
	}
	setup.selection, err = resolveSessionRuntimeSelection(opts)
	if err != nil {
		return setup, err
	}
	setup.options = opts
	return setup, nil
}

func createSessionRuntimePlan(setup sessionRuntimeSetup, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if setup.selection.Transport == SessionTransportWebRTC && setup.options.ReplayPath == "" {
		return planWebRTCSessionRuntime(setup.options, setup.selection, factory)
	}
	return planSessionRuntimeMode(setup.options, factory)
}

func configureSessionRuntimePlan(ctx context.Context, setup sessionRuntimeSetup, plan *sessionRuntimePlan) error {
	if err := configureSessionRuntimePlanBase(setup, plan); err != nil {
		return err
	}
	if err := prepareSessionRuntimePlanTools(ctx, setup.options, plan); err != nil {
		return err
	}
	if err := configureSessionRuntimePlanAudio(ctx, setup.options, plan); err != nil {
		return err
	}
	configureSessionRuntimePlanObservers(setup.options, plan)
	return validateSessionRuntimePlanTransport(setup, plan)
}

func configureSessionRuntimePlanBase(setup sessionRuntimeSetup, plan *sessionRuntimePlan) error {
	opts := setup.options
	plan.diagnostics = opts.Diagnostics
	plan.metricsRecorder = opts.MetricsRecorder
	plan.streamObserver = opts.StreamObserver
	if plan.audioInputs == nil {
		plan.audioInputs = opts.AudioInputs
	}
	plan.scheduledAudioDispatch = setup.scheduledAudioDispatch
	plan.filesystemPolicy = opts.FilesystemPolicy
	plan.clockSource = platformclock.Ensure(opts.Clock)
	plan.runtime = newSessionRuntimeObservationRecorder(opts.RuntimeObserver, plan.clockSource)
	plan.loop.runtime = plan.runtime
	plan.loop.clockSource = plan.clockSource
	plan.loop.audioService = opts.AudioService
	plan.loop.livenessClock = opts.LivenessClock
	if plan.loop.livenessClock != nil {
		return nil
	}
	if opts.AudioService != nil {
		clock, err := opts.AudioService.NewClock(plan.clockSource)
		if err != nil {
			return err
		}
		plan.loop.livenessClock = clock
		return nil
	}
	if timerSource, ok := plan.clockSource.(platformclock.TimerSource); ok {
		plan.loop.livenessClock = timerSource
	} else {
		plan.loop.livenessClock = platformclock.Real{}
	}
	return nil
}

func prepareSessionRuntimePlanTools(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	plan.loop.BareLive = plan.loop.BareLive || opts.BareLive
	plan.loop.cancellationIntent = opts.CancellationIntent
	plan.loop.toolDiagnostics = opts.ToolDiagnostics
	plan.loop.SessionUpdatedTimeout = opts.SessionUpdatedTimeout
	plan.loop.AudioInterruptions = opts.AudioInterruptions
	plan.rtcDeviceRequest = opts.RTCBinding
	plan.deviceService = opts.DeviceService
	plan.configureLoopObserver(&plan.loop)
	plan.rtcDeviceRequest.OutputVoice = opts.Voice
	plan.rtcDeviceRequest.BypassSelfHearing = plan.rtcDeviceRequest.BypassSelfHearing || opts.ReplayPath != ""
	plan.loop.ToolExecutor = opts.ToolExecutor
	plan.loop.ToolDefinitions = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	if err := prepareSessionRuntimeToolsAndAudio(ctx, opts, plan); err != nil {
		return err
	}
	policySnapshot := plan.turnRuntime.InteractiveToolPolicy()
	plan.interactivePolicy = policySnapshot
	plan.loop.InteractiveToolPolicy = policySnapshot
	plan.loop.ToolDefinitionBase = append([]messages.ToolDefinition(nil), opts.ToolDefinitionBase...)
	plan.loop.RefreshToolDefinitions = opts.RefreshToolDefinitions
	plan.loop.BrowserWatch = opts.BrowserWatch
	plan.loop.ToolExecutionTimeout = opts.ToolExecutionTimeout
	plan.loop.ScheduledAudioDispatch = plan.scheduledAudioDispatch
	plan.configureLoopObserver(&plan.loop)
	return nil
}

func configureSessionRuntimePlanAudio(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if opts.AudioService == nil {
		return errors.New("audio service is required for session rate resolution")
	}
	plan.voiceGainDB = opts.AudioService.VoiceGainDB(opts.Voice)
	if err := configureSessionAudioContract(opts, plan); err != nil {
		return err
	}
	inputs, err := opts.AudioService.ConvertScheduledInputs(ctx, plan.audioInputs, plan.inputAudioSampleRate)
	if err != nil {
		return err
	}
	plan.audioInputs = inputs
	if plan.loop.observer != nil {
		plan.loop.observer.scheduleAudioInputs(plan.audioInputs)
	}
	plan.loop.InputAudioSampleRate = plan.inputAudioSampleRate
	if plan.rtcDeviceRequest.HasOutput() && plan.outputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = plan.outputAudioSampleRate
	}
	if plan.rtcDeviceRequest.HasInput() && plan.inputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = plan.inputAudioSampleRate
	}
	return nil
}

func configureSessionRuntimePlanObservers(opts SessionRunOptions, plan *sessionRuntimePlan) {
	observability := opts.Observability
	plan.rtcDeviceRequest.PlaybackObserver = combineRTCDevicePlaybackObservers(
		plan.rtcDeviceRequest.PlaybackObserver,
		sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(plan.diagnostics)),
		sessionPlaybackObservabilityObserver(observability.MetricSampler, observability.Logger),
	)
	plan.rtcDeviceRequest.PlaybackReceiptObserver = combineRTCDevicePlaybackReceiptObservers(
		plan.rtcDeviceRequest.PlaybackReceiptObserver,
		func(receipt audio.PlaybackReceipt) {
			if plan.runtime != nil {
				plan.runtime.audioPlaybackReceipt(receipt)
			}
		},
	)
	plan.rtcDeviceRequest.CaptureObserver = combineRTCDeviceCaptureObservers(
		plan.rtcDeviceRequest.CaptureObserver,
		sessionCaptureObservabilityObserver(observability.MetricSampler, observability.Logger),
	)
}

func validateSessionRuntimePlanTransport(setup sessionRuntimeSetup, plan *sessionRuntimePlan) error {
	plan.selection = setup.selection
	plan.transport = setup.selection.Transport
	plan.signalingEndpoint = setup.selection.SignalingEndpoint
	plan.mediaSource = setup.selection.MediaSource
	if plan.rtcRuntime == nil && setup.selection.Transport == SessionTransportWebRTC && setup.options.ReplayPath == "" {
		return wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	return nil
}
