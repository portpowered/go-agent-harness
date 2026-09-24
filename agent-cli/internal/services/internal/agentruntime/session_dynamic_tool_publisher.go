package agentruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionDynamicToolPublicationError string

func (e sessionDynamicToolPublicationError) Error() string { return string(e) }

// ErrSessionDynamicToolPublication identifies a failed live page-tool
// refresh or provider session.update delivery. The last successful surface is
// deliberately retained when this error is reported.
const ErrSessionDynamicToolPublication sessionDynamicToolPublicationError = "session dynamic tool publication failed"

// sessionDynamicToolPublicationSettleWindow is long enough to collect the
// selection/generation/catalog notifications emitted by one browser change,
// while keeping a genuine later catalog change responsive. The publisher
// refreshes at the end of this window, rather than once per notification, so
// the provider sees the final surface for one effective change.
const sessionDynamicToolPublicationSettleWindow = 10 * time.Millisecond

// SessionDynamicToolPublicationLifecycle is the bounded lifecycle vocabulary
// retained by the session-owned dynamic publication controller.
type SessionDynamicToolPublicationLifecycle string

const (
	SessionDynamicToolPublicationCreated   SessionDynamicToolPublicationLifecycle = "created"
	SessionDynamicToolPublicationReady     SessionDynamicToolPublicationLifecycle = "ready"
	SessionDynamicToolPublicationFailed    SessionDynamicToolPublicationLifecycle = "failed"
	SessionDynamicToolPublicationStopped   SessionDynamicToolPublicationLifecycle = "stopped"
	SessionDynamicToolPublicationWatchGone SessionDynamicToolPublicationLifecycle = "watch_closed"
)

type sessionDynamicToolPublicationEvent struct {
	browserID  webmcp.BrowserID
	targetID   webmcp.TargetID
	generation uint64
	sequence   uint64
}

// sessionDynamicToolPublisher observes one independent broker watch and
// serializes catalog refreshes with full provider definition replacements.
// It is intentionally session-owned: no broker ownership or second browser
// event consumer is created here.
type sessionDynamicToolPublisher struct {
	staticStableDefinitions []messages.ToolDefinition
	initialDefinitions      []messages.ToolDefinition
	watch                   func(context.Context) <-chan webmcp.BrokerEvent
	refresh                 func(context.Context) ([]messages.ToolDefinition, error)
	timerFactory            webmcp.TimerFactory

	ready     chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	errCh     chan error

	mu         sync.Mutex
	state      SessionDynamicToolPublicationState
	pending    sessionDynamicToolPublicationEvent
	hasPending bool
	started    bool
	cancel     context.CancelFunc
}

//lint:ignore U1000 package tests exercise the default timer seam.
func newSessionDynamicToolPublisher(
	staticStableDefinitions []messages.ToolDefinition,
	initialDefinitions []messages.ToolDefinition,
	watch func(context.Context) <-chan webmcp.BrokerEvent,
	refresh func(context.Context) ([]messages.ToolDefinition, error),
) *sessionDynamicToolPublisher {
	return newSessionDynamicToolPublisherWithTimer(staticStableDefinitions, initialDefinitions, watch, refresh, nil)
}
func newSessionDynamicToolPublisherWithTimer(
	staticStableDefinitions []messages.ToolDefinition,
	initialDefinitions []messages.ToolDefinition,
	watch func(context.Context) <-chan webmcp.BrokerEvent,
	refresh func(context.Context) ([]messages.ToolDefinition, error),
	timerFactory webmcp.TimerFactory,
) *sessionDynamicToolPublisher {
	if watch == nil || refresh == nil {
		return nil
	}
	if timerFactory == nil {
		timerFactory = sessionDynamicToolPublicationWallTimerFactory{}
	}
	base := messages.CanonicalToolDefinitions(staticStableDefinitions)
	initial := messages.CanonicalToolDefinitions(initialDefinitions)
	if len(initial) == 0 {
		initial = append([]messages.ToolDefinition(nil), base...)
	}
	initial = mergeSessionToolDefinitionBase(base, initial)
	digest, _ := sessionToolDefinitionDigest(initial)
	return &sessionDynamicToolPublisher{
		staticStableDefinitions: append([]messages.ToolDefinition(nil), base...),
		initialDefinitions:      append([]messages.ToolDefinition(nil), initial...),
		watch:                   watch,
		refresh:                 refresh,
		timerFactory:            timerFactory,
		ready:                   make(chan struct{}),
		done:                    make(chan struct{}),
		errCh:                   make(chan error, 1),
		state: SessionDynamicToolPublicationState{
			StaticStableDefinitions:   append([]messages.ToolDefinition(nil), base...),
			LastSuccessfulDefinitions: append([]messages.ToolDefinition(nil), initial...),
			LastSuccessfulDigest:      digest,
			Lifecycle:                 SessionDynamicToolPublicationCreated,
		},
	}
}

