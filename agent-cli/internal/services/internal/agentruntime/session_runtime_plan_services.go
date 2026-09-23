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
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithContext(context.Background(), opts)
}

//lint:ignore U1000 package tests exercise the context-free planning seam.
func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithFactoryAndContext(context.Background(), opts, factory)
}

func planSessionRuntimeWithContext(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	factory := opts.runtimeFactory
	if !factory.configured() {
		factory = newDefaultSessionRuntimeFactory()
	}
	return planSessionRuntimeWithFactoryAndContext(ctx, opts, factory)
}

func planSessionRuntimeWithFactoryAndContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	if ctx == nil {
		return sessionRuntimePlan{}, errors.New("session planning context is required")
	}
	if err := ctx.Err(); err != nil {
		return sessionRuntimePlan{}, err
	}
	recordingClaim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	opts, err = resolveSessionRuntimeFilesystem(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	var capabilityCoordinator SessionCapabilityCoordinator
	opts, capabilityCoordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		if planErr != nil && recordingClaim != nil {
			planErr = errors.Join(planErr, recordingClaim.release())
		}
		if planErr != nil {
			closeSessionCapabilityIfNeeded(capabilityCoordinator, &planErr)
		}
	}()
	interactivePolicy, err := resolveSessionInteractiveToolPolicy(opts, opts.ToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(opts.AudioInputs)); err != nil {
		return sessionRuntimePlan{}, err
	}
	if opts.ReplayPath == "" {
		// Replay keeps its capture-owned provider identity.
		opts.Provider = effectiveSessionProvider(opts)
	}
	selection, err := resolveSessionRuntimeSelection(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan, err = planSessionRuntimeForSelection(opts, selection, factory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return configureSessionRuntimePlan(ctx, plan, opts, selection, interactivePolicy, scheduledAudioDispatchPolicyForOptions(opts), capabilityCoordinator, recordingClaim)
}

func resolveSessionRuntimeFilesystem(opts SessionRunOptions) (SessionRunOptions, error) {
	filesystemPolicy := opts.FilesystemPolicy
	if filesystemPolicy == nil {
		var err error
		filesystemPolicy, err = tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
		if err != nil {
			return SessionRunOptions{}, fmt.Errorf("resolve filesystem scope: %w", err)
		}
	}
	opts.FilesystemPolicy = filesystemPolicy
	opts.WorkDir = filesystemPolicy.PrimaryRoot()
	opts.AllowPaths = filesystemPolicy.AdditionalRoots()
	return opts, nil
}

func planSessionRuntimeForSelection(opts SessionRunOptions, selection SessionRuntimeSelection, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return planWebRTCSessionRuntime(opts, selection, factory)
	}
	return planSessionRuntimeMode(opts, factory)
}

