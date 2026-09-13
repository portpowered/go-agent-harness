package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	rtcsessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession/wire"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// Deprecated: use go-agent-runtime/services/rtcsession; these aliases preserve the existing CLI composition seam during the graph migration.
type SessionRTCRuntime = rtcontract.SessionRTCRuntime
type SessionRTCDataPlane = rtcontract.SessionRTCDataPlane
type SessionRTCRuntimeFactory = rtcontract.SessionRTCRuntimeFactory
type SessionRTCSignalingResolver = rtcontract.SessionRTCSignalingResolver
type SessionRTCDataPlaneFactory = rtcontract.SessionRTCDataPlaneFactory
type SessionRTCMediaSourceOpener = rtcontract.SessionRTCMediaSourceOpener
type SessionRTCComponents = rtcontract.SessionRTCComponents
type SessionRTCRuntimeError = rtcsession.SessionRTCRuntimeError

var ErrSessionRTCRuntimeUnavailable = rtcsession.ErrSessionRTCRuntimeUnavailable
var ErrSessionRTCRuntimeClosed = rtcsession.ErrSessionRTCRuntimeClosed
var ErrSessionRTCDataPlaneUnavailable = rtcsession.ErrSessionRTCDataPlaneUnavailable

func NewSessionRTCRuntimeFactory(components SessionRTCComponents) SessionRTCRuntimeFactory {
	return NewSessionRTCRuntimeFactoryWithObservability(components, nil, nil)
}

func NewSessionRTCRuntimeFactoryWithObservability(components SessionRTCComponents, sampler observability.MetricSampler, logger observability.Logger) SessionRTCRuntimeFactory {
	service := rtcsessionwire.NewService(toPublicComponents(components), sampler, logger)
	return func(selection SessionRuntimeSelection) (SessionRTCRuntime, error) {
		runtime, err := service.NewRuntime(toPublicSelection(selection))
		if err != nil {
			return nil, err
		}
		return &sessionRTCRuntimeAdapter{runtime: runtime, service: service}, nil
	}
}
func toPublicSelection(s SessionRuntimeSelection) rtcsession.SessionRuntimeSelection {
	return rtcsession.SessionRuntimeSelection{Transport: s.Transport, SignalingEndpoint: s.SignalingEndpoint, MediaSource: s.MediaSource}
}
func toPublicComponents(components SessionRTCComponents) rtcsession.SessionRTCComponents {
	public := rtcsession.SessionRTCComponents{}
	if components.ResolveSignaling != nil {
		public.ResolveSignaling = func(ctx context.Context, endpoint string) (rtc.Signaling, error) {
			return components.ResolveSignaling(ctx, endpoint)
		}
	}
	if components.NewDataPlane != nil {
		public.NewDataPlane = func(ctx context.Context, signaling rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			return components.NewDataPlane(ctx, signaling)
		}
	}
	if components.OpenMediaSource != nil {
		public.OpenMediaSource = func(ctx context.Context, source string) (sharedaudio.InboundMedia, error) {
			return components.OpenMediaSource(ctx, source)
		}
	}
	return public
}
func planWebRTCSessionRuntime(opts SessionRunOptions, selection SessionRuntimeSelection, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	runtimeFactory := opts.RTCRuntimeFactory
	if runtimeFactory == nil {
		runtimeFactory = factory.newRTCRuntime
	}
	if runtimeFactory == nil {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	runtime, err := runtimeFactory(selection)
	if err != nil {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", err)
	}
	if runtime == nil {
		return sessionRuntimePlan{}, wrapSessionRTCRuntimeError("create runtime", ErrSessionRTCRuntimeUnavailable)
	}
	decorator := sessionRTCRuntimeDecoratorFor(runtime)
	closeOnPlanError := func(planErr error) (sessionRuntimePlan, error) {
		return sessionRuntimePlan{}, errors.Join(planErr, wrapSessionPhaseError("close WebRTC runtime", runtime.Close()))
	}
	provider := effectiveSessionProvider(opts)
	opts.Provider = provider
	model := opts.Model
	var (
		inner           messages.SessionInferencer
		recordingDialer sessionRecordingDialer
		flushCapture    func() error
		flushCaptureTo  func(string) error
		announce        string
		finalize        func(context.Context, io.Writer) error
		mode            = sessionRuntimeModeInjectedLive
	)
	if opts.SessionInferencer != nil {
		inner = opts.SessionInferencer
	} else {
		provider, model, mode, recordingDialer, inner, err = planWebRTCProvider(opts, provider, factory, decorator)
		if err != nil {
			return closeOnPlanError(err)
		}
		flushCapture = func() error { return recordingDialer.FlushToFile(opts.RecordPath) }
		flushCaptureTo = func(path string) error { return recordingDialer.FlushToFile(path) }
		if provider == sessionProviderOpenAI {
			announce = fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath)
		} else {
			announce = fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath)
		}
		finalize = func(_ context.Context, out io.Writer) error {
			_, writeErr := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return writeErr
		}
	}
	if inner == nil {
		return closeOnPlanError(wrapSessionRTCRuntimeError("create provider session", ErrSessionRTCRuntimeUnavailable))
	}
	rtcInferencer := decorator.wrapInferencer(inner)
	return sessionRuntimePlan{mode: mode, provider: provider, model: model, capturePath: opts.RecordPath, announce: announce, inferencer: rtcInferencer, flushCapture: flushCapture, flushCaptureTo: flushCaptureTo, finalize: finalize, loop: sessionLoopOptions{Prompt: opts.Prompt, CloseAfterOpen: true}, rtcRuntime: runtime, closeSession: rtcInferencer.CloseSession}, nil
}

