// This file owns the shared session-runtime modes, factories, plan state, generic planning and dispatch, execution, and cross-provider error handling.
package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type sessionRuntimeMode string

const (
	defaultSessionAudioDevice                          = "default"
	sessionRuntimeModeBareLive      sessionRuntimeMode = "bare-live"
	sessionRuntimeModeInjectedLive  sessionRuntimeMode = "injected-live"
	sessionRuntimeModeReplayGeneric sessionRuntimeMode = "replay-generic"
	sessionRuntimeModeReplayGrok    sessionRuntimeMode = "replay-grok-websocket"
	sessionRuntimeModeReplayOpenAI  sessionRuntimeMode = "replay-openai-websocket"
	sessionRuntimeModeRecordGrok    sessionRuntimeMode = "record-grok"
	sessionRuntimeModeRecordOpenAI  sessionRuntimeMode = "record-openai"
)

type runtimeAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}

type runtimeAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}

type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

type sessionRuntimeFactory struct {
	newDefaultLiveDialer               func() transport.Dialer
	newGrokSessionInferencer           func(config.GrokConfig, transport.Dialer) (messages.SessionInferencer, error)
	newOpenAISessionInf                func(config.OpenAIConfig, string, transport.Dialer, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newBareLiveSessionInferencer       func(SessionRunOptions) (messages.SessionInferencer, string, error)
	newGrokSessionWithTools            func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error)
	newOpenAISessionWithTools          func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newOpenAIScheduledSessionWithTools func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newRTCRuntime                      SessionRTCRuntimeFactory
}

// SessionRuntimeFactory is the process-scoped provider construction owner.
// Wire creates one instance and every session/room planner receives that
// instance, keeping provider construction out of request dispatch.
type SessionRuntimeFactory = sessionRuntimeFactory

func NewSessionRuntimeFactory() SessionRuntimeFactory { return newDefaultSessionRuntimeFactory() }

func (f sessionRuntimeFactory) configured() bool {
	return f.newDefaultLiveDialer != nil || f.newBareLiveSessionInferencer != nil || f.newRTCRuntime != nil
}
func newDefaultSessionRuntimeFactory() sessionRuntimeFactory {
	return sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			return grok.NewDefaultWebSocketDialer()
		},
		newGrokSessionInferencer: func(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
			return buildGrokSessionInferencer(sessionCfg, dialer)
		},
		newOpenAISessionInf: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(sessionCfg, voice, dialer, inputAudioTranscription)
		},
		newBareLiveSessionInferencer: func(opts SessionRunOptions) (messages.SessionInferencer, string, error) {
			return NewLiveSessionInferencer(opts, "")
		},
		newGrokSessionWithTools: func(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
			return buildGrokSessionInferencerWithTools(sessionCfg, dialer, toolDefinitions)
		},
		newOpenAISessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
		},
		newOpenAIScheduledSessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithScheduledAudioAndInputAudioTranscription(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
		},
	}
}

func (f sessionRuntimeFactory) newGrokSessionInferencerForTools(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if f.newGrokSessionWithTools != nil {
		return f.newGrokSessionWithTools(sessionCfg, dialer, toolDefinitions)
	}
	return f.newGrokSessionInferencer(sessionCfg, dialer)
}

