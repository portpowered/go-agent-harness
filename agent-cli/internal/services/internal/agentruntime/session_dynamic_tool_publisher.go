package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrSessionDynamicToolPublication identifies a failed live page-tool
// refresh or provider session.update delivery. The last successful surface is
// deliberately retained when this error is reported.
var ErrSessionDynamicToolPublication = errors.New("session dynamic tool publication failed")

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

// SessionDynamicToolPublicationState is a diagnostic snapshot for one live
// session. Definitions are copied on the way in and out so callers cannot
// mutate the state used by the publisher.
type SessionDynamicToolPublicationState struct {
	StaticStableDefinitions     []messages.ToolDefinition
	LastSuccessfulDefinitions   []messages.ToolDefinition
	LastSuccessfulDigest        string
	LastSuccessfulBrowserID     webmcp.BrowserID
	LastSuccessfulTargetID      webmcp.TargetID
	LastSuccessfulGeneration    uint64
	LastSuccessfulEventSequence uint64
	LatestEventSequence         uint64
	Lifecycle                   SessionDynamicToolPublicationLifecycle
	Err                         error
	PublicationCount            uint64
}

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

func (p *sessionDynamicToolPublisher) run(ctx context.Context, loop *agentloop.AgentLoop, events <-chan webmcp.BrokerEvent) {
	defer close(p.done)
	ready := p.ready
	var settleTimer webmcp.Timer
	var settleC <-chan time.Time
	pendingRefresh := false
	resetSettleTimer := func() {
		if settleTimer == nil {
			settleTimer = p.timerFactory.NewTimer(sessionDynamicToolPublicationSettleWindow)
		} else {
			if !settleTimer.Stop() {
				select {
				case <-settleTimer.C():
				default:
				}
			}
			settleTimer.Reset(sessionDynamicToolPublicationSettleWindow)
		}
		settleC = settleTimer.C()
	}
	stopSettleTimer := func() {
		if settleTimer == nil {
			return
		}
		if !settleTimer.Stop() {
			select {
			case <-settleTimer.C():
			default:
			}
		}
		settleC = nil
	}
	defer stopSettleTimer()

	drainEvents := func() bool {
		for {
			select {
			case event, ok := <-events:
				if !ok {
					p.setLifecycle(SessionDynamicToolPublicationWatchGone)
					return false
				}
				if p.consumeEvent(event) {
					pendingRefresh = true
				}
			default:
				return true
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			p.setLifecycle(SessionDynamicToolPublicationStopped)
			return
		case <-ready:
			ready = nil
			p.setLifecycle(SessionDynamicToolPublicationReady)
			// Events can arrive while the provider handshake is in flight. Drain
			// the already queued portion before taking the first snapshot so the
			// initial publication observes the latest catalog state.
			if !drainEvents() {
				return
			}
			pendingRefresh = false
			if err := p.refreshAndPublish(ctx, loop, "session_ready"); err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				p.setLifecycle(SessionDynamicToolPublicationWatchGone)
				return
			}
			if !p.consumeEvent(event) || ready != nil {
				continue
			}
			pendingRefresh = true
			resetSettleTimer()
		case <-settleC:
			stopSettleTimer()
			if !pendingRefresh {
				continue
			}
			// A buffered broker watch is a complete notification burst at this
			// boundary. Fold it in before reading the catalog so related
			// selection, generation, and catalog events publish only once.
			if !drainEvents() {
				return
			}
			pendingRefresh = false
			if err := p.refreshAndPublish(ctx, loop, "broker_event"); err != nil {
				return
			}
		}
	}
}

type sessionDynamicToolPublicationWallTimerFactory struct{}

func (sessionDynamicToolPublicationWallTimerFactory) NewTimer(duration time.Duration) webmcp.Timer {
	return sessionDynamicToolPublicationWallTimer{timer: time.NewTimer(duration)}
}

type sessionDynamicToolPublicationWallTimer struct {
	timer *time.Timer
}

func (t sessionDynamicToolPublicationWallTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t sessionDynamicToolPublicationWallTimer) Stop() bool {
	return t.timer.Stop()
}

func (t sessionDynamicToolPublicationWallTimer) Reset(duration time.Duration) bool {
	return t.timer.Reset(duration)
}