func configureSessionRuntimePlan(ctx context.Context, plan sessionRuntimePlan, opts SessionRunOptions, selection SessionRuntimeSelection, interactivePolicy InteractiveToolPolicy, scheduledAudioDispatch ScheduledAudioDispatchPolicy, capabilityCoordinator SessionCapabilityCoordinator, recordingClaim *sessionRecordingClaim) (sessionRuntimePlan, error) {
	if err := configureSessionRuntimeLoop(&plan, opts, interactivePolicy, scheduledAudioDispatch); err != nil {
		return sessionRuntimePlan{}, err
	}
	if err := configureSessionRuntimeAudio(ctx, &plan, opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	configureSessionRuntimeDeviceObservers(ctx, &plan, opts.Observability)
	plan.selection = selection
	plan.transport = selection.Transport
	plan.signalingEndpoint = selection.SignalingEndpoint
	plan.mediaSource = selection.MediaSource
	if plan.rtcRuntime == nil && selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	plan.capabilityCoordinator = capabilityCoordinator
	return wireSessionRecordingClaim(plan, recordingClaim), nil
}

func newSessionRTCRuntimeForPlan(opts SessionRunOptions, selection SessionRuntimeSelection, factory sessionRuntimeFactory) (SessionRTCRuntime, error) {
	runtimeFactory := opts.RTCRuntimeFactory
	if runtimeFactory == nil {
		runtimeFactory = factory.newRTCRuntime
	}
	if runtimeFactory == nil {
		return nil, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	runtime, err := runtimeFactory(selection)
	if err != nil {
		return nil, wrapSessionRTCRuntimeError("create runtime", err)
	}
	if runtime == nil {
		return nil, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	return runtime, nil
}

func configureSessionRuntimeLoop(plan *sessionRuntimePlan, opts SessionRunOptions, interactivePolicy InteractiveToolPolicy, scheduledAudioDispatch ScheduledAudioDispatchPolicy) error {
	plan.diagnostics = opts.Diagnostics
	plan.metricsRecorder = opts.MetricsRecorder
	plan.streamObserver = opts.StreamObserver
	// A mode planner can restore scheduled inputs from a recording. Preserve
	// those values and use caller inputs only when the planner left them unset.
	if plan.audioInputs == nil {
		plan.audioInputs = opts.AudioInputs
	}
	plan.scheduledAudioDispatch = scheduledAudioDispatch
	plan.filesystemPolicy = opts.FilesystemPolicy
	plan.voice = opts.Voice
	plan.clockSource = platformclock.Ensure(opts.Clock)
	plan.runtime = sessiontracewire.NewRuntimeRecorder(opts.RuntimeObserver, plan.clockSource)
	plan.loop.runtime = plan.runtime
	plan.loop.audioService = opts.AudioService
	plan.loop.clockSource = plan.clockSource
	plan.loop.livenessClock = opts.LivenessClock
	if plan.loop.livenessClock == nil {
		plan.loop.livenessClock = sessiontracewire.LivenessClockFromSource(plan.clockSource)
	}
	plan.loop.BareLive = plan.loop.BareLive || opts.BareLive
	plan.loop.cancellationIntent = opts.CancellationIntent
	plan.loop.toolDiagnostics = opts.ToolDiagnostics
	plan.loop.SessionUpdatedTimeout = opts.SessionUpdatedTimeout
	plan.loop.AudioInterruptions = opts.AudioInterruptions
	plan.rtcDeviceRequest = opts.RTCBinding
	plan.deviceService = opts.DeviceService
	// Keep live device playback at the same voice gain as recorded and room
	// output, and prevent replayed media from becoming a live feedback path.
	plan.rtcDeviceRequest.OutputVoice = opts.Voice
	plan.rtcDeviceRequest.BypassSelfHearing = plan.rtcDeviceRequest.BypassSelfHearing || opts.ReplayPath != ""
	// Clone the per-session image binding so concurrent capability snapshots
	// cannot leak across plans.
	plan.loop.ToolExecutor = bindSessionImageToolExecutor(opts, *plan)
	plan.loop.ToolDefinitions = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	policySnapshot := interactivePolicy.Clone()
	plan.interactivePolicy = &policySnapshot
	plan.loop.InteractiveToolPolicy = &policySnapshot
	// Zero keeps production on the class-specific policy timeout; nonzero is
	// the hermetic per-invocation adapter seam.
	plan.loop.ToolDefinitionBase = append([]messages.ToolDefinition(nil), opts.ToolDefinitionBase...)
	plan.loop.RefreshToolDefinitions = opts.RefreshToolDefinitions
	plan.loop.BrowserWatch = opts.BrowserWatch
	plan.loop.ToolExecutionTimeout = opts.ToolExecutionTimeout
	plan.loop.ScheduledAudioDispatch = scheduledAudioDispatch
	return nil
}

func configureSessionRuntimeAudio(ctx context.Context, plan *sessionRuntimePlan, opts SessionRunOptions) error {
	rates, err := resolveSessionRuntimeAudioRates(ctx, plan, opts)
	if err != nil {
		return err
	}
	plan.outputAudioSampleRate = rates.OutputRate
	plan.inputAudioSampleRate = rates.InputRate
	if configurer, ok := plan.inferencer.(runtimeAudioOutputConfigurer); ok {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rates.OutputRate))
	}
	if configurer, ok := plan.inferencer.(runtimeAudioInputConfigurer); ok {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rates.InputRate))
	}
	convertedInputs, err := opts.AudioService.ConvertScheduledInputs(ctx, sessionRuntimeAudioInputs(plan.audioInputs), rates.InputRate)
	if err != nil {
		return err
	}
	plan.audioInputs = sessionTraceAudioInputs(convertedInputs)
	plan.loop.InputAudioSampleRate = rates.InputRate
	if plan.rtcDeviceRequest.HasOutput() && rates.OutputRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = rates.OutputRate
	}
	if plan.rtcDeviceRequest.HasInput() && rates.InputRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = rates.InputRate
	}
	return nil
}

