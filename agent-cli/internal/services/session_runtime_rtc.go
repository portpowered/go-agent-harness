package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrSessionRTCRuntimeUnavailable identifies a WebRTC selection for which
	// the application has not supplied the protocol-owning runtime factory.
	// A missing factory is an explicit setup failure; it must never fall back
	// to the WebSocket runtime.
	ErrSessionRTCRuntimeUnavailable = errors.New("WebRTC session runtime is unavailable")
	// ErrSessionRTCRuntimeClosed identifies a start attempted after the
	// runtime's caller-owned lifecycle has been closed.
	ErrSessionRTCRuntimeClosed = errors.New("WebRTC session runtime is closed")
	// ErrSessionRTCDataPlaneUnavailable identifies a runtime that completed
	// setup without returning the provider-facing data plane.
	ErrSessionRTCDataPlaneUnavailable = errors.New("WebRTC session data plane is unavailable")
)

// SessionRTCRuntime is the service boundary for one selected WebRTC session.
// Implementations own every resource created by Start and release it from
// Close. The service sees only provider-neutral transport interfaces.
type SessionRTCRuntime interface {
	Start(context.Context) (SessionRTCDataPlane, error)
	Close() error
}

// SessionRTCDataPlane is the provider-facing RTC data connection plus the
// separate media attachment seam. PCM frames never travel through Dial or
// transport.Conn message methods.
type SessionRTCDataPlane interface {
	transport.Dialer
	AttachInboundMedia(context.Context, rtc.InboundMedia) error
	Close() error
}

// SessionRTCRuntimeFactory constructs an inert runtime owner for one exact
// service selection. Construction must not resolve endpoints, open media, or
// start asynchronous work; those effects belong to SessionRTCRuntime.Start.
type SessionRTCRuntimeFactory func(SessionRuntimeSelection) (SessionRTCRuntime, error)

// SessionRTCSignalingResolver resolves one opaque service endpoint through
// the provider-neutral RTC signaling contract.
type SessionRTCSignalingResolver func(context.Context, string) (rtc.Signaling, error)

// SessionRTCDataPlaneFactory creates the provider-facing RTC peer/data path
// after signaling has been resolved. The returned data plane is caller-owned.
type SessionRTCDataPlaneFactory func(context.Context, rtc.Signaling) (SessionRTCDataPlane, error)

// SessionRTCMediaSourceOpener parses and opens one opaque media-source value
// through the existing RTC media-source contract. The returned inbound media
// endpoint is caller-owned by the runtime.
type SessionRTCMediaSourceOpener func(context.Context, string) (rtc.InboundMedia, error)

// SessionRTCComponents are the protocol-neutral dependencies needed by the
// service-owned runtime composition. Concrete signaling, peer, and media
// implementations remain behind these narrow function seams.
type SessionRTCComponents struct {
	ResolveSignaling SessionRTCSignalingResolver
	NewDataPlane     SessionRTCDataPlaneFactory
	OpenMediaSource  SessionRTCMediaSourceOpener
}

// NewSessionRTCRuntimeFactory returns a factory that composes the existing
// signaling, peer/data, and media-source components. It performs no setup at
// factory construction time; every resource is acquired by Start and released
// by Close.
func NewSessionRTCRuntimeFactory(components SessionRTCComponents) SessionRTCRuntimeFactory {
	return func(selection SessionRuntimeSelection) (SessionRTCRuntime, error) {
		if err := components.validate(); err != nil {
			return nil, err
		}
		return &sessionComposedRTCRuntime{
			selection:  selection,
			components: components,
		}, nil
	}
}

func (c SessionRTCComponents) validate() error {
	if c.ResolveSignaling == nil {
		return fmt.Errorf("%w: signaling resolver is not configured", ErrSessionRTCRuntimeUnavailable)
	}
	if c.NewDataPlane == nil {
		return fmt.Errorf("%w: RTC data-plane factory is not configured", ErrSessionRTCRuntimeUnavailable)
	}
	if c.OpenMediaSource == nil {
		return fmt.Errorf("%w: media-source opener is not configured", ErrSessionRTCRuntimeUnavailable)
	}
	return nil
}

