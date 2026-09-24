// Package publication implements session-owned dynamic tool publication: one
// independent browser watch whose catalog refreshes are serialized into full
// provider definition replacements.
package publication

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const errNilWatch sessionturn.Error = "broker watch returned a nil event channel"

// target is one ordered publication candidate.
type target struct {
	browserID  string
	targetID   string
	generation uint64
	sequence   uint64
}

func (t target) sameTarget(other target) bool {
	return t.browserID == other.browserID && t.targetID == other.targetID
}

// Publisher owns one session's publication state. It never owns the broker
// and never creates a second consumer of the host's event stream.
type Publisher struct {
	base    []messages.ToolDefinition
	watch   func(context.Context) <-chan sessionturn.BrowserEvent
	refresh func(context.Context) ([]messages.ToolDefinition, error)
	publish func(context.Context, []messages.ToolDefinition) error
	timers  sessionturn.TimerFactory

	ready     chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	errCh     chan error

	mu         sync.Mutex
	state      sessionturn.PublicationState
	pending    target
	hasPending bool
	started    bool
	cancel     context.CancelFunc
}

var _ sessionturn.Publication = (*Publisher)(nil)

// New creates an unstarted publisher. The initial surface always retains the
// static base definitions.
func New(request sessionturn.PublicationRequest) *Publisher {
	timers := request.TimerFactory
	if timers == nil {
		timers = wallTimers{}
	}
	base := messages.CanonicalToolDefinitions(request.StaticStableDefinitions)
	initial := messages.CanonicalToolDefinitions(request.InitialDefinitions)
	if len(initial) == 0 {
		initial = append([]messages.ToolDefinition(nil), base...)
	}
	initial = Merge(base, initial)
	digest, err := Digest(initial)
	if err != nil {
		// An unencodable initial surface cannot match any refreshed digest.
		digest = ""
	}
	return &Publisher{
		base:    append([]messages.ToolDefinition(nil), base...),
		watch:   request.Watch,
		refresh: request.Refresh,
		publish: request.Publish,
		timers:  timers,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
		errCh:   make(chan error, 1),
		state: sessionturn.PublicationState{
			StaticStableDefinitions:   append([]messages.ToolDefinition(nil), base...),
			LastSuccessfulDefinitions: append([]messages.ToolDefinition(nil), initial...),
			LastSuccessfulDigest:      digest,
			Lifecycle:                 sessionturn.PublicationCreated,
		},
	}
}

// Start begins watching before the session is ready. Events observed during
// the provider handshake are reconciled by the first refresh after readiness.
func (p *Publisher) Start(parent context.Context) {
	if parent == nil {
		parent = context.Background() //nolint:contextcheck // a nil parent is the documented background default
	}
	p.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		p.mu.Lock()
		p.started = true
		p.cancel = cancel
		p.mu.Unlock()
		events := p.watch(ctx)
		if events == nil {
			p.report("watch", 0, errNilWatch)
			close(p.done)
			return
		}
		go newRunner(p, events).run(ctx)
	})
}

// MarkSessionReady releases publication after the provider's configuration
// boundary, so a dynamic update cannot overtake the initial SESSION.UPDATE.
func (p *Publisher) MarkSessionReady() {
	p.readyOnce.Do(func() { close(p.ready) })
}

// Errors reports the first publication failure.
func (p *Publisher) Errors() <-chan error { return p.errCh }

// Stop cancels the watch and waits for the run loop to exit.
func (p *Publisher) Stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		started, cancel := p.started, p.cancel
		p.mu.Unlock()
		if !started {
			p.setLifecycle(sessionturn.PublicationStopped)
			return
		}
		if cancel != nil {
			cancel()
		}
		<-p.done
	})
}

// State returns an independent snapshot.
func (p *Publisher) State() sessionturn.PublicationState {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state
	state.StaticStableDefinitions = messages.CanonicalToolDefinitions(state.StaticStableDefinitions)
	state.LastSuccessfulDefinitions = messages.CanonicalToolDefinitions(state.LastSuccessfulDefinitions)
	return state
}

func (p *Publisher) setLifecycle(lifecycle sessionturn.PublicationLifecycle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Lifecycle == sessionturn.PublicationFailed {
		return
	}
	p.state.Lifecycle = lifecycle
}

func (p *Publisher) latestEventSequence() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.LatestEventSequence
}

// fail records the first failure and returns the recorded error, so a later
// failure returns the first one.
func (p *Publisher) fail(phase string, sequence uint64, err error) error {
	p.report(phase, sequence, err)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Err
}

// report records and reports only the first failure.
func (p *Publisher) report(phase string, sequence uint64, err error) {
	if err == nil {
		err = errUnknownFailure
	}
	publicationErr := &sessionturn.PublicationError{Phase: phase, Sequence: sequence, Err: err}
	p.mu.Lock()
	if p.state.Lifecycle == sessionturn.PublicationFailed {
		p.mu.Unlock()
		return
	}
	p.state.Lifecycle = sessionturn.PublicationFailed
	p.state.Err = publicationErr
	p.mu.Unlock()
	select {
	case p.errCh <- publicationErr:
	default:
	}
}

const errUnknownFailure sessionturn.Error = "unknown publication failure"

// Inert is the publication of a session without dynamic tools.
type Inert struct{}

var _ sessionturn.Publication = Inert{}

func (Inert) MarkSessionReady()                   {}
func (Inert) Errors() <-chan error                { return nil }
func (Inert) State() sessionturn.PublicationState { return sessionturn.PublicationState{} }
func (Inert) Stop()                               {}