func resolveSessionRuntimeAudioRates(ctx context.Context, plan *sessionRuntimePlan, opts SessionRunOptions) (audioio.RateResolution, error) {
	if opts.AudioService == nil {
		return audioio.RateResolution{}, errors.New("audio service is required for session rate resolution")
	}
	inputRate, outputRate := plan.inputAudioSampleRate, plan.outputAudioSampleRate
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		request := requested.Request().Config
		if inputRate <= 0 {
			inputRate = int(request.InputAudioSampleRate)
		}
		if outputRate <= 0 {
			outputRate = int(request.OutputAudioSampleRate)
		}
	}
	return opts.AudioService.ResolveRates(ctx, audioio.RateRequest{
		Provider: plan.provider, Replay: opts.ReplayPath != "",
		CapturedInputRate: inputRate, CapturedOutputRate: outputRate,
	})
}

func sessionRuntimeAudioInputs(inputs []sessiontrace.ScheduledAudioInput) []audioio.ScheduledAudioInput {
	converted := make([]audioio.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		converted[index] = audioio.ScheduledAudioInput(input)
	}
	return converted
}

func sessionTraceAudioInputs(inputs []audioio.ScheduledAudioInput) []sessiontrace.ScheduledAudioInput {
	converted := make([]sessiontrace.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		converted[index] = sessiontrace.ScheduledAudioInput(input)
	}
	return converted
}

func startLiveSessionUpdatedTimer(opts sessionLoopOptions, current platformclock.Timer) (platformclock.Timer, <-chan time.Time, error) {
	if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.ScheduledAudioAwaitingConfiguration() || current != nil {
		if current == nil {
			return nil, nil, nil
		}
		return current, current.C(), nil
	}
	timeout := opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	timer, err := opts.audioService.NewTimer(opts.clockSource, timeout)
	if err != nil {
		return nil, nil, err
	}
	return timer, timer.C(), nil
}

func stopLiveSessionUpdatedTimer(timer *platformclock.Timer, timeout *<-chan time.Time) {
	if timer == nil || *timer == nil {
		return
	}
	(*timer).Stop()
	*timer = nil
	*timeout = nil
}

func configureSessionRuntimeDeviceObservers(ctx context.Context, plan *sessionRuntimePlan, dependencies observability.Dependencies) {
	playback := sessiontracewire.NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{
		Sink: plan.diagnostics, MetricSampler: dependencies.MetricSampler, Logger: dependencies.Logger, Runtime: plan.runtime,
	})
	tracePlayback := playback.PlaybackObserver(nil)
	priorPlayback := plan.rtcDeviceRequest.PlaybackObserver
	plan.rtcDeviceRequest.PlaybackObserver = func(id string, stats audio.PlaybackQueueStats) {
		if priorPlayback != nil {
			priorPlayback(id, stats)
		}
		if tracePlayback != nil {
			tracePlayback(devicegw.DeviceID(id), stats)
		}
	}
	traceReceipt := playback.PlaybackReceiptObserver(nil)
	priorReceipt := plan.rtcDeviceRequest.PlaybackReceiptObserver
	plan.rtcDeviceRequest.PlaybackReceiptObserver = func(receipt audio.PlaybackReceipt) {
		if priorReceipt != nil {
			priorReceipt(receipt)
		}
		if traceReceipt != nil {
			traceReceipt(receipt)
		}
	}
	traceCapture := playback.CaptureObserver(nil)
	priorCapture := plan.rtcDeviceRequest.CaptureObserver
	plan.rtcDeviceRequest.CaptureObserver = func(id string, stats audio.CaptureQueueStats) {
		if priorCapture != nil {
			priorCapture(id, stats)
		}
		if traceCapture != nil {
			traceCapture(devicegw.DeviceID(id), stats)
		}
	}
}

// prepareSessionStreamOutput gives an unowned stream its terminal renderer.
// The returned finalizer preserves transcript errors before publishing status.
func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := wire.NewReporter()
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