// planWebRTCSessionRuntime keeps provider-specific configuration and capture
// construction behind the existing provider seams while replacing only the
// live transport owner. Runtime startup remains lazy until the session loop
// asks the inferencer to connect, which keeps planning free of network and
// media-source side effects.
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
	rtcInferencer := &sessionRTCRuntimeInferencer{runtime: runtime}

	closeOnPlanError := func(planErr error) (sessionRuntimePlan, error) {
		return sessionRuntimePlan{}, errors.Join(planErr, wrapSessionPhaseError("close WebRTC runtime", runtime.Close()))
	}

	provider := strings.ToLower(strings.TrimSpace(effectiveSessionProvider(opts)))
	model := opts.Model
	var (
		inner        messages.SessionInferencer
		flushCapture func() error
		announce     string
		finalize     func(context.Context, io.Writer) error
		mode         = sessionRuntimeModeInjectedLive
	)

	if opts.SessionInferencer != nil {
		inner = opts.SessionInferencer
	} else if strings.EqualFold(provider, sessionProviderOpenAI) {
		sessionCfg, resolveErr := resolveOpenAIRealtimeSessionConfig(opts)
		if resolveErr != nil {
			return closeOnPlanError(resolveErr)
		}
		provider = sessionProviderOpenAI
		model = sessionCfg.Model
		mode = sessionRuntimeModeRecordOpenAI
		dialer := &sessionRTCLazyDialer{runtime: runtime}
		recordingDialer := factory.newRecordingDialer(dialer, provider, model)
		if recordingDialer == nil {
			return closeOnPlanError(wrapSessionRTCRuntimeError("create recording transport", ErrSessionRTCRuntimeUnavailable))
		}
		inner, err = factory.newOpenAISessionInf(sessionCfg, opts.Voice, recordingDialer)
		if err != nil {
			return closeOnPlanError(err)
		}
		flushCapture = func() error { return recordingDialer.FlushToFile(opts.RecordPath) }
		announce = fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath)
		finalize = func(_ context.Context, out io.Writer) error {
			_, writeErr := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return writeErr
		}
	} else {
		sessionCfg, resolveErr := resolveGrokSessionConfig(opts)
		if resolveErr != nil {
			return closeOnPlanError(resolveErr)
		}
		provider = sessionProviderGrok
		model = sessionCfg.Model
		mode = sessionRuntimeModeRecordGrok
		dialer := &sessionRTCLazyDialer{runtime: runtime}
		recordingDialer := factory.newRecordingDialer(dialer, provider, model)
		if recordingDialer == nil {
			return closeOnPlanError(wrapSessionRTCRuntimeError("create recording transport", ErrSessionRTCRuntimeUnavailable))
		}
		inner, err = factory.newGrokSessionInferencer(sessionCfg, recordingDialer)
		if err != nil {
			return closeOnPlanError(err)
		}
		flushCapture = func() error { return recordingDialer.FlushToFile(opts.RecordPath) }
		announce = fmt.Sprintf("Starting Grok session recording to %s", opts.RecordPath)
		finalize = func(_ context.Context, out io.Writer) error {
			_, writeErr := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return writeErr
		}
	}
	if inner == nil {
		return closeOnPlanError(wrapSessionRTCRuntimeError("create provider session", ErrSessionRTCRuntimeUnavailable))
	}
	rtcInferencer.inner = inner

	return sessionRuntimePlan{
		mode:         mode,
		provider:     provider,
		model:        model,
		capturePath:  opts.RecordPath,
		announce:     announce,
		inferencer:   rtcInferencer,
		flushCapture: flushCapture,
		finalize:     finalize,
		loop: sessionLoopOptions{
			Prompt:         opts.Prompt,
			CloseAfterOpen: true,
		},
		rtcRuntime:   runtime,
		closeSession: rtcInferencer.CloseSession,
	}, nil
}

// SessionRTCRuntimeError adds bounded phase context while preserving the
// underlying signaling, peer, media-source, provider, and context identity.
type SessionRTCRuntimeError struct {
	Phase string
	Err   error
}