func startSessionDynamicToolPublisher(parent context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) (*sessionDynamicToolPublisher, <-chan error) {
	publisher := newSessionDynamicToolPublisherWithTimer(
		opts.ToolDefinitionBase,
		opts.ToolDefinitions,
		opts.BrowserWatch,
		opts.RefreshToolDefinitions,
		opts.PublicationTimerFactory,
	)
	if publisher == nil {
		return nil, nil
	}
	publisher.start(parent, loop)
	return publisher, publisher.errors()
}

// start begins watching before the session is marked ready. Events that occur
// while the provider handshake is in flight are consumed and reconciled by the
// first refresh after SESSION.OPEN.
func (p *sessionDynamicToolPublisher) start(parent context.Context, loop *agentloop.AgentLoop) {
	if p == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	p.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		p.mu.Lock()
		p.started = true
		p.cancel = cancel
		p.mu.Unlock()
		events := p.watch(ctx)
		if events == nil {
			p.fail("watch", 0, errors.New("broker watch returned a nil event channel"))
			close(p.done)
			return
		}
		go p.run(ctx, loop, events)
	})
}

// markSessionReady releases publication after the provider's SESSION.CREATED
// boundary has been handled. The model runner sends the initial SESSION.UPDATE
// while handling that boundary, so dynamic publication cannot overtake the
// initial provider configuration. Events seen before readiness are still
// reconciled by the first refresh.
func (p *sessionDynamicToolPublisher) markSessionReady() {
	if p == nil {
		return
	}
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *sessionDynamicToolPublisher) errors() <-chan error {
	if p == nil {
		return nil
	}
	return p.errCh
}

func (p *sessionDynamicToolPublisher) MarkReady() { p.markSessionReady() }

func (p *sessionDynamicToolPublisher) Errors() <-chan error { return p.errors() }

func (p *sessionDynamicToolPublisher) Stop() { p.stop() }

func (p *sessionDynamicToolPublisher) stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.mu.Lock()
		started := p.started
		cancel := p.cancel
		p.mu.Unlock()
		if !started {
			p.setLifecycle(SessionDynamicToolPublicationStopped)
			return
		}
		if cancel != nil {
			cancel()
		}
		<-p.done
	})
}

