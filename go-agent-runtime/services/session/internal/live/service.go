package live

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/observations"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"sync"
	"time"
)

const (
	defaultEventCapacity        = 128
	minimumEventCapacity        = 4
	maxPendingToolCallResponses = 128 // bounded by the admitted event window
	deferredImageOpeningPrompt  = "Use the attached image to answer the user's next spoken question."
)
const defaultSessionUpdatedTimeout = 30 * time.Second
const defaultPlaybackDrainTimeout = 5 * time.Second

type mediaRequirements struct{ inbound, outbound bool }
type replayVirtualPlaybackController struct{}

func (replayVirtualPlaybackController) StartPlayback(sharedaudio.PlaybackResponse) {}
func (replayVirtualPlaybackController) InterruptPlayback(sharedaudio.PlaybackResponse) (int, bool) {
	return 0, true
}

var _ session.LiveService = (*Service)(nil)
var _ session.LiveRunner = (*Service)(nil)

type Dependencies struct {
	InferencerFactory session.LiveInferencerFactory
	CapabilityFactory session.LiveCapabilityFactory
	ToolExecutor      messages.ToolExecutor
	ToolDefinitions   []messages.ToolDefinition
	EventCapacity     int
	Clock             session.LiveClock
	Scheduler         platformclock.Scheduler
}
type Service struct {
	inferencerFactory session.LiveInferencerFactory
	capabilityFactory session.LiveCapabilityFactory
	toolExecutor      messages.ToolExecutor
	toolDefinitions   []messages.ToolDefinition
	eventCapacity     int
	clock             session.LiveClock
	scheduler         platformclock.Scheduler
}

