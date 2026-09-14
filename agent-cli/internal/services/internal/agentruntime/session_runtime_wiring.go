package agentruntime

import (
	"context"
	"fmt"
	"io"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sessionPlanningState struct {
	opts        SessionRunOptions
	coordinator SessionCapabilityCoordinator
}

func planSessionRuntimeWithFactoryContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	state, err := prepareSessionPlanningState(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	defer func() {
		if planErr != nil {
			closeSessionCapabilityIfNeeded(state.coordinator, &planErr)
		}
	}()
	interactivePolicy, err := resolveSessionInteractiveToolPolicy(state.opts, state.opts.ToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(state.opts.AudioInTurnBarge, len(state.opts.AudioInputs)); err != nil {
		return sessionRuntimePlan{}, err
	}
	scheduledAudioDispatch := scheduledAudioDispatchPolicyForOptions(state.opts)
	if state.opts.ReplayPath == "" {
		state.opts.Provider = effectiveSessionProvider(state.opts)
	}
	selection, err := resolveSessionRuntimeSelection(state.opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan, err = planSelectedSessionRuntime(ctx, state.opts, selection, factory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return completeSessionRuntimePlan(ctx, state.opts, plan, interactivePolicy, scheduledAudioDispatch, selection, state.coordinator)
}

func prepareSessionPlanningState(opts SessionRunOptions) (sessionPlanningState, error) {
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	if opts.RecordPath != "" && opts.recordingService == nil {
		opts.recordingService = recordingwire.NewService(platformclock.Ensure(opts.Clock))
	}
	filesystemPolicy := opts.FilesystemPolicy
	if filesystemPolicy == nil {
		var err error
		filesystemPolicy, err = tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
		if err != nil {
			return sessionPlanningState{}, fmt.Errorf("resolve filesystem scope: %w", err)
		}
	}
	opts.FilesystemPolicy = filesystemPolicy
	opts.WorkDir = filesystemPolicy.PrimaryRoot()
	opts.AllowPaths = filesystemPolicy.AdditionalRoots()
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	return sessionPlanningState{opts: opts, coordinator: coordinator}, nil
}

func planSelectedSessionRuntime(ctx context.Context, opts SessionRunOptions, selection SessionRuntimeSelection, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return planWebRTCSessionRuntime(opts, selection, factory)
	}
	return planSessionRuntimeModeContext(ctx, opts, factory)
}

func completeSessionRuntimePlan(ctx context.Context, opts SessionRunOptions, plan sessionRuntimePlan, interactivePolicy InteractiveToolPolicy, scheduledAudioDispatch ScheduledAudioDispatchPolicy, selection SessionRuntimeSelection, coordinator SessionCapabilityCoordinator) (sessionRuntimePlan, error) {
	plan = applySessionRuntimeLifecycle(opts, plan, scheduledAudioDispatch)
	if err := configureSessionAudioContract(opts, &plan); err != nil {
		return sessionRuntimePlan{}, err
	}
	var err error
	plan.audioInputs, err = convertScheduledAudioInputs(plan.audioInputs, plan.inputAudioSampleRate)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	applySessionRuntimeDevices(ctx, opts, &plan, interactivePolicy)
	applySessionRuntimeSelection(selection, &plan)
	if plan.rtcRuntime == nil && selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	plan.capabilityCoordinator = coordinator
	return claimSessionRuntimeCapture(opts, plan)
}

func applySessionRuntimeLifecycle(opts SessionRunOptions, plan sessionRuntimePlan, scheduledAudioDispatch ScheduledAudioDispatchPolicy) sessionRuntimePlan {
	plan.diagnostics = opts.Diagnostics
	plan.metricsRecorder = opts.MetricsRecorder
	plan.streamObserver = opts.StreamObserver
	if plan.audioInputs == nil {
		plan.audioInputs = opts.AudioInputs
	}
	plan.scheduledAudioDispatch = scheduledAudioDispatch
	plan.filesystemPolicy = opts.FilesystemPolicy
	plan.clockSource = platformclock.Ensure(opts.Clock)
	plan.runtime = newSessionRuntimeObservationRecorder(opts.RuntimeObserver, plan.clockSource)
	plan.loop.runtime = plan.runtime
	plan.loop.clockSource = plan.clockSource
	plan.loop.livenessClock = opts.LivenessClock
	if plan.loop.livenessClock == nil {
		plan.loop.livenessClock = sessionLivenessClockFromSource(plan.clockSource)
	}
	plan.loop.BareLive = plan.loop.BareLive || opts.BareLive
	plan.loop.cancellationIntent = opts.CancellationIntent
	plan.loop.toolDiagnostics = opts.ToolDiagnostics
	plan.loop.SessionUpdatedTimeout = opts.SessionUpdatedTimeout
	plan.loop.AudioInterruptions = opts.AudioInterruptions
	return plan
}

func applySessionRuntimeDevices(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan, interactivePolicy InteractiveToolPolicy) {
	plan.rtcDeviceRequest = opts.RTCDeviceBinding
	plan.rtcDeviceRequest.OutputVoice = opts.Voice
	plan.rtcDeviceRequest.BypassSelfHearing = plan.rtcDeviceRequest.BypassSelfHearing || opts.ReplayPath != ""
	plan.loop.ToolExecutor = bindSessionImageToolExecutor(opts, *plan)
	plan.loop.ToolDefinitions = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	policySnapshot := interactivePolicy.Clone()
	plan.interactivePolicy = &policySnapshot
	plan.loop.InteractiveToolPolicy = &policySnapshot
	plan.loop.ToolDefinitionBase = append([]messages.ToolDefinition(nil), opts.ToolDefinitionBase...)
	plan.loop.RefreshToolDefinitions = opts.RefreshToolDefinitions
	plan.loop.BrowserWatch = opts.BrowserWatch
	plan.loop.ToolExecutionTimeout = opts.ToolExecutionTimeout
	plan.loop.ScheduledAudioDispatch = plan.scheduledAudioDispatch
	plan.loop.InputAudioSampleRate = plan.inputAudioSampleRate
	if plan.rtcDeviceRequest.outputSelected() && plan.outputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = plan.outputAudioSampleRate
	}
	if plan.rtcDeviceRequest.inputSelected() && plan.inputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = plan.inputAudioSampleRate
	}
	applySessionRuntimeObservability(ctx, opts, plan)
}

func applySessionRuntimeObservability(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) {
	observabilityDependencies := opts.Observability
	if observabilityDependencies.MetricSampler == nil && observabilityDependencies.Logger == nil {
		observabilityDependencies = plan.rtcDeviceRequest.Observability
	}
	plan.rtcDeviceRequest.PlaybackObserver = combineRTCDevicePlaybackObservers(
		plan.rtcDeviceRequest.PlaybackObserver,
		sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(plan.diagnostics)),
		sessionPlaybackObservabilityObserverContext(ctx, observabilityDependencies.MetricSampler, observabilityDependencies.Logger),
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
		sessionCaptureObservabilityObserverContext(ctx, observabilityDependencies.MetricSampler, observabilityDependencies.Logger),
	)
}