func planWebRTCProvider(opts SessionRunOptions, provider string, factory sessionRuntimeFactory, decorator *sessionRTCRuntimeDecorator) (string, string, sessionRuntimeMode, sessionRecordingDialer, messages.SessionInferencer, error) {
	if strings.EqualFold(provider, sessionProviderOpenAI) {
		return planWebRTCOpenAIProvider(opts, factory, decorator)
	}
	return planWebRTCGrokProvider(opts, factory, decorator)
}
func planWebRTCOpenAIProvider(opts SessionRunOptions, factory sessionRuntimeFactory, decorator *sessionRTCRuntimeDecorator) (string, string, sessionRuntimeMode, sessionRecordingDialer, messages.SessionInferencer, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return "", "", "", nil, nil, err
	}
	provider, mode := sessionProviderOpenAI, sessionRuntimeModeRecordOpenAI
	dialer := factory.newRecordingDialer(decorator.lazyDialer(), provider, sessionCfg.Model)
	if dialer == nil {
		return "", "", "", nil, nil, wrapSessionRTCRuntimeError("create recording transport", ErrSessionRTCRuntimeUnavailable)
	}
	inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, provider, opts.RTCDeviceBinding.InputPresent)
	inner, err := factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, dialer, opts.ToolDefinitions, false, inputAudioTranscription)
	return provider, sessionCfg.Model, mode, dialer, inner, err
}

func planWebRTCGrokProvider(opts SessionRunOptions, factory sessionRuntimeFactory, decorator *sessionRTCRuntimeDecorator) (string, string, sessionRuntimeMode, sessionRecordingDialer, messages.SessionInferencer, error) {
	sessionCfg, err := resolveGrokSessionConfig(opts)
	if err != nil {
		return "", "", "", nil, nil, err
	}
	provider, mode := sessionProviderGrok, sessionRuntimeModeRecordGrok
	dialer := factory.newRecordingDialer(decorator.lazyDialer(), provider, sessionCfg.Model)
	if dialer == nil {
		return "", "", "", nil, nil, wrapSessionRTCRuntimeError("create recording transport", ErrSessionRTCRuntimeUnavailable)
	}
	inner, err := factory.newGrokSessionInferencerForTools(sessionCfg, dialer, opts.ToolDefinitions)
	return provider, sessionCfg.Model, mode, dialer, inner, err
}

