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
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type sessionRuntimeMode string

const (
	sessionRuntimeModeBareLive      sessionRuntimeMode = "bare-live"
	sessionRuntimeModeInjectedLive  sessionRuntimeMode = "injected-live"
	sessionRuntimeModeReplayGeneric sessionRuntimeMode = "replay-generic"
	sessionRuntimeModeReplayGrok    sessionRuntimeMode = "replay-grok-websocket"
	sessionRuntimeModeReplayOpenAI  sessionRuntimeMode = "replay-openai-websocket"
	sessionRuntimeModeRecordGrok    sessionRuntimeMode = "record-grok"
	sessionRuntimeModeRecordOpenAI  sessionRuntimeMode = "record-openai"
)

type sessionRecordingDialer interface {
	transport.Dialer
	FlushToFile(path string) error
}

type sessionReplayDialer interface {
	transport.Dialer
	Done() <-chan struct{}
	Err() error
	Model() string
}

type sessionRuntimeFactory struct {
	newDefaultLiveDialer               func() transport.Dialer
	newRecordingDialer                 func(transport.Dialer, string, string) sessionRecordingDialer
	newReplayDialer                    func(string) (sessionReplayDialer, error)
	newRecordedTimingReplayDialer      func(string) (sessionReplayDialer, error)
	newReplayInferencer                func(string) messages.SessionInferencer
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
	return f.newDefaultLiveDialer != nil || f.newReplayDialer != nil || f.newBareLiveSessionInferencer != nil || f.newRTCRuntime != nil
}