func (f sessionRuntimeFactory) newOpenAISessionInferencerForTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, scheduledAudio bool, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	if scheduledAudio && f.newOpenAIScheduledSessionWithTools != nil {
		return f.newOpenAIScheduledSessionWithTools(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
	}
	if f.newOpenAISessionWithTools != nil {
		return f.newOpenAISessionWithTools(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
	}
	return f.newOpenAISessionInf(sessionCfg, voice, dialer, inputAudioTranscription)
}

type sessionRuntimePlan struct {
	mode                   sessionRuntimeMode
	provider               string
	model                  string
	voice                  string
	inputAudioSampleRate   int
	outputAudioSampleRate  int
	capturePath            string
	loopOut                io.Writer
	inferencer             messages.SessionInferencer
	loop                   sessionLoopOptions
	announceTools          []messages.ToolDefinition
	announce               string
	replayIntegrityWarning string
	flushCapture           func() error
	flushCaptureTo         func(string) error
	finalize               func(context.Context, io.Writer) error
	replayCompletion       func(*sessionTerminalReporter)
	diagnostics            SessionDiagnosticSink
	metricsRecorder        metrics.Recorder
	streamObserver         SessionStreamObserver
	audioInputs            []ScheduledAudioInput
	scheduledAudioDispatch ScheduledAudioDispatchPolicy
	clockSource            platformclock.Source
	runtime                *sessionRuntimeObservationRecorder
	rtcRuntime             SessionRTCRuntime
	closeSession           func() error
	selection              SessionRuntimeSelection
	transport              string
	signalingEndpoint      string
	mediaSource            string
	rtcDeviceRequest       runtimedevices.RTCBindingRequest
	deviceService          runtimedevices.Service
	rtcBinding             runtimedevices.RTCBinding
	capabilityCoordinator  SessionCapabilityCoordinator
	recordingService       runtimerecording.Service
	liveEvidenceOptions    *runtimerecording.LiveEvidenceOptions
	liveEvidence           runtimerecording.LiveEvidence
	recordingSession       runtimerecording.SessionCapture
	recordingSetup         func(*sessionRuntimePlan) error
	browserRecording       *sessionBrowserRecording
	interactivePolicy      *InteractiveToolPolicy
	filesystemPolicy       *tools.FilesystemPolicy
}

func (p sessionRuntimePlan) bareLiveOutput() (string, string) {
	return p.liveOutput("Starting bare live session: ")
}

func (p sessionRuntimePlan) browserLiveOutput() (string, string) {
	return p.liveOutput("Starting WebMCP browser live session: ")
}

func (p sessionRuntimePlan) liveOutput(prefix string) (string, string) {
	transport := p.transport
	if transport == "" {
		transport = SessionTransportWebSocket
	}
	inputDevice, outputDevice := "unavailable", "unavailable"
	if p.rtcDeviceRequest.HasInput() {
		inputDevice = p.rtcDeviceRequest.InputDevice
		if inputDevice == "" {
			inputDevice = defaultSessionAudioDevice
		}
	}
	if p.rtcDeviceRequest.HasOutput() {
		outputDevice = p.rtcDeviceRequest.OutputDevice
		if outputDevice == "" {
			outputDevice = defaultSessionAudioDevice
		}
	}
	identity := fmt.Sprintf("provider=%s model=%s transport=%s input-device=%s output-device=%s", p.provider, p.model, transport, inputDevice, outputDevice)
	return prefix + identity, "Listening: " + identity
}

func (p sessionRuntimePlan) run(ctx context.Context, out io.Writer) (runErr error) {
	return p.withLiveEvidence(ctx, func(runCtx context.Context, prepared sessionRuntimePlan) error {
		return prepared.runPrepared(runCtx, out)
	})
}

func (p sessionRuntimePlan) withLiveEvidence(ctx context.Context, run func(context.Context, sessionRuntimePlan) error) error {
	if p.liveEvidenceOptions != nil && p.liveEvidence == nil {
		if p.recordingService == nil {
			return errors.New("recording service is not configured")
		}
		return p.recordingService.RunLiveEvidence(ctx, *p.liveEvidenceOptions, func(runCtx context.Context, evidence runtimerecording.LiveEvidence) error {
			p.liveEvidence = evidence
			return p.withLiveEvidence(runCtx, run)
		})
	}
	if p.recordingSetup != nil {
		if err := p.recordingSetup(&p); err != nil {
			return err
		}
		p.recordingSetup = nil
		if p.recordingSession != nil {
			p.inferencer = p.recordingSession
		}
	}
	return run(ctx, p)
}

func (p sessionRuntimePlan) finishBrowserRecording(ctx context.Context) error {
	if p.browserRecording == nil {
		return nil
	}
	p.browserRecording.stop()
	artifact, err := p.browserRecording.artifact()
	if err != nil || artifact == nil || p.liveEvidence == nil {
		return err
	}
	return p.liveEvidence.RecordBrowserArtifact(context.WithoutCancel(ctx), artifact)
}

func (p sessionRuntimePlan) runPrepared(ctx context.Context, out io.Writer) (runErr error) {
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		p.loop.terminalReporter = reporter
	}
	finalizer := durationwire.NewService().NewFinalizer(p.finalizationPorts())
	if p.browserRecording != nil {
		p.browserRecording.start(ctx)
		defer func() {
			runErr = errors.Join(runErr, p.finishBrowserRecording(ctx))
		}()
	}
	defer func() {
		runErr = finalizer.Finish(ctx, out, runErr)
		if !sessionErrorHasIndependentFailure(runErr) && p.replayCompletion != nil {
			p.replayCompletion(reporter)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}()
	if p.replayIntegrityWarning != "" {
		if _, err := fmt.Fprintln(out, p.replayIntegrityWarning); err != nil {
			return err
		}
	}
	if err := p.bindRTC(ctx, finalizer); err != nil {
		return err
	}
	if err := p.writeAnnouncements(out, true); err != nil {
		return err
	}
	loopOut := out
	if p.loopOut != nil {
		loopOut = p.loopOut
	}
	loop := p.loop
	p.configureLoopObserver(&loop)
	if p.inferencer != nil {
		reporter.markRunStarted()
		if err := runAgentLoopSession(ctx, loopOut, p.inferencer, loop); err != nil {
			return wrapSessionRuntimeError(p, wrapSessionPhaseError("run session loop", err))
		}
	}
	return nil
}

func writeFilesystemScopeAnnouncement(out io.Writer, policy *tools.FilesystemPolicy) {
	if policy == nil {
		return
	}
	_, _ = fmt.Fprintln(out, "Filesystem scope: "+policy.ScopeDescription())
	_, _ = fmt.Fprintln(out, tools.FilesystemScopeStartupNotice)
}

// writeSessionToolAnnouncement makes the exact provider-advertised surface
// visible at startup. Canonical definitions are already stable-sorted by name,
// but canonicalize again to keep direct service callers deterministic.
func writeSessionToolAnnouncement(out io.Writer, definitions []messages.ToolDefinition) {
	definitions = messages.CanonicalToolDefinitions(definitions)
	names := make([]string, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if name := strings.TrimSpace(definition.Name); name != "" {
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		_, _ = fmt.Fprintln(out, "Tools: none")
		return
	}
	_, _ = fmt.Fprintln(out, "Tools: "+strings.Join(names, ", "))
}

// configureLoopObserver installs the shared stream observer for every session
// runner mode, including the duration-bounded path which executes plan.loop
// directly instead of calling plan.run.
func (p sessionRuntimePlan) configureLoopObserver(loop *sessionLoopOptions) {
	if loop == nil {
		return
	}
	obs := newSessionProgressObserver(p.diagnostics, p.metricsRecorder, p.provider, p.model)
	obs.liveRecorder = p.liveEvidence
	obs.streamObserver = p.streamObserver
	obs.runtime = p.runtime
	obs.setLivenessClock(loop.livenessClock)
	obs.cancellationIntent = loop.cancellationIntent
	obs.requireSessionUpdated = loop.RequireSessionUpdated
	obs.scheduledAudioDispatch = loop.ScheduledAudioDispatch
	obs.scheduleAudioInputs(p.audioInputs)
	loop.observer = obs
}

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeContext(context.Background(), opts)
}

func planSessionRuntimeContext(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	factory := opts.runtimeFactory
	if !factory.configured() {
		// Kept for package-local test callers while composition migrates. All
		// production service entrypoints install runtimeFactory from Wire.
		factory = newDefaultSessionRuntimeFactory()
	}
	opts.runtimeFactory = factory
	return planSessionRuntimeWithFactoryContext(ctx, opts, factory)
}

//lint:ignore U1000 package tests exercise the context-free planning seam.
func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithFactoryContext(context.Background(), opts, factory)
}

func planSessionRuntimeWithFactoryContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	if (opts.RecordPath != "" || opts.RecordDirectory != "") && opts.recordingService == nil {
		return sessionRuntimePlan{}, errors.New("recording service is not configured")
	}
	if (opts.RecordPath != "" || opts.RecordDirectory != "") && opts.providerCaptureService == nil {
		return sessionRuntimePlan{}, errors.New("provider capture service is not configured")
	}
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	filesystemPolicy := opts.FilesystemPolicy
	if filesystemPolicy == nil {
		var err error
		filesystemPolicy, err = tools.ResolveFilesystemPolicy(opts.WorkDir, opts.AllowPaths...)
		if err != nil {
			return sessionRuntimePlan{}, fmt.Errorf("resolve filesystem scope: %w", err)
		}
	}
	opts.FilesystemPolicy = filesystemPolicy
	opts.WorkDir = filesystemPolicy.PrimaryRoot()
	opts.AllowPaths = filesystemPolicy.AdditionalRoots()
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
	scheduledAudioDispatch := scheduledAudioDispatchPolicyForOptions(opts)

	// Resolve the provider once at the session boundary so every live mode
	// (bare, browser-enabled, recorded, injected, and RTC) consumes the same
	// realtime-capable policy. Replay keeps its capture-owned provider identity.
	if opts.ReplayPath == "" {
		opts.Provider = effectiveSessionProvider(opts)
	}

	selection, err := resolveSessionRuntimeSelection(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		plan, err = planWebRTCSessionRuntime(opts, selection, factory)
	} else {
		plan, err = planSessionRuntimeModeContext(ctx, opts, factory)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.diagnostics = opts.Diagnostics
	plan.metricsRecorder = opts.MetricsRecorder
	plan.streamObserver = opts.StreamObserver
	// A mode planner (for example a self-driving bare replay of a recorded
	// scheduled-audio-turn capture) may have already populated audioInputs
	// directly from the capture; opts.AudioInputs only fills that in when the
	// planner left it unset.
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
			return sessionRuntimePlan{}, errors.New("audio service is required for session liveness timing")
		}
		plan.loop.livenessClock, err = opts.AudioService.NewClock(plan.clockSource)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
	}
	plan.loop.BareLive = plan.loop.BareLive || opts.BareLive
	plan.loop.cancellationIntent = opts.CancellationIntent
	plan.loop.toolDiagnostics = opts.ToolDiagnostics
	plan.loop.SessionUpdatedTimeout = opts.SessionUpdatedTimeout
	plan.loop.AudioInterruptions = opts.AudioInterruptions
	plan.rtcDeviceRequest = opts.RTCBinding
	plan.deviceService = opts.DeviceService
	// Local device playback of this session's own synthesized voice must
	// carry the same fixed per-voice loudness correction as every other
	// output path, so --voice selection does not
	// leave a live interactive session sounding louder or quieter than a
	// recorded/room session using the same voice.
	plan.rtcDeviceRequest.OutputVoice = opts.Voice
	// Replay preserves the selected device pumps for lifecycle/round-trip
	// callers, but its recorded media is not a live acoustic topology. Keep the
	// explicit bypass at the binding boundary so replayed provider output cannot
	// be mistaken for speaker-to-microphone feedback.
	plan.rtcDeviceRequest.BypassSelfHearing = plan.rtcDeviceRequest.BypassSelfHearing || opts.ReplayPath != ""
	// The single composed executor crosses into every session mode (live,
	// replay, record) here; the duplex loop construction seam decides whether
	// tool execution is enabled. The read_image binding is cloned per session
	// so its capability snapshot cannot leak across concurrent sessions.
	plan.loop.ToolExecutor = bindSessionImageToolExecutor(opts, plan)
	plan.loop.ToolDefinitions = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	policySnapshot := interactivePolicy.Clone()
	plan.interactivePolicy = &policySnapshot
	plan.loop.InteractiveToolPolicy = &policySnapshot
	// The per-invocation adapter deadline override is a hermetic test seam;
	// zero selects the class-specific policy budget.
	plan.loop.ToolDefinitionBase = append([]messages.ToolDefinition(nil), opts.ToolDefinitionBase...)
	plan.loop.RefreshToolDefinitions = opts.RefreshToolDefinitions
	plan.loop.BrowserWatch = opts.BrowserWatch
	// The per-invocation adapter deadline override crosses with the executor;
	// zero keeps every production plan on defaultSessionToolExecutionTimeout.
	plan.loop.ToolExecutionTimeout = opts.ToolExecutionTimeout
	plan.loop.ScheduledAudioDispatch = scheduledAudioDispatch
	if opts.AudioService == nil {
		return sessionRuntimePlan{}, errors.New("audio service is required for session rate resolution")
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
		return sessionRuntimePlan{}, err
	}
	plan.outputAudioSampleRate = rates.OutputRate
	plan.inputAudioSampleRate = rates.InputRate
	if configurer, ok := plan.inferencer.(runtimeAudioOutputConfigurer); ok {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rates.OutputRate))
	}
	if configurer, ok := plan.inferencer.(runtimeAudioInputConfigurer); ok {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rates.InputRate))
	}
	plan.audioInputs, err = opts.AudioService.ConvertScheduledInputs(context.Background(), plan.audioInputs, plan.inputAudioSampleRate)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.loop.InputAudioSampleRate = plan.inputAudioSampleRate
	if plan.rtcDeviceRequest.HasOutput() && plan.outputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = plan.outputAudioSampleRate
	}
	if plan.rtcDeviceRequest.HasInput() && plan.inputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = plan.inputAudioSampleRate
	}
	// The playback-overflow observer's sink is resolved (never trusted as-is)
	// so an omitted SessionRunOptions.Diagnostics can no longer make a real
	// device overflow invisible; see resolvePlaybackDiagnosticSink.
	observabilityDependencies := opts.Observability
	plan.rtcDeviceRequest.PlaybackObserver = combineRTCDevicePlaybackObservers(
		plan.rtcDeviceRequest.PlaybackObserver,
		sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(plan.diagnostics)),
		sessionPlaybackObservabilityObserver(observabilityDependencies.MetricSampler, observabilityDependencies.Logger),
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
		sessionCaptureObservabilityObserver(observabilityDependencies.MetricSampler, observabilityDependencies.Logger),
	)
	plan.selection = selection
	plan.transport = selection.Transport
	plan.signalingEndpoint = selection.SignalingEndpoint
	plan.mediaSource = selection.MediaSource
	if plan.rtcRuntime == nil && selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	plan.capabilityCoordinator = capabilityCoordinator
	plan.recordingService = opts.recordingService
	if opts.RecordDirectory != "" {
		evidenceOptions := sessionLiveEvidenceOptions(opts, plan, opts.RecordDirectory, opts.RecordMaxDuration)
		plan.liveEvidenceOptions = &evidenceOptions
		plan.browserRecording = newSessionBrowserRecording(opts, plan)
	}
	if plan.recordingSession != nil {
		plan.inferencer = plan.recordingSession
	}
	return plan, nil
}