func New(deps Dependencies) *Service {
	capacity := deps.EventCapacity
	if capacity < minimumEventCapacity {
		capacity = defaultEventCapacity
	}
	return &Service{
		inferencerFactory: deps.InferencerFactory,
		capabilityFactory: deps.CapabilityFactory,
		toolExecutor:      deps.ToolExecutor,
		toolDefinitions:   input.CloneToolDefinitions(deps.ToolDefinitions),
		eventCapacity:     capacity,
		clock:             deps.Clock,
		scheduler:         deps.Scheduler,
	}
}
func (s *Service) OpenLive(ctx context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if s == nil || s.inferencerFactory == nil {
		return nil, errors.New("live inferencer factory is required")
	}
	if ctx == nil {
		return nil, errors.New("live session context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request = input.CloneLiveRequest(request)
	h := newHandle(request, s.inferencerFactory, s.capabilityFactory, s.toolExecutor, s.toolDefinitions, s.eventCapacity, s.clock, s.scheduler)
	h.parentCtx = ctx
	return h, nil
}

type handle struct {
	request                                          session.LiveRequest
	factory                                          session.LiveInferencerFactory
	capabilityFactory                                session.LiveCapabilityFactory
	toolExecutor                                     messages.ToolExecutor
	toolDefinitions                                  []messages.ToolDefinition
	capabilityClose                                  func() error
	capabilityRefresh                                func(context.Context) ([]messages.ToolDefinition, error)
	capabilityWatch                                  func(context.Context) <-chan session.LiveCapabilityEvent
	captureFlush                                     func() error
	observer                                         *observations.Observer
	capabilityMu                                     sync.Mutex
	eventCapacity                                    int
	clock                                            session.LiveClock
	scheduler                                        platformclock.Scheduler
	media                                            *mediagate.Gate
	events                                           chan session.LiveEvent
	done                                             chan struct{}
	startDone                                        chan struct{}
	mu                                               sync.Mutex
	started, closed                                  bool
	startErr, terminalErr                            error
	runErr, providerErr                              error
	providerTerminalError                            func() error
	pumpErr                                          error
	cancel                                           context.CancelCauseFunc
	parentCtx                                        context.Context
	cancelRequested, gracefulStop                    bool
	cancelCause                                      error
	loop                                             *agentloop.AgentLoop
	captureComplete, responseStarted, responseActive bool
	activeResponseIDs                                map[string]struct{}
	anonymousResponses                               int
	responsePending                                  bool
	responseObserved                                 uint64
	responseStartWake                                chan struct{}
	replayResponses                                  int
	captureResponseTarget                            int
	replayResponseWake                               chan struct{}
	responseTerminalWake                             chan struct{}
	scheduledAudioCount                              int
	dispatchedAudioCount                             int
	captureTurnWake                                  chan struct{}
	activeScheduledAudio                             bool
	scheduledResponseBase                            int
	observedResponseTerminals                        int
	observedResponseIDs                              map[string]struct{}
	interruptedScheduledResponses                    int
	scheduledContinuationTerminals                   int
	pendingToolCalls                                 int
	pendingToolCallResponses                         map[string]string
	terminalValue                                    *messages.SessionCloseValue
	providerCloseObserved                            bool
	localCloseObserved                               bool
	userCancelled                                    bool
	outputObserved                                   bool
	runWG                                            sync.WaitGroup
	finishOnce                                       sync.Once
	startFinish                                      sync.Once
	eventMu                                          sync.Mutex
	eventsClosed                                     bool
	sequence                                         uint64
	dropped                                          uint64
	openingSent                                      bool
	openingReady                                     chan struct{}
	openingReadyOnce                                 sync.Once
	openingAdmissionErr                              error
	captureSourceActive                              bool
	mediaRequirements                                mediaRequirements
	replayReady                                      chan struct{}
	replayReadyOnce                                  sync.Once
	providerDoneSignal                               chan struct{}
	providerDoneOnce                                 sync.Once
	terminalObserved                                 chan struct{}
	terminalOnce                                     sync.Once
	policyMu                                         sync.Mutex
	sessionUpdatedOnce                               sync.Once
	sessionUpdatedSignal                             chan struct{}
	sessionUpdatedTimerReady                         chan platformclock.Timer
	sessionUpdatedTimerScheduled                     bool
	sessionUpdatedSeen                               bool
	firstTurnOnce                                    sync.Once
	firstTurnSignal                                  chan struct{}
	firstTurnTimerReady                              chan platformclock.Timer
	firstTurnTimerScheduled                          bool
	firstTurnSeen                                    bool
	retryRequests                                    chan retryRequest
	retryMu                                          sync.Mutex
	retriesUsed                                      int
	livenessMu                                       sync.Mutex
	livenessTimer                                    platformclock.Timer
	livenessGeneration                               uint64
	livenessArmed                                    bool
	livenessStopped                                  bool
	livenessWake                                     chan struct{}
	livenessFailure                                  *session.LiveLivenessFailure
	livenessErr                                      error
	responseOutputSeen                               bool
	responseToolObligation                           bool
	toolMu                                           sync.Mutex
	toolContinuations                                map[string]*liveToolContinuation
	continuationErr                                  error
}

func (h *handle) mediaFailure(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	if h.cancelRequested && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		h.mu.Unlock()
		return
	}
	if h.pumpErr == nil {
		h.pumpErr = err
	}
	providerInbound := errors.Is(err, mediagate.ErrProviderInboundMedia)
	h.mu.Unlock()
	if providerInbound {
		return
	}
	h.Cancel(err)
}
func (h *handle) setRecorder(recorder session.LiveRecorder) {
	if h == nil {
		return
	}
	var observer *observations.Observer
	if recorder != nil {
		observer = observations.New(recorder, h.evidenceContext, h.clock, h.request.InputAudioSampleRate, h.request.OutputAudioSampleRate)
	}
	h.mu.Lock()
	h.observer = observer
	h.mu.Unlock()
	if h.media != nil {
		h.media.SetFrameObserver(func(direction mediagate.FrameDirection, frame sharedaudio.PCMFrame) {
			observer.Frame(direction == mediagate.FrameOutbound, frame)
		})
	}
}
func (h *handle) observationPort() *observations.Observer {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.observer
}
func (h *handle) now() time.Time {
	if h == nil || h.clock == nil {
		return time.Time{}
	}
	return h.clock()
}
func (h *handle) evidenceContext() context.Context {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	parent := h.parentCtx
	h.mu.Unlock()
	return context.WithoutCancel(parent)
}
func (h *handle) recorderError() error                    { return h.observationPort().Error() }
func (h *handle) recordMessage(record session.LiveRecord) { h.observationPort().Message(record) }
func (h *handle) recordEvent(event session.LiveEvent)     { h.observationPort().Event(event) }
func newHandle(request session.LiveRequest, factory session.LiveInferencerFactory, capabilityFactory session.LiveCapabilityFactory, executor messages.ToolExecutor, definitions []messages.ToolDefinition, eventCapacity int, clock session.LiveClock, scheduler platformclock.Scheduler) *handle {
	h := &handle{
		request:                  request,
		factory:                  terminalDrainFactory(factory),
		capabilityFactory:        capabilityFactory,
		toolExecutor:             executor,
		toolDefinitions:          input.CloneToolDefinitions(definitions),
		eventCapacity:            eventCapacity,
		clock:                    clock,
		scheduler:                scheduler,
		events:                   make(chan session.LiveEvent, eventCapacity),
		done:                     make(chan struct{}),
		startDone:                make(chan struct{}),
		sessionUpdatedSignal:     make(chan struct{}),
		sessionUpdatedTimerReady: make(chan platformclock.Timer, 1),
		firstTurnSignal:          make(chan struct{}),
		firstTurnTimerReady:      make(chan platformclock.Timer, 1),
		retryRequests:            make(chan retryRequest, 1),
		replayResponseWake:       make(chan struct{}),
		responseTerminalWake:     make(chan struct{}),
		responseStartWake:        make(chan struct{}),
		captureTurnWake:          make(chan struct{}),
		replayReady:              make(chan struct{}),
		openingReady:             make(chan struct{}),
		providerDoneSignal:       make(chan struct{}),
		terminalObserved:         make(chan struct{}),
		livenessWake:             make(chan struct{}, 1),
		toolContinuations:        make(map[string]*liveToolContinuation),
		pendingToolCallResponses: make(map[string]string),
		activeResponseIDs:        make(map[string]struct{}),
	}
	h.media = mediagate.New(h.mediaFailure)
	return h
}
func (h *handle) Media() sharedaudio.MediaEndpoints {
	if h == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return h.media.Endpoints()
}
func (h *handle) Events() <-chan session.LiveEvent {
	if h == nil {
		return nil
	}
	return h.events
}
func (h *handle) Start(ctx context.Context) error {
	if h == nil {
		return session.ErrLiveClosed
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return session.ErrLiveClosed
	}
	if h.started {
		h.mu.Unlock()
		return errors.New("live session has already been started")
	}
	if ctx == nil {
		h.mu.Unlock()
		return errors.New("live session context is required")
	}
	h.started = true
	h.parentCtx = ctx
	runCtx, cancel := context.WithCancelCause(ctx)
	h.cancel = cancel
	if h.cancelCause != nil {
		cancel(h.cancelCause)
	}
	h.mu.Unlock()
	return h.start(runCtx)
}
func (h *handle) configureScheduledAudio(scheduled, responseBase int) {
	if h == nil || scheduled <= 0 {
		return
	}
	h.mu.Lock()
	h.scheduledAudioCount = scheduled
	if responseBase > 0 {
		h.scheduledResponseBase = responseBase
	}
	h.mu.Unlock()
}
func terminalDrainFactory(factory session.LiveInferencerFactory) session.LiveInferencerFactory {
	return func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		inner, err := factory(ctx, request)
		if err != nil || inner == nil {
			return inner, err
		}
		return terminalDrainInferencer{inner: inner}, nil
	}
}