func newDefaultSessionRuntimeFactory() sessionRuntimeFactory {
	return sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			return grok.NewDefaultWebSocketDialer()
		},
		newRecordingDialer: func(inner transport.Dialer, providerName string, model string) sessionRecordingDialer {
			return gwtesting.NewRecordingWebSocketDialer(inner, providerName, model)
		},
		newReplayDialer: func(path string) (sessionReplayDialer, error) {
			return gwtesting.NewReplayWebSocketDialer(path)
		},
		newRecordedTimingReplayDialer: func(path string) (sessionReplayDialer, error) {
			return gwtesting.NewReplayWebSocketDialer(path, gwtesting.WithRecordedSessionTiming())
		},
		newReplayInferencer: func(path string) messages.SessionInferencer {
			return gwtesting.NewReplaySessionInferencer(path)
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

func (f sessionRuntimeFactory) replayDialer(path, timing string) (sessionReplayDialer, error) {
	if normalizedSessionReplayTiming(timing) == sessionReplayTimingRecorded && f.newRecordedTimingReplayDialer != nil {
		return f.newRecordedTimingReplayDialer(path)
	}
	return f.newReplayDialer(path)
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
	inputAudioSampleRate   int
	outputAudioSampleRate  int
	capturePath            string
	loopOut                io.Writer
	inferencer             messages.SessionInferencer
	loop                   sessionLoopOptions
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
	rtcDeviceRequest       RTCDeviceBindingRequest
	capabilityCoordinator  SessionCapabilityCoordinator
	captureClaim           *sessionRecordingClaim
	captureClaimWired      bool
	interactivePolicy      *InteractiveToolPolicy
	filesystemPolicy       *tools.FilesystemPolicy
}

func (p sessionRuntimePlan) bareLiveOutput(binding *RTCDeviceBinding) (string, string) {
	return p.liveOutput(binding, "Starting bare live session: ")
}

func (p sessionRuntimePlan) browserLiveOutput(binding *RTCDeviceBinding) (string, string) {
	return p.liveOutput(binding, "Starting WebMCP browser live session: ")
}

func (p sessionRuntimePlan) liveOutput(binding *RTCDeviceBinding, prefix string) (string, string) {
	transport := p.transport
	if transport == "" {
		transport = SessionTransportWebSocket
	}
	inputDevice, outputDevice := "unavailable", "unavailable"
	if binding != nil {
		if binding.Source != nil {
			inputDevice = string(binding.Source.DeviceID())
		}
		if binding.Sink != nil {
			outputDevice = string(binding.Sink.DeviceID())
		}
	}
	identity := fmt.Sprintf("provider=%s model=%s transport=%s input-device=%s output-device=%s", p.provider, p.model, transport, inputDevice, outputDevice)
	return prefix + identity, "Listening: " + identity
}

func (p sessionRuntimePlan) run(ctx context.Context, out io.Writer) (runErr error) {
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		p.loop.terminalReporter = reporter
	}
	finalizer := newSessionRuntimeFinalizer(p)
	defer func() {
		runErr = finalizer.finish(ctx, out, runErr)
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

	deviceBinding, err := PrepareRTCDeviceBindings(p.rtcDeviceRequest)
	if err != nil {
		return err
	}
	if deviceBinding != nil {
		p.loop.rtcDeviceBinding = deviceBinding
		finalizer.setDeviceBinding(deviceBinding)
	}
	// The filesystem-scope disclosure is best-effort: it is new, unconditional
	// startup output on every session, and a write failure here must not
	// masquerade as (or pre-empt) the session's own run/drain failure below,
	// which is what a broken writer is actually expected to surface as.
	writeFilesystemScopeAnnouncement(out, p.filesystemPolicy)
	writeSessionToolAnnouncement(out, p.loop.ToolDefinitions)
	announcement := p.announce
	if p.loop.BareLive {
		announcement, p.loop.ListeningBanner = p.bareLiveOutput(deviceBinding)
	} else if p.loop.BrowserToolsInteractive {
		announcement, p.loop.ListeningBanner = p.browserLiveOutput(deviceBinding)
	}
	if announcement != "" {
		if _, err := fmt.Fprintln(out, announcement); err != nil {
			return err
		}
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
	obs.streamObserver = p.streamObserver
	obs.runtime = p.runtime
	obs.livenessClock = loop.livenessClock
	if obs.livenessClock == nil {
		obs.livenessClock = sessionLivenessClockFromSource(p.clockSource)
	}
	obs.cancellationIntent = loop.cancellationIntent
	obs.requireSessionUpdated = loop.RequireSessionUpdated
	obs.scheduledAudioDispatch = loop.ScheduledAudioDispatch
	obs.scheduleAudioInputs(p.audioInputs)
	loop.observer = obs
}

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	factory := opts.runtimeFactory
	if !factory.configured() {
		// Kept for package-local test callers while composition migrates. All
		// production service entrypoints install runtimeFactory from Wire.
		factory = newDefaultSessionRuntimeFactory()
	}
	return planSessionRuntimeWithFactory(opts, factory)
}

func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	recordingClaim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return sessionRuntimePlan{}, err
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
		if planErr != nil && recordingClaim != nil {
			_ = recordingClaim.release()
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
		plan, err = planSessionRuntimeMode(opts, factory)
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
	plan.rtcDeviceRequest = opts.RTCDeviceBinding
	// Local device playback of this session's own synthesized voice must
	// carry the same fixed per-voice loudness correction as every other
	// output path (see VoiceLoudnessGainDB), so --voice selection does not
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
	if err := configureSessionAudioContract(opts, &plan); err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.audioInputs, err = convertScheduledAudioInputs(plan.audioInputs, plan.inputAudioSampleRate)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	plan.loop.InputAudioSampleRate = plan.inputAudioSampleRate
	if plan.rtcDeviceRequest.outputSelected() && plan.outputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.OutputSampleRate = plan.outputAudioSampleRate
	}
	if plan.rtcDeviceRequest.inputSelected() && plan.inputAudioSampleRate > 0 {
		plan.rtcDeviceRequest.InputSampleRate = plan.inputAudioSampleRate
	}
	// The playback-overflow observer's sink is resolved (never trusted as-is)
	// so an omitted SessionRunOptions.Diagnostics can no longer make a real
	// device overflow invisible; see resolvePlaybackDiagnosticSink.
	observabilityDependencies := opts.Observability
	if observabilityDependencies.MetricSampler == nil && observabilityDependencies.Logger == nil {
		observabilityDependencies = plan.rtcDeviceRequest.Observability
	}
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
	plan = wireSessionRecordingClaim(plan, recordingClaim)
	return plan, nil
}