//lint:ignore U1000 package tests inspect the publication snapshot seam.
func (p *sessionDynamicToolPublisher) stateSnapshot() SessionDynamicToolPublicationState {
	if p == nil {
		return SessionDynamicToolPublicationState{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state
	state.StaticStableDefinitions = messages.CanonicalToolDefinitions(state.StaticStableDefinitions)
	state.LastSuccessfulDefinitions = messages.CanonicalToolDefinitions(state.LastSuccessfulDefinitions)
	return state
}
func (p *sessionDynamicToolPublisher) consumeEvent(event webmcp.BrokerEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.acceptEventSequence(event.Sequence) {
		return false
	}
	if !isDynamicToolPublicationEvent(event.Type) {
		return false
	}
	candidate := publicationEventFromBroker(event)
	if p.hasStaleGeneration(candidate) {
		return false
	}
	p.pending = p.inheritPublicationGeneration(candidate)
	p.hasPending = true
	return true
}

func (p *sessionDynamicToolPublisher) acceptEventSequence(sequence uint64) bool {
	if sequence != 0 && sequence <= p.state.LatestEventSequence {
		return false
	}
	if sequence > p.state.LatestEventSequence {
		p.state.LatestEventSequence = sequence
	}
	return true
}

func (p *sessionDynamicToolPublisher) hasStaleGeneration(candidate sessionDynamicToolPublicationEvent) bool {
	if p.hasPending && samePublicationTarget(candidate, p.pending) &&
		isOlderPublicationGeneration(candidate.generation, p.pending.generation) {
		return true
	}
	lastSuccessful := sessionDynamicToolPublicationEvent{
		browserID: p.state.LastSuccessfulBrowserID, targetID: p.state.LastSuccessfulTargetID,
		generation: p.state.LastSuccessfulGeneration,
	}
	return samePublicationTarget(candidate, lastSuccessful) &&
		isOlderPublicationGeneration(candidate.generation, p.state.LastSuccessfulGeneration)
}

func isOlderPublicationGeneration(candidate, current uint64) bool {
	return candidate != 0 && current != 0 && candidate < current
}

func (p *sessionDynamicToolPublisher) inheritPublicationGeneration(candidate sessionDynamicToolPublicationEvent) sessionDynamicToolPublicationEvent {
	if candidate.generation != 0 {
		return candidate
	}
	if p.hasPending && samePublicationTarget(candidate, p.pending) {
		candidate.generation = p.pending.generation
		return candidate
	}
	lastSuccessful := sessionDynamicToolPublicationEvent{
		browserID: p.state.LastSuccessfulBrowserID, targetID: p.state.LastSuccessfulTargetID,
	}
	if samePublicationTarget(candidate, lastSuccessful) {
		candidate.generation = p.state.LastSuccessfulGeneration
	}
	return candidate
}

func (p *sessionDynamicToolPublisher) refreshAndPublish(ctx context.Context, loop *agentloop.AgentLoop, phase string) error {
	definitions, err := p.refresh(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+"_refresh", p.latestEventSequence(), err)
	}
	canonical := mergeSessionToolDefinitionBase(p.staticStableDefinitions, definitions)
	digest, err := sessionToolDefinitionDigest(canonical)
	if err != nil {
		return p.fail(phase+"_digest", p.latestEventSequence(), err)
	}

	p.mu.Lock()
	unchanged := digest == p.state.LastSuccessfulDigest
	pending := p.pending
	hasPending := p.hasPending
	p.mu.Unlock()
	if unchanged {
		p.commitSuccessfulPublication(pending, hasPending, canonical, digest, false)
		return nil
	}
	if loop == nil {
		return p.fail(phase+"_send", p.latestEventSequence(), errors.New("session agent loop is nil"))
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{
		Type: messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
			Tools: canonical,
		}),
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+"_send", p.latestEventSequence(), err)
	}

	p.commitSuccessfulPublication(pending, hasPending, canonical, digest, true)
	return nil
}

func (p *sessionDynamicToolPublisher) commitSuccessfulPublication(event sessionDynamicToolPublicationEvent, hasEvent bool, definitions []messages.ToolDefinition, digest string, delivered bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if delivered {
		p.state.LastSuccessfulDefinitions = append([]messages.ToolDefinition(nil), definitions...)
		p.state.LastSuccessfulDigest = digest
		p.state.PublicationCount++
		if hasEvent {
			p.state.LastSuccessfulBrowserID = event.browserID
			p.state.LastSuccessfulTargetID = event.targetID
			p.state.LastSuccessfulGeneration = event.generation
			p.state.LastSuccessfulEventSequence = event.sequence
		}
	}
	if hasEvent && p.hasPending && p.pending == event {
		// Clearing the work item is not a publication-state advance. It only
		// records that this unchanged or delivered event has been reconciled;
		// LastSuccessful* remains unchanged when no provider frame was needed.
		p.hasPending = false
	}
}

func samePublicationTarget(left, right sessionDynamicToolPublicationEvent) bool {
	return left.browserID == right.browserID && left.targetID == right.targetID
}

func (p *sessionDynamicToolPublisher) latestEventSequence() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.LatestEventSequence
}

func (p *sessionDynamicToolPublisher) setLifecycle(lifecycle SessionDynamicToolPublicationLifecycle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Lifecycle == SessionDynamicToolPublicationFailed {
		return
	}
	p.state.Lifecycle = lifecycle
}

func (p *sessionDynamicToolPublisher) fail(phase string, sequence uint64, err error) error {
	if err == nil {
		err = errors.New("unknown publication failure")
	}
	publicationErr := &SessionDynamicToolPublicationError{Phase: phase, Sequence: sequence, Err: err}
	p.mu.Lock()
	if p.state.Lifecycle == SessionDynamicToolPublicationFailed {
		existing := p.state.Err
		p.mu.Unlock()
		return existing
	}
	p.state.Lifecycle = SessionDynamicToolPublicationFailed
	p.state.Err = publicationErr
	p.mu.Unlock()
	select {
	case p.errCh <- publicationErr:
	default:
	}
	return publicationErr
}