func (p *sessionDynamicToolPublisher) consumeEvent(event webmcp.BrokerEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Sequence != 0 && event.Sequence <= p.state.LatestEventSequence {
		return false
	}
	if event.Sequence > p.state.LatestEventSequence {
		p.state.LatestEventSequence = event.Sequence
	}

	switch event.Type {
	case webmcp.BrokerEventSelected, webmcp.BrokerEventCatalogChanged, webmcp.BrokerEventGenerationChanged:
		candidate := sessionDynamicToolPublicationEvent{
			browserID:  event.BrowserID,
			targetID:   event.TargetID,
			generation: event.Generation,
			sequence:   event.Sequence,
		}
		// Sequence is authoritative for ordering across different targets. A
		// generation never moves backwards for the same target, even when a
		// producer omitted its sequence while replaying a stale notification.
		if p.hasPending && samePublicationTarget(candidate, p.pending) &&
			candidate.generation != 0 && p.pending.generation != 0 && candidate.generation < p.pending.generation {
			return false
		}
		if samePublicationTarget(candidate, sessionDynamicToolPublicationEvent{
			browserID:  p.state.LastSuccessfulBrowserID,
			targetID:   p.state.LastSuccessfulTargetID,
			generation: p.state.LastSuccessfulGeneration,
		}) && candidate.generation != 0 && p.state.LastSuccessfulGeneration != 0 && candidate.generation < p.state.LastSuccessfulGeneration {
			return false
		}
		if candidate.generation == 0 {
			if p.hasPending && samePublicationTarget(candidate, p.pending) {
				candidate.generation = p.pending.generation
			} else if samePublicationTarget(candidate, sessionDynamicToolPublicationEvent{
				browserID: p.state.LastSuccessfulBrowserID,
				targetID:  p.state.LastSuccessfulTargetID,
			}) {
				candidate.generation = p.state.LastSuccessfulGeneration
			}
		}
		p.pending = candidate
		p.hasPending = true
		return true
	default:
		return false
	}
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

// SessionDynamicToolPublicationError contains only bounded phase and event
// metadata in its public text while retaining the underlying cause for tests
// and programmatic classification.
type SessionDynamicToolPublicationError struct {
	Phase    string
	Sequence uint64
	Err      error
}

func (e *SessionDynamicToolPublicationError) Error() string {
	if e == nil {
		return ErrSessionDynamicToolPublication.Error()
	}
	message := strings.TrimSpace(e.ErrString())
	if message == "" {
		message = "unknown error"
	}
	return fmt.Sprintf("%s: phase=%s sequence=%d: %s", ErrSessionDynamicToolPublication, e.Phase, e.Sequence, message)
}

func (e *SessionDynamicToolPublicationError) ErrString() string {
	if e == nil || e.Err == nil {
		return ""
	}
	message := strings.TrimSpace(e.Err.Error())
	const maxPublicationErrorText = 256
	if len(message) > maxPublicationErrorText {
		return message[:maxPublicationErrorText] + "..."
	}
	return message
}

func (e *SessionDynamicToolPublicationError) Unwrap() error {
	if e == nil {
		return ErrSessionDynamicToolPublication
	}
	return errors.Join(ErrSessionDynamicToolPublication, e.Err)
}

func mergeSessionToolDefinitionBase(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
	canonicalBase := messages.CanonicalToolDefinitions(base)
	canonicalDefinitions := messages.CanonicalToolDefinitions(definitions)
	if len(canonicalBase) == 0 {
		return canonicalDefinitions
	}
	merged := append([]messages.ToolDefinition(nil), canonicalBase...)
	baseNames := make(map[string]struct{}, len(canonicalBase))
	for _, definition := range canonicalBase {
		baseNames[definition.Name] = struct{}{}
	}
	for _, definition := range canonicalDefinitions {
		if _, isBase := baseNames[definition.Name]; isBase {
			continue
		}
		merged = append(merged, definition)
	}
	return messages.CanonicalToolDefinitions(merged)
}

func sessionToolDefinitionDigest(definitions []messages.ToolDefinition) (string, error) {
	canonical := messages.CanonicalToolDefinitions(definitions)
	payload := make([]sessionToolDefinitionDigestEntry, 0, len(canonical))
	for _, definition := range canonical {
		payload = append(payload, sessionToolDefinitionDigestEntry{
			Name:             definition.Name,
			Description:      definition.Description,
			Parameters:       definition.Parameters,
			ParameterSchema:  string(definition.ParameterSchema),
			ParametersClosed: definition.ParametersClosed,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type sessionToolDefinitionDigestEntry struct {
	Name             string                   `json:"name"`
	Description      string                   `json:"description"`
	Parameters       []messages.ToolParameter `json:"parameters"`
	ParameterSchema  string                   `json:"parameter_schema,omitempty"`
	ParametersClosed bool                     `json:"parameters_closed"`
}