func applySessionRuntimeSelection(selection SessionRuntimeSelection, plan *sessionRuntimePlan) {
	plan.selection = selection
	plan.transport = selection.Transport
	plan.signalingEndpoint = selection.SignalingEndpoint
	plan.mediaSource = selection.MediaSource
}

func claimSessionRuntimeCapture(opts SessionRunOptions, plan sessionRuntimePlan) (sessionRuntimePlan, error) {
	if opts.RecordPath == "" || opts.recordingService == nil {
		return plan, nil
	}
	claim, err := opts.recordingService.Claim(runtimerecording.ClaimOptions{Destination: opts.RecordPath, Kind: runtimerecording.ClaimKindCapture})
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return wireSessionRecordingClaim(plan, claim), nil
}

// wireSessionRecordingClaim redirects one recording plan's capture flush
// through its destination claim. It is kept separate from planning because an
// injected session can add its fixture recorder after the generic runtime plan
// has been built.
func wireSessionRecordingClaim(plan sessionRuntimePlan, claim runtimerecording.DestinationClaim) sessionRuntimePlan {
	if claim == nil {
		return plan
	}
	plan.captureClaim = claim
	if plan.captureClaimWired || plan.flushCapture == nil {
		return plan
	}
	flushTo := plan.flushCaptureTo
	published := false
	plan.flushCapture = func() error {
		if flushTo == nil {
			return fmt.Errorf("recording plan does not support private capture publication")
		}
		err := claim.Publish(flushTo)
		if err == nil {
			published = true
		}
		return err
	}
	originalFinalize := plan.finalize
	plan.finalize = func(ctx context.Context, out io.Writer) error {
		if !published || originalFinalize == nil {
			return nil
		}
		return originalFinalize(ctx, out)
	}
	plan.captureClaimWired = true
	return plan
}