func (e *SessionRTCRuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Phase == "" {
		return fmt.Sprintf("WebRTC session runtime: %v", e.Err)
	}
	if e.Err == nil {
		return fmt.Sprintf("WebRTC session runtime %s", e.Phase)
	}
	return fmt.Sprintf("WebRTC session runtime %s: %v", e.Phase, e.Err)
}

func (e *SessionRTCRuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapSessionRTCRuntimeError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return &SessionRTCRuntimeError{Phase: phase, Err: err}
}

type sessionComposedRTCRuntime struct {
	selection  SessionRuntimeSelection
	components SessionRTCComponents

	startMu sync.Mutex
	mu      sync.Mutex

	started   bool
	failed    bool
	startErr  error
	closed    bool
	startDone chan struct{}
	cancel    context.CancelFunc

	signaling rtc.Signaling
	dataPlane SessionRTCDataPlane
	media     rtc.InboundMedia

	closeOnce sync.Once
	closeErr  error
}

var _ SessionRTCRuntime = (*sessionComposedRTCRuntime)(nil)

func (r *sessionComposedRTCRuntime) Start(ctx context.Context) (SessionRTCDataPlane, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, wrapSessionRTCRuntimeError("start", ErrSessionRTCRuntimeClosed)
	}
	if r.started {
		dataPlane := r.dataPlane
		r.mu.Unlock()
		return dataPlane, nil
	}
	if r.failed {
		err := r.startErr
		r.mu.Unlock()
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	startDone := make(chan struct{})
	r.cancel = cancel
	r.startDone = startDone
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.startDone == startDone {
			r.startDone = nil
			close(startDone)
		}
		r.mu.Unlock()
	}()

	var signaling rtc.Signaling
	var dataPlane SessionRTCDataPlane
	var media rtc.InboundMedia
	fail := func(phase string, err error) (SessionRTCDataPlane, error) {
		wrapped := wrapSessionRTCRuntimeError(phase, err)
		cancel()
		closeErr := closeSessionRTCResources(media, dataPlane, signaling)
		if closeErr != nil {
			wrapped = errors.Join(wrapped, wrapSessionRTCRuntimeError("cleanup after "+phase, closeErr))
		}
		r.mu.Lock()
		r.failed = true
		r.startErr = wrapped
		r.cancel = nil
		r.mu.Unlock()
		return nil, wrapped
	}

	if r.components.ResolveSignaling == nil {
		return fail("resolve signaling", ErrSessionRTCRuntimeUnavailable)
	}
	signaling, err := r.components.ResolveSignaling(runCtx, r.selection.SignalingEndpoint)
	if err != nil {
		return fail("resolve signaling", err)
	}
	if signaling == nil {
		return fail("resolve signaling", ErrSessionRTCRuntimeUnavailable)
	}

	if r.components.NewDataPlane == nil {
		return fail("create RTC peer/data path", ErrSessionRTCRuntimeUnavailable)
	}
	dataPlane, err = r.components.NewDataPlane(runCtx, signaling)
	if err != nil {
		return fail("create RTC peer/data path", err)
	}
	if dataPlane == nil {
		return fail("create RTC peer/data path", ErrSessionRTCDataPlaneUnavailable)
	}

	if r.components.OpenMediaSource == nil {
		return fail("open media source", ErrSessionRTCRuntimeUnavailable)
	}
	media, err = r.components.OpenMediaSource(runCtx, r.selection.MediaSource)
	if err != nil {
		return fail("open media source", err)
	}
	if media == nil {
		return fail("open media source", ErrSessionRTCRuntimeUnavailable)
	}

	if err := dataPlane.AttachInboundMedia(runCtx, media); err != nil {
		return fail("attach media source", err)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fail("start", ErrSessionRTCRuntimeClosed)
	}
	r.signaling = signaling
	r.dataPlane = dataPlane
	r.media = media
	r.started = true
	r.cancel = cancel
	r.mu.Unlock()
	return dataPlane, nil
}