func sessionLiveEvidenceOptions(opts SessionRunOptions, plan sessionRuntimePlan, destination string, maxDuration time.Duration) runtimerecording.LiveEvidenceOptions {
	model := strings.TrimSpace(plan.model)
	if model == "" {
		model = strings.TrimSpace(opts.Model)
	}
	options := runtimerecording.LiveEvidenceOptions{
		Destination: destination, Provider: plan.provider, Model: model,
		ClockBase: plan.clockSource.Now(), WallClockStart: time.Now().UTC(),
		OutputAudioRate:         plan.outputAudioSampleRate,
		Credentials:             sessionRecordingCredentials(opts, plan),
		ProviderCaptureRequired: plan.mode == sessionRuntimeModeRecordOpenAI || plan.mode == sessionRuntimeModeRecordGrok,
	}
	if strings.TrimSpace(opts.RecordPath) != "" {
		options.ProviderCapturePath = opts.RecordPath
		options.DisableProviderCaptureSidecar = maxDuration > 0
	}
	return options
}

func sessionRecordingCredentials(opts SessionRunOptions, plan sessionRuntimePlan) []string {
	credentials := make([]string, 0, 2)
	appendCredential := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range credentials {
			if existing == value {
				return
			}
		}
		credentials = append(credentials, value)
	}
	appendCredential(opts.APIKey)
	if opts.LoadedConfig != nil {
		switch strings.ToLower(strings.TrimSpace(plan.provider)) {
		case sessionProviderOpenAI:
			if opts.LoadedConfig.Model.OpenAI != nil {
				appendCredential(opts.LoadedConfig.Model.OpenAI.APIKey)
			}
		case sessionProviderGrok:
			if opts.LoadedConfig.Model.Grok != nil {
				appendCredential(opts.LoadedConfig.Model.Grok.APIKey)
			}
		}
	}
	return credentials
}

