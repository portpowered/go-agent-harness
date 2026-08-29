// This file owns the shared session-runtime modes, factories, plan state, generic planning and dispatch, execution, and cross-provider error handling.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type sessionRuntimeMode string

const (
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
	newReplayInferencer                func(string) messages.SessionInferencer
	newGrokSessionInferencer           func(config.GrokConfig, transport.Dialer) (messages.SessionInferencer, error)
	newOpenAISessionInf                func(config.OpenAIConfig, string, transport.Dialer) (messages.SessionInferencer, error)
	newGrokSessionWithTools            func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error)
	newOpenAISessionWithTools          func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error)
	newOpenAIScheduledSessionWithTools func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error)
	newRTCRuntime                      SessionRTCRuntimeFactory
}

var defaultSessionRuntimeFactory = sessionRuntimeFactory{
	newDefaultLiveDialer: func() transport.Dialer {
		return grok.NewDefaultWebSocketDialer()
	},
	newRecordingDialer: func(inner transport.Dialer, providerName string, model string) sessionRecordingDialer {
		return gwtesting.NewRecordingWebSocketDialer(inner, providerName, model)
	},
	newReplayDialer: func(path string) (sessionReplayDialer, error) {
		return gwtesting.NewReplayWebSocketDialer(path)
	},
	newReplayInferencer: func(path string) messages.SessionInferencer {
		return gwtesting.NewReplaySessionInferencer(path)
	},
	newGrokSessionInferencer: func(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencer(sessionCfg, dialer)
	},
	newOpenAISessionInf: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencer(sessionCfg, voice, dialer)
	},
	newGrokSessionWithTools: func(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithTools(sessionCfg, dialer, toolDefinitions)
	},
	newOpenAISessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithTools(sessionCfg, voice, dialer, toolDefinitions)
	},
	newOpenAIScheduledSessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithScheduledAudio(sessionCfg, voice, dialer, toolDefinitions)
	},
}

func (f sessionRuntimeFactory) newGrokSessionInferencerForTools(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if f.newGrokSessionWithTools != nil {
		return f.newGrokSessionWithTools(sessionCfg, dialer, toolDefinitions)
	}
	return f.newGrokSessionInferencer(sessionCfg, dialer)
}

func (f sessionRuntimeFactory) newOpenAISessionInferencerForTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, scheduledAudio bool) (messages.SessionInferencer, error) {
	if scheduledAudio && f.newOpenAIScheduledSessionWithTools != nil {
		return f.newOpenAIScheduledSessionWithTools(sessionCfg, voice, dialer, toolDefinitions)
	}
	if f.newOpenAISessionWithTools != nil {
		return f.newOpenAISessionWithTools(sessionCfg, voice, dialer, toolDefinitions)
	}
	return f.newOpenAISessionInf(sessionCfg, voice, dialer)
}

type sessionRuntimePlan struct {
	mode                   sessionRuntimeMode
	provider               string
	model                  string
	capturePath            string
	loopOut                io.Writer
	inferencer             messages.SessionInferencer
	loop                   sessionLoopOptions
	announce               string
	flushCapture           func() error
	finalize               func(context.Context, io.Writer) error
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
	capabilityCoordinator  *SessionCapabilityCoordinator
}

func (p sessionRuntimePlan) run(ctx context.Context, out io.Writer) (runErr error) {
	finalizer := newSessionRuntimeFinalizer(p)
	defer func() {
		runErr = finalizer.finish(ctx, out, runErr)
	}()

	deviceBinding, err := PrepareRTCDeviceBindings(p.rtcDeviceRequest)
	if err != nil {
		return err
	}
	if deviceBinding != nil {
		p.loop.rtcDeviceBinding = deviceBinding
		finalizer.setDeviceBinding(deviceBinding)
	}
	if p.announce != "" {
		if _, err := fmt.Fprintln(out, p.announce); err != nil {
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
		if err := runAgentLoopSession(ctx, loopOut, p.inferencer, loop); err != nil {
			return wrapSessionRuntimeError(p, wrapSessionPhaseError("run session loop", err))
		}
	}
	return nil
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
	obs.requireSessionUpdated = loop.RequireSessionUpdated
	obs.scheduledAudioDispatch = loop.ScheduledAudioDispatch
	obs.scheduleAudioInputs(p.audioInputs)
	loop.observer = obs
}

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithFactory(opts, defaultSessionRuntimeFactory)
}