func (r *sessionComposedRTCRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		cancel := r.cancel
		startDone := r.startDone
		media, dataPlane, signaling := r.media, r.dataPlane, r.signaling
		r.media = nil
		r.dataPlane = nil
		r.signaling = nil
		r.cancel = nil
		r.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if startDone != nil {
			<-startDone
		}

		// Start acquires resources in signaling → data-plane → media order;
		// close in reverse ownership order so a blocked attachment can observe
		// its source closing before its peer and signaling owners disappear.
		r.closeErr = closeSessionRTCResources(media, dataPlane, signaling)
	})
	return r.closeErr
}

func closeSessionRTCResources(media rtc.InboundMedia, dataPlane SessionRTCDataPlane, signaling rtc.Signaling) error {
	return errors.Join(
		closeSessionRTCResource(media),
		closeSessionRTCResource(dataPlane),
		closeSessionRTCResource(signaling),
	)
}

func closeSessionRTCResource(resource interface{ Close() error }) error {
	if resource == nil {
		return nil
	}
	return resource.Close()
}

// sessionRTCRuntimeInferencer starts the RTC owner before the provider
// inferencer connects. Its wrapper closes the RTC owner with the provider
// session and keeps the public messages.Session contract unchanged.
type sessionRTCRuntimeInferencer struct {
	inner   messages.SessionInferencer
	runtime SessionRTCRuntime

	mu      sync.Mutex
	session *sessionRTCRuntimeSession
}

var _ messages.SessionInferencer = (*sessionRTCRuntimeInferencer)(nil)

func (i *sessionRTCRuntimeInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.runtime == nil {
		return nil, wrapSessionRTCRuntimeError("start", ErrSessionRTCRuntimeUnavailable)
	}
	if _, err := i.runtime.Start(ctx); err != nil {
		return nil, err
	}
	if i.inner == nil {
		_ = i.runtime.Close()
		return nil, wrapSessionRTCRuntimeError("connect provider session", ErrSessionRTCRuntimeUnavailable)
	}
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, errors.Join(wrapSessionRTCRuntimeError("connect provider session", err), i.runtime.Close())
	}
	if session == nil {
		closeErr := i.runtime.Close()
		return nil, errors.Join(wrapSessionRTCRuntimeError("connect provider session", ErrSessionRTCRuntimeUnavailable), closeErr)
	}
	wrapped := &sessionRTCRuntimeSession{Session: session, runtime: i.runtime}
	i.mu.Lock()
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

// CloseSession synchronously closes the provider session established by the
// inferencer. It is idempotent with the model runner's deferred session close
// and lets the service observe provider transport cleanup before returning.
func (i *sessionRTCRuntimeInferencer) CloseSession() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

type sessionRTCRuntimeSession struct {
	messages.Session
	runtime   SessionRTCRuntime
	closeOnce sync.Once
	closeErr  error
}

var _ messages.Session = (*sessionRTCRuntimeSession)(nil)

func (s *sessionRTCRuntimeSession) TerminalError() error {
	if s == nil {
		return nil
	}
	return terminalSessionError(s.Session)
}

func (s *sessionRTCRuntimeSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionRTCRuntimeSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionRTCRuntimeSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *sessionRTCRuntimeSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *sessionRTCRuntimeSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(s.Session.Close(), s.runtime.Close())
	})
	return s.closeErr
}

// sessionRTCLazyDialer adapts a started RTC runtime to the existing provider
// transport.Dialer seam. It is only used after sessionRTCRuntimeInferencer has
// completed runtime startup, but retaining the start call makes the adapter
// safe for provider implementations that connect through a different path.
type sessionRTCLazyDialer struct {
	runtime SessionRTCRuntime
}

var _ transport.Dialer = (*sessionRTCLazyDialer)(nil)

func (d *sessionRTCLazyDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.runtime == nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", ErrSessionRTCRuntimeUnavailable)
	}
	dataPlane, err := d.runtime.Start(context.Background())
	if err != nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", err)
	}
	if dataPlane == nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", ErrSessionRTCDataPlaneUnavailable)
	}
	conn, err := dataPlane.Dial(endpoint, headers)
	if err != nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", err)
	}
	if conn == nil {
		return nil, wrapSessionRTCRuntimeError("dial RTC data path", rtc.ErrNilConnection)
	}
	return conn, nil
}
