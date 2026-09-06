// Package live owns the continuous duplex session implementation. The package
// is deliberately private to the session service; hosts see only the narrow
// LiveService/LiveHandle contracts in services/session.
package live

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/observations"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const defaultEventCapacity = 128
const minimumEventCapacity = 4

const defaultSessionUpdatedTimeout = 30 * time.Second

const defaultPlaybackDrainTimeout = 5 * time.Second

var _ session.LiveService = (*Service)(nil)
var _ session.LiveRunner = (*Service)(nil)

// Dependencies are the explicit provider and tool edges for one live service.
// The inferencer factory is intentionally called only by Start, keeping
// construction inert for embedders that create services during application
// setup or dependency graph validation.
type Dependencies struct {
	InferencerFactory session.LiveInferencerFactory
	CapabilityFactory session.LiveCapabilityFactory
	ToolExecutor      messages.ToolExecutor
	ToolDefinitions   []messages.ToolDefinition
	EventCapacity     int
	Clock             session.LiveClock
	Scheduler         platformclock.Scheduler
}

// Service implements session.LiveService.
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

// OpenLive validates only the service edge and allocates bounded local ports.
// It does not call the provider factory, open a socket, read a replay path, or
// create device resources.
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
	request           session.LiveRequest
	factory           session.LiveInferencerFactory
	capabilityFactory session.LiveCapabilityFactory
	toolExecutor      messages.ToolExecutor
	toolDefinitions   []messages.ToolDefinition
	capabilityClose   func() error
	capabilityRefresh func(context.Context) ([]messages.ToolDefinition, error)
	capabilityWatch   func(context.Context) <-chan session.LiveCapabilityEvent
	captureFlush      func() error
	observer          *observations.Observer
	capabilityMu      sync.Mutex
	eventCapacity     int
	clock             session.LiveClock
	scheduler         platformclock.Scheduler
	media             *mediagate.Gate
	events            chan session.LiveEvent
	done              chan struct{}
	startDone         chan struct{}

	mu              sync.Mutex
	started         bool
	closed          bool
	startErr        error
	terminalErr     error
	runErr          error
	providerErr     error
	pumpErr         error
	cancel          context.CancelCauseFunc
	parentCtx       context.Context
	cancelRequested bool
	cancelCause     error
	gracefulStop    bool
	loop            *agentloop.AgentLoop
	captureComplete bool
	responseStarted bool
	responseActive  bool
	// responsePending records a client-owned response boundary that has been
	// admitted to the provider session but has not produced its first inbound
	// lifecycle event yet. Realtime adapters commonly queue the
	// input_audio_buffer.commit/response.create pair asynchronously; keeping
	// this separate from responseActive lets a following finite turn reserve
	// cancellation without treating every queued response as an already
	// streaming response after the boundary has settled.
	responsePending    bool
	responseObserved   uint64
	responseStartWake  chan struct{}
	replayResponses    int
	replayResponseWake chan struct{}
	pendingToolCalls   int
	terminalValue      *messages.SessionCloseValue

	runWG       sync.WaitGroup
	finishOnce  sync.Once
	startFinish sync.Once

	eventMu         sync.Mutex
	eventsClosed    bool
	sequence        uint64
	dropped         uint64
	openingSent     bool
	replayReady     chan struct{}
	replayReadyOnce sync.Once
	// providerDone is raised by the provider-session adapter after its
	// transport closes. terminalObserved is raised when the model runner has
	// forwarded the corresponding SessionClose boundary. Keeping these as
	// separate signals prevents a transport close from cancelling the loop
	// before its final provider metadata is visible to the runtime.
	providerDoneSignal chan struct{}
	providerDoneOnce   sync.Once
	terminalObserved   chan struct{}
	terminalOnce       sync.Once

	// Timing policy state is kept separate from the general lifecycle mutex so
	// SESSION.OPEN handling never waits on a scheduler or provider operation.
	policyMu                     sync.Mutex
	sessionUpdatedOnce           sync.Once
	sessionUpdatedSignal         chan struct{}
	sessionUpdatedTimerReady     chan platformclock.Timer
	sessionUpdatedTimerScheduled bool
	sessionUpdatedSeen           bool
	firstTurnOnce                sync.Once
	firstTurnSignal              chan struct{}
	firstTurnTimerReady          chan platformclock.Timer
	firstTurnTimerScheduled      bool
	firstTurnSeen                bool
	retryRequests                chan retryRequest
	retryMu                      sync.Mutex
	retriesUsed                  int
	livenessMu                   sync.Mutex
	livenessTimer                platformclock.Timer
	livenessGeneration           uint64
	livenessArmed                bool
	livenessStopped              bool
	livenessWake                 chan struct{}
	livenessFailure              *session.LiveLivenessFailure
	livenessErr                  error
	responseOutputSeen           bool
	responseToolObligation       bool
	// Tool continuation state is updated by both provider observation and the
	// model runner's provider-admission callbacks. It has its own lock because
	// those paths run on different workers; holding the lifecycle mutex while
	// waiting for provider I/O would make a failed continuation impossible to
	// report during teardown.
	toolMu            sync.Mutex
	toolContinuations map[string]*liveToolContinuation
	continuationErr   error
}

func (h *handle) mediaFailure(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	// Closing a live handle cancels the media bridge deliberately. The
	// resulting context cancellation is a teardown signal, not a new terminal
	// cause and must not erase the caller's explicit cancellation error.
	if h.cancelRequested && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		h.mu.Unlock()
		return
	}
	if h.pumpErr == nil {
		h.pumpErr = err
	}
	h.mu.Unlock()
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

func (h *handle) setProviderMediaAttached(attached bool) {
	h.observationPort().SetMediaAttached(attached)
}

func (h *handle) now() time.Time {
	if h == nil || h.clock == nil {
		return time.Time{}
	}
	return h.clock()
}

// evidenceContext retains invocation values while deliberately removing the
// caller's cancellation. Recording is a lifecycle join: terminal and late
// media observations must still reach the bounded recorder after the provider
// cancellation path has fired. Finalize receives the same policy from the
// invocation owner.
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
		factory:                  factory,
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
		responseStartWake:        make(chan struct{}),
		replayReady:              make(chan struct{}),
		providerDoneSignal:       make(chan struct{}),
		terminalObserved:         make(chan struct{}),
		livenessWake:             make(chan struct{}, 1),
		toolContinuations:        make(map[string]*liveToolContinuation),
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