// wireSessionRecordingClaim redirects one recording plan's capture flush
// through its destination claim. It is kept separate from planning because an
// injected session can add its fixture recorder after the generic runtime plan
// has been built.
func wireSessionRecordingClaim(plan sessionRuntimePlan, claim *sessionRecordingClaim) sessionRuntimePlan {
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
		err := claim.publish(flushTo)
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
	if opts.ReplayPath != "" {
		return planReplaySessionRuntime(opts, factory)
	}
	if opts.SessionInferencer != nil {
		if err := validateInjectedLiveSession(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		provider := strings.ToLower(effectiveSessionProvider(opts))
		model := strings.TrimSpace(opts.Model)
		if model == "" {
			switch provider {
			case sessionProviderOpenAI:
				resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
				if err != nil {
					return sessionRuntimePlan{}, err
				}
				model = resolved.Model
			case sessionProviderGrok:
				resolved, err := resolveGrokSessionConfig(opts)
				if err != nil {
					return sessionRuntimePlan{}, err
				}
				model = resolved.Model
			}
		}
		interactive := browserToolsInteractiveLive(opts)
		return sessionRuntimePlan{
			mode:       sessionRuntimeModeInjectedLive,
			provider:   provider,
			model:      model,
			inferencer: opts.SessionInferencer,
			loop: sessionLoopOptions{
				Prompt:                   opts.Prompt,
				CloseAfterOpen:           !opts.BareLive && !interactive && !opts.WaitForClose && len(opts.AudioInputs) == 0,
				WaitForClose:             opts.BareLive || interactive || opts.WaitForClose || len(opts.AudioInputs) > 0,
				CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
				MaxDuration:              injectedSessionMaxDuration(opts.BareLive || interactive),
				AdvertiseToolDefinitions: true,
				RequireSessionUpdated:    len(opts.AudioInputs) > 0 && strings.EqualFold(effectiveSessionProvider(opts), sessionProviderOpenAI),
				BareLive:                 opts.BareLive,
				BrowserToolsInteractive:  interactive,
			},
		}, nil
	}
	if opts.BareLive {
		return planBareLiveSessionRuntime(opts, factory)
	}
	if opts.RecordPath == "" {
		if opts.BrowserToolsEnabled {
			return planBrowserLiveSessionRuntime(opts, factory)
		}
		return planLiveSessionRuntime(opts, factory)
	}
	return planRecordSessionRuntime(opts, factory)
}

func injectedSessionMaxDuration(bareLive bool) time.Duration {
	if bareLive {
		return 0
	}
	return 3 * time.Second
}

func planReplaySessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionInferencer := opts.SessionInferencer
	if sessionInferencer != nil {
		return sessionRuntimePlan{
			mode:        sessionRuntimeModeReplayGeneric,
			capturePath: opts.ReplayPath,
			provider:    strings.ToLower(strings.TrimSpace(opts.Provider)),
			model:       opts.Model,
			inferencer:  sessionInferencer,
			loop: sessionLoopOptions{
				Prompt:                   opts.Prompt,
				WaitForClose:             opts.WaitForClose,
				MaxDuration:              3 * time.Second,
				AdvertiseToolDefinitions: true,
			},
		}, nil
	}

	loaded, err := gwtesting.LoadSessionCaptureForReplay(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	replayIntegrityWarning := loaded.IntegrityWarning(opts.ReplayPath)

	if _, err := os.Stat(opts.ReplayPath); err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}

	if usesWebSocketCapture(opts.ReplayPath) {
		if usesOpenAIWebSocketCapture(opts.ReplayPath) {
			plan, err := planOpenAIReplayRuntime(opts, factory)
			if err != nil {
				return sessionRuntimePlan{}, err
			}
			plan.replayIntegrityWarning = replayIntegrityWarning
			return plan, nil
		}
		plan, err := planGrokReplayRuntime(opts, factory)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		plan.replayIntegrityWarning = replayIntegrityWarning
		return plan, nil
	}

	return sessionRuntimePlan{
		mode:                   sessionRuntimeModeReplayGeneric,
		capturePath:            opts.ReplayPath,
		replayIntegrityWarning: replayIntegrityWarning,
		loopOut:                io.Discard,
		inferencer:             factory.newReplayInferencer(opts.ReplayPath),
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