func resolveSessionInteractiveToolPolicy(opts SessionRunOptions, definitions []messages.ToolDefinition) (InteractiveToolPolicy, error) {
	if opts.InteractiveToolPolicy != nil {
		policy := opts.InteractiveToolPolicy.Clone()
		if err := policy.Validate(); err != nil {
			return InteractiveToolPolicy{}, fmt.Errorf("resolve interactive tool policy: %w", err)
		}
		return policy, nil
	}

	loadedConfig := opts.LoadedConfig
	if loadedConfig == nil && opts.ConfigDir != "" {
		// The CLI composition root supplies LoadedConfig alongside its tool
		// definitions. Direct service callers may only provide ConfigDir; honor
		// an existing file there without creating a new config as a planning
		// side effect. Provider resolution retains ownership of default-file
		// creation when no file exists.
		configPath := filepath.Join(opts.ConfigDir, config.ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			storage, storageErr := config.NewDefaultConfigStorage(opts.ConfigDir)
			if storageErr != nil {
				return InteractiveToolPolicy{}, fmt.Errorf("initialize interactive tool configuration: %w", storageErr)
			}
			loadedConfig, storageErr = storage.Load()
			if storageErr != nil {
				return InteractiveToolPolicy{}, fmt.Errorf("load interactive tool configuration: %w", storageErr)
			}
		} else if !os.IsNotExist(err) {
			return InteractiveToolPolicy{}, fmt.Errorf("inspect interactive tool configuration: %w", err)
		}
	}
	settings := config.DefaultInteractiveToolConfig()
	if loadedConfig != nil {
		resolved, err := loadedConfig.ResolveInteractiveToolConfig()
		if err != nil {
			return InteractiveToolPolicy{}, fmt.Errorf("resolve interactive tool policy: %w", err)
		}
		settings = resolved
	}
	return NewInteractiveToolPolicyForSession(settings, definitions, opts.ToolDefinitionBase, opts.BrowserToolsEnabled)
}

