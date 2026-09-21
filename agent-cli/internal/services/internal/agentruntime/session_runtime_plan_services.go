package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func configureSessionRuntimePlan(plan sessionRuntimePlan, opts SessionRunOptions, selection SessionRuntimeSelection, interactivePolicy InteractiveToolPolicy, scheduledAudioDispatch ScheduledAudioDispatchPolicy, capabilityCoordinator SessionCapabilityCoordinator, recordingClaim *sessionRecordingClaim) (sessionRuntimePlan, error) {
	if err := configureSessionRuntimeLoop(&plan, opts, interactivePolicy, scheduledAudioDispatch); err != nil {
		return sessionRuntimePlan{}, err
	}
	if err := configureSessionRuntimeAudio(&plan, opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	configureSessionRuntimeDeviceObservers(&plan, opts.Observability)
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
	plan.runtime = newSessionRuntimeObservationRecorder(opts.RuntimeObserver, plan.clockSource)
	plan.loop.runtime = plan.runtime
	plan.loop.audioService = opts.AudioService
	plan.loop.clockSource = plan.clockSource
	plan.loop.livenessClock = opts.LivenessClock
	if plan.loop.livenessClock == nil {
		if opts.AudioService == nil {
			return errors.New("audio service is required for session liveness timing")
		}
		var err error
		plan.loop.livenessClock, err = opts.AudioService.NewClock(plan.clockSource)
		if err != nil {
			return err
		}
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

func configureSessionRuntimeAudio(plan *sessionRuntimePlan, opts SessionRunOptions) error {
	if opts.AudioService == nil {
		return errors.New("audio service is required for session rate resolution")
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
	rates, err := opts.AudioService.ResolveRates(context.Background(), audioio.RateRequest{
		Provider: plan.provider, Replay: opts.ReplayPath != "",
		CapturedInputRate: inputRate, CapturedOutputRate: outputRate,
	})
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
	plan.audioInputs, err = opts.AudioService.ConvertScheduledInputs(context.Background(), plan.audioInputs, rates.InputRate)
	if err != nil {
		return err
	}
	plan.loop.InputAudioSampleRate = rates.InputRate
	if plan.rtcDeviceRequest.HasOutput() && rates.OutputRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = rates.OutputRate
	}
	if plan.rtcDeviceRequest.HasInput() && rates.InputRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = rates.InputRate
	}
	return nil
}

func configureSessionRuntimeDeviceObservers(plan *sessionRuntimePlan, dependencies observability.Dependencies) {
	// Resolve the diagnostic sink so device overflow remains visible even when
	// the caller omitted SessionRunOptions.Diagnostics.
	plan.rtcDeviceRequest.PlaybackObserver = combineRTCDevicePlaybackObservers(
		plan.rtcDeviceRequest.PlaybackObserver,
		sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(plan.diagnostics)),
		sessionPlaybackObservabilityObserver(dependencies.MetricSampler, dependencies.Logger),
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
		sessionCaptureObservabilityObserver(dependencies.MetricSampler, dependencies.Logger),
	)
}

// prepareSessionStreamOutput gives an unowned stream its terminal renderer.
// The returned finalizer preserves transcript errors before publishing status.
func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := newSessionTerminalReporter()
	opts.terminalReporter = reporter
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.finishTranscript())
		return errors.Join(runErr, reporter.publish(out, runErr))
	}
}

func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setLivenessClock(opts.livenessClock)
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
