package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
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

const injectedSessionDefaultMaxDuration = 3 * time.Second

func planSessionRuntimeContext(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithContext(ctx, opts)
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
	var err error
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	opts, err = resolveSessionRuntimeFilesystem(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	var capabilityCoordinator SessionCapabilityCoordinator
	opts, capabilityCoordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
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
	return configureSessionRuntimePlan(ctx, plan, opts, selection, interactivePolicy, scheduledAudioDispatchPolicyForOptions(opts), capabilityCoordinator)
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

func configureSessionRuntimePlan(ctx context.Context, plan sessionRuntimePlan, opts SessionRunOptions, selection SessionRuntimeSelection, interactivePolicy InteractiveToolPolicy, scheduledAudioDispatch ScheduledAudioDispatchPolicy, capabilityCoordinator SessionCapabilityCoordinator) (sessionRuntimePlan, error) {
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
	plan.recordingService = opts.RecordingService
	if opts.RecordDirectory != "" {
		evidenceOptions := sessionLiveEvidenceOptions(opts, plan, opts.RecordDirectory, opts.RecordMaxDuration)
		plan.liveEvidenceOptions = &evidenceOptions
	}
	return plan, nil
}

func (s *observedSession) observeRuntimeSend(msg messages.StreamMessage) {
	if s.runtime == nil {
		return
	}
	//nolint:exhaustive // Only media, commit, and response-create sends affect this trace.
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil {
			s.runtime.ProviderAudioSent(value.Content)
		}
	case messages.StreamTypeMessageEnd:
		s.runtime.InputCommit()
		s.runtime.ResponseCreate(msg)
	case messages.StreamTypeResponseCreate:
		s.runtime.ResponseCreate(msg)
	default:
		// Only media, commit, and response-create sends affect this trace.
	}
}

func (s *observedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func sessionLiveEvidenceOptions(opts SessionRunOptions, plan sessionRuntimePlan, destination string, maxDuration time.Duration) runtimerecording.LiveEvidenceOptions {
	model := strings.TrimSpace(plan.model)
	if model == "" {
		model = strings.TrimSpace(opts.Model)
	}
	return runtimerecording.LiveEvidenceOptions{
		Destination: destination, Provider: plan.provider, Model: model, OutputAudioRate: plan.outputAudioSampleRate,
		ClockBase: plan.clockSource.Now(), WallClockStart: time.Now().UTC(),
		Credentials:                   sessionRecordingCredentials(opts, plan),
		ProviderCaptureRequired:       plan.mode == sessionRuntimeModeRecordOpenAI || plan.mode == sessionRuntimeModeRecordGrok,
		ProviderCapturePath:           opts.RecordPath,
		DisableProviderCaptureSidecar: strings.TrimSpace(opts.RecordPath) != "" && maxDuration > 0,
	}
}

func sessionRecordingCredentials(opts SessionRunOptions, plan sessionRuntimePlan) []string {
	credentials := appendRecordingCredential(nil, opts.APIKey)
	if opts.LoadedConfig == nil {
		return credentials
	}
	switch strings.ToLower(strings.TrimSpace(plan.provider)) {
	case sessionProviderOpenAI:
		if opts.LoadedConfig.Model.OpenAI != nil {
			credentials = appendRecordingCredential(credentials, opts.LoadedConfig.Model.OpenAI.APIKey)
		}
	case sessionProviderGrok:
		if opts.LoadedConfig.Model.Grok != nil {
			credentials = appendRecordingCredential(credentials, opts.LoadedConfig.Model.Grok.APIKey)
		}
	}
	return credentials
}

func appendRecordingCredential(credentials []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return credentials
	}
	for _, existing := range credentials {
		if existing == value {
			return credentials
		}
	}
	return append(credentials, value)
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