func planSessionRuntimeMode(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planSessionRuntimeModeContext(context.Background(), opts, factory)
}

func injectedSessionMaxDuration(bareLive bool) time.Duration {
	if bareLive {
		return 0
	}
	return 3 * time.Second
}

func planReplaySessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planReplaySessionRuntimeContext(context.Background(), opts, factory)
}

func planRecordSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	provider := effectiveSessionProvider(opts)
	switch provider {
	case sessionProviderOpenAI:
		return planOpenAIRecordRuntime(opts, factory)
	case sessionProviderGrok:
		return planGrokRecordRuntime(opts, factory)
	default:
		return sessionRuntimePlan{}, unsupportedRealtimeSessionProviderError(provider)
	}
}

const injectedSessionDefaultMaxDuration = 3 * time.Second

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
	case opts.RecordPath != "" || opts.RecordDirectory != "":
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

func planReplaySessionRuntimeContext(ctx context.Context, opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.SessionInferencer != nil {
		return genericInjectedReplayPlan(opts), nil
	}
	service := opts.replayService
	if service == nil {
		return sessionRuntimePlan{}, errors.New("replay service is not configured")
	}
	inspection, err := service.InspectCapture(ctx, opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inspection.IsRealtime() {
		return planRealtimeReplay(ctx, opts, factory, inspection)
	}
	return planTurnReplay(opts, service, inspection)
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

func planTurnReplay(opts SessionRunOptions, service runtimereplay.Service, inspection runtimereplay.CaptureInspection) (sessionRuntimePlan, error) {
	return sessionRuntimePlan{
		mode: sessionRuntimeModeReplayGeneric, capturePath: opts.ReplayPath,
		replayIntegrityWarning: inspection.IntegrityWarning, loopOut: io.Discard,
		loop: sessionLoopOptions{Prompt: opts.Prompt},
		finalize: func(ctx context.Context, out io.Writer) error {
			return runReplayCapture(ctx, out, service, opts.ReplayPath)
		},
	}, nil
}

func prepareReplayLiveContext(ctx context.Context, opts SessionRunOptions) (runtimereplay.LivePrepared, transport.Dialer, runtimereplay.CaptureInspection, error) {
	service := opts.replayService
	if service == nil {
		return nil, nil, runtimereplay.CaptureInspection{}, errors.New("replay service is not configured")
	}
	timing := runtimesession.LiveReplayTimingFast
	if normalizedSessionReplayTiming(opts.ReplayTiming) == sessionReplayTimingRecorded {
		timing = runtimesession.LiveReplayTimingRealtime
	}
	prepared, err := service.PrepareLive(ctx, runtimereplay.LiveRequest{SourcePath: opts.ReplayPath, Timing: timing})
	if err != nil {
		return nil, nil, runtimereplay.CaptureInspection{}, err
	}
	inspection := prepared.Inspection()
	if inspection.LivePlan == nil {
		return nil, nil, runtimereplay.CaptureInspection{}, closePreparedReplay(prepared, fmt.Errorf("replay session capture %s has no admitted live plan", opts.ReplayPath))
	}
	return prepared, prepared.WrapDialer(nil), inspection, nil
}

func replayInputAudioSampleRate(inspection runtimereplay.CaptureInspection) int {
	if inspection.LivePlan == nil {
		return 0
	}
	return inspection.LivePlan.InputAudioSampleRate
}

func replayOutputAudioSampleRate(inspection runtimereplay.CaptureInspection) int {
	if inspection.LivePlan == nil {
		return 0
	}
	return inspection.LivePlan.OutputAudioSampleRate
}

func replayProviderCloseExpected(inspection runtimereplay.CaptureInspection) bool {
	return inspection.LivePlan != nil && inspection.LivePlan.ProviderCloseExpected
}

func replayMaxDuration(inspection runtimereplay.CaptureInspection) time.Duration {
	if inspection.LivePlan == nil {
		return 0
	}
	return inspection.LivePlan.MaxDuration
}

func wrapSessionRuntimeError(plan sessionRuntimePlan, err error) error {
	if err == nil {
		return nil
	}
	err = decorateRateLimitedSessionRuntimeError(err)
	switch plan.mode {
	case sessionRuntimeModeRecordGrok, sessionRuntimeModeRecordOpenAI:
		return fmt.Errorf("record session capture %s: %w", plan.capturePath, err)
	case sessionRuntimeModeReplayGeneric, sessionRuntimeModeReplayGrok, sessionRuntimeModeReplayOpenAI:
		return fmt.Errorf("replay session capture %s: %w", plan.capturePath, err)
	default:
		return err
	}
}

func decorateRateLimitedSessionRuntimeError(err error) error {
	if err == nil || strings.Contains(err.Error(), "classification=") {
		return err
	}
	classification := gwproviders.SessionErrorClassification("", "", err.Error())
	if classification != gwproviders.ErrorClassRateLimited {
		return err
	}
	return fmt.Errorf("[classification=%s]: %w", classification, err)
}

func missingOwnedSessionDialerError(provider string) error {
	return fmt.Errorf("%s session runtime requires an injected websocket dialer", provider)
}