type terminalDrainInferencer struct{ inner messages.SessionInferencer }

func (i terminalDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil || s == nil {
		return s, err
	}
	source := s.Receive()
	capacity := defaultEventCapacity
	if source != nil && source.Cap() > 0 {
		capacity = source.Cap()
	}
	d := &terminalDrainSession{inner: s, receive: messages.NewTypedBuffer[messages.StreamMessage](capacity), done: make(chan struct{}), stop: make(chan struct{})}
	go d.forward(context.WithoutCancel(ctx), source, s.Done())
	return d, nil
}
func (i terminalDrainInferencer) FlushCapture() error {
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		return flusher.FlushCapture()
	}
	return nil
}
func (s *terminalDrainSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if requester, ok := s.inner.(messages.SessionResponseRequester); ok {
		return requester.RequestResponse(ctx)
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}
func (s *terminalDrainSession) SupportsResponseRequests() bool {
	if capability, ok := s.inner.(messages.SessionResponseCapability); ok {
		return capability.SupportsResponseRequests()
	}
	_, ok := s.inner.(messages.SessionResponseRequester)
	return ok
}
func (s *terminalDrainSession) FlushOutbound(ctx context.Context) error {
	if flusher, ok := s.inner.(messages.SessionOutboundFlusher); ok {
		return flusher.FlushOutbound(ctx)
	}
	return nil
}
func (s *terminalDrainSession) InitialSessionConfigSent() bool {
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}
func (s *terminalDrainSession) TerminalError() error {
	provider, ok := s.inner.(interface{ TerminalError() error })
	if !ok {
		return nil
	}
	return provider.TerminalError()
}