// Deprecated: CLI-only adapter while callers migrate to rtcsession.Service.
type sessionRTCRuntimeAdapter struct {
	runtime rtcsession.SessionRTCRuntime
	service rtcsession.Service
}

var _ SessionRTCRuntime = (*sessionRTCRuntimeAdapter)(nil)

func (a *sessionRTCRuntimeAdapter) Start(ctx context.Context) (SessionRTCDataPlane, error) {
	if a == nil || a.runtime == nil {
		return nil, wrapSessionRTCRuntimeError("start", ErrSessionRTCRuntimeUnavailable)
	}
	dataPlane, err := a.runtime.Start(ctx)
	if err != nil {
		return nil, err
	}
	return dataPlane, nil
}

func (a *sessionRTCRuntimeAdapter) Close() error {
	if a == nil || a.runtime == nil {
		return nil
	}
	return a.runtime.Close()
}

type sessionRTCRuntimeDecorator struct {
	runtime rtcsession.SessionRTCRuntime
	service rtcsession.Service
}

func sessionRTCRuntimeDecoratorFor(runtime SessionRTCRuntime) *sessionRTCRuntimeDecorator {
	if adapter, ok := runtime.(*sessionRTCRuntimeAdapter); ok && adapter.service != nil {
		return &sessionRTCRuntimeDecorator{runtime: adapter.runtime, service: adapter.service}
	}
	return &sessionRTCRuntimeDecorator{runtime: &publicSessionRTCRuntimeAdapter{runtime: runtime}, service: rtcsessionwire.NewService(rtcsession.SessionRTCComponents{}, nil, nil)}
}

// Deprecated: bridges legacy injected test/embedding runtimes to the public service.
type publicSessionRTCRuntimeAdapter struct{ runtime SessionRTCRuntime }

var _ rtcsession.SessionRTCRuntime = (*publicSessionRTCRuntimeAdapter)(nil)

func (a *publicSessionRTCRuntimeAdapter) Start(ctx context.Context) (rtcsession.SessionRTCDataPlane, error) {
	if a == nil || a.runtime == nil {
		return nil, wrapSessionRTCRuntimeError("start", ErrSessionRTCRuntimeUnavailable)
	}
	dataPlane, err := a.runtime.Start(ctx)
	if err != nil {
		return nil, err
	}
	if dataPlane == nil {
		return nil, nil
	}
	return &publicSessionRTCDataPlaneAdapter{dataPlane: dataPlane}, nil
}

func (a *publicSessionRTCRuntimeAdapter) Close() error {
	if a == nil || a.runtime == nil {
		return nil
	}
	return a.runtime.Close()
}

type publicSessionRTCDataPlaneAdapter struct{ dataPlane SessionRTCDataPlane }

var _ rtcsession.SessionRTCDataPlane = (*publicSessionRTCDataPlaneAdapter)(nil)

func (d *publicSessionRTCDataPlaneAdapter) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.dataPlane == nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", ErrSessionRTCDataPlaneUnavailable)
	}
	return d.dataPlane.Dial(endpoint, headers)
}

func (d *publicSessionRTCDataPlaneAdapter) AttachInboundMedia(ctx context.Context, media sharedaudio.InboundMedia) error {
	if d == nil || d.dataPlane == nil {
		return wrapSessionRTCRuntimeError("attach media source", ErrSessionRTCDataPlaneUnavailable)
	}
	return d.dataPlane.AttachInboundMedia(ctx, media)
}

func (d *publicSessionRTCDataPlaneAdapter) Close() error {
	if d == nil || d.dataPlane == nil {
		return nil
	}
	return d.dataPlane.Close()
}

func (d *sessionRTCRuntimeDecorator) wrapInferencer(inner messages.SessionInferencer) rtcsession.Inferencer {
	return d.service.WrapInferencer(d.runtime, inner)
}

func (d *sessionRTCRuntimeDecorator) lazyDialer() transport.Dialer {
	return d.service.NewLazyDialer(d.runtime)
}

func wrapSessionRTCRuntimeError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return &rtcsession.SessionRTCRuntimeError{Phase: phase, Err: err}
}