func planSessionRuntimeWithFactory(opts SessionRunOptions, factory sessionRuntimeFactory) (plan sessionRuntimePlan, planErr error) {
	var capabilityCoordinator *SessionCapabilityCoordinator
	opts, capabilityCoordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		if planErr != nil {
			closeSessionCapabilityIfNeeded(capabilityCoordinator, &planErr)
		}
	}()
	if err := ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(opts.AudioInputs)); err != nil {
		return sessionRuntimePlan{}, err
	}
	scheduledAudioDispatch := scheduledAudioDispatchPolicyForOptions(opts)

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
	plan.audioInputs = opts.AudioInputs
	plan.scheduledAudioDispatch = scheduledAudioDispatch
	plan.clockSource = platformclock.Ensure(opts.Clock)
	plan.runtime = newSessionRuntimeObservationRecorder(opts.RuntimeObserver, plan.clockSource)
	plan.loop.runtime = plan.runtime
	plan.loop.SessionUpdatedTimeout = opts.SessionUpdatedTimeout
	plan.loop.AudioInterruptions = opts.AudioInterruptions
	plan.rtcDeviceRequest = opts.RTCDeviceBinding
	// The single composed executor crosses into every session mode (live,
	// replay, record) here; the duplex loop construction seam decides whether
	// tool execution is enabled. The read_image binding is cloned per session
	// so its capability snapshot cannot leak across concurrent sessions.
	plan.loop.ToolExecutor = bindSessionImageToolExecutor(opts, plan)
	plan.loop.ToolDefinitions = append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)
	// The per-invocation adapter deadline override crosses with the executor;
	// zero keeps every production plan on defaultSessionToolExecutionTimeout.
	plan.loop.ToolExecutionTimeout = opts.ToolExecutionTimeout
	plan.loop.ScheduledAudioDispatch = scheduledAudioDispatch
	plan.selection = selection
	plan.transport = selection.Transport
	plan.signalingEndpoint = selection.SignalingEndpoint
	plan.mediaSource = selection.MediaSource
	if plan.rtcRuntime == nil && selection.Transport == SessionTransportWebRTC && opts.ReplayPath == "" {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	plan.capabilityCoordinator = capabilityCoordinator
	return plan, nil
}

func planSessionRuntimeMode(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	if opts.ReplayPath != "" {
		return planReplaySessionRuntime(opts, factory)
	}
	if opts.SessionInferencer != nil {
		if err := validateInjectedLiveSession(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return sessionRuntimePlan{
			mode:       sessionRuntimeModeInjectedLive,
			provider:   strings.ToLower(effectiveSessionProvider(opts)),
			model:      opts.Model,
			inferencer: opts.SessionInferencer,
			loop: sessionLoopOptions{
				Prompt:                   opts.Prompt,
				CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
				WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
				CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
				MaxDuration:              3 * time.Second,
				AdvertiseToolDefinitions: true,
				RequireSessionUpdated:    len(opts.AudioInputs) > 0 && strings.EqualFold(effectiveSessionProvider(opts), sessionProviderOpenAI),
			},
		}, nil
	}
	if opts.BrowserToolsEnabled && opts.RecordPath == "" {
		return planBrowserLiveSessionRuntime(opts, factory)
	}
	return planRecordSessionRuntime(opts, factory)
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

	if _, err := os.Stat(opts.ReplayPath); err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}

	if usesWebSocketCapture(opts.ReplayPath) {
		if usesOpenAIWebSocketCapture(opts.ReplayPath) {
			return planOpenAIReplayRuntime(opts, factory)
		}
		return planGrokReplayRuntime(opts, factory)
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeReplayGeneric,
		capturePath: opts.ReplayPath,
		loopOut:     io.Discard,
		inferencer:  factory.newReplayInferencer(opts.ReplayPath),
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
	if strings.EqualFold(effectiveSessionProvider(opts), sessionProviderOpenAI) {
		return planOpenAIRecordRuntime(opts, factory)
	}
	return planGrokRecordRuntime(opts, factory)
}

func wrapSessionRuntimeError(plan sessionRuntimePlan, err error) error {
	if err == nil {
		return nil
	}
	switch plan.mode {
	case sessionRuntimeModeRecordGrok, sessionRuntimeModeRecordOpenAI:
		return fmt.Errorf("record session capture %s: %w", plan.capturePath, err)
	case sessionRuntimeModeReplayGeneric, sessionRuntimeModeReplayGrok, sessionRuntimeModeReplayOpenAI:
		return fmt.Errorf("replay session capture %s: %w", plan.capturePath, err)
	default:
		return err
	}
}

func missingOwnedSessionDialerError(provider string) error {
	return fmt.Errorf("%s session runtime requires an injected websocket dialer", provider)
}
