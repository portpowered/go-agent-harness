// This file owns the shared session-runtime modes, factories, plan state, generic planning and dispatch, execution, and cross-provider error handling.
package agentruntime

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
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
	replayCompletion       func(sessionterminal.Reporter)
	diagnostics            SessionDiagnosticSink
	metricsRecorder        metrics.Recorder
	streamObserver         SessionStreamObserver
	audioInputs            []ScheduledAudioInput
	scheduledAudioDispatch ScheduledAudioDispatchPolicy
	clockSource            platformclock.Source
	runtime                sessiontrace.RuntimeRecorder
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
	captureClaim           *sessionRecordingClaim
	captureClaimWired      bool
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
	reporter := p.loop.terminalReporter
	if reporter == nil {
		reporter = sessionterminalwire.NewReporter()
		p.loop.terminalReporter = reporter
	}
	finalizer := newSessionRuntimeFinalizer(p)
	defer func() {
		runErr = finalizer.finish(ctx, out, runErr)
		if !sessionterminalwire.HasIndependentFailure(runErr) && p.replayCompletion != nil {
			p.replayCompletion(reporter)
		}
		runErr = errors.Join(runErr, reporter.Publish(out, runErr))
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
		reporter.MarkRunStarted()
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
	obs := sessiontracewire.NewObserver(sessiontrace.NewObserverOptions{
		Sink:                   p.diagnostics,
		Recorder:               p.metricsRecorder,
		Provider:               p.provider,
		Model:                  p.model,
		StreamObserver:         p.streamObserver,
		RuntimeRecorder:        p.runtime,
		TerminalService:        sessionterminalwire.NewService(),
		CancellationIntent:     loop.cancellationIntent,
		LivenessClock:          loop.livenessClock,
		RequireSessionUpdated:  loop.RequireSessionUpdated,
		ScheduledAudioDispatch: sessiontrace.ScheduledAudioDispatchPolicy(loop.ScheduledAudioDispatch),
	})
	obs.ScheduleAudioInputs(p.audioInputs)
	loop.observer = obs
}
func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithContext(context.Background(), opts)
}

//lint:ignore U1000 package tests exercise the context-free planning seam.
func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithFactoryAndContext(context.Background(), opts, factory)
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

func observeSessionWire(dialer transport.Dialer, opts SessionRunOptions) transport.Dialer {
	if dialer == nil {
		return nil
	}
	return sessiontracewire.NewProviderWireDialer(dialer, opts.RuntimeObserver, platformclock.Ensure(opts.Clock))
}
