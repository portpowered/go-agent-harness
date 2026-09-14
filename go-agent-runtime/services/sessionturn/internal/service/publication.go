package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	publicationSettleWindow = 10 * time.Millisecond
	publicationStopTimeout  = 500 * time.Millisecond
)

type publication struct {
	base, initial []messages.ToolDefinition
	watch         func(context.Context) <-chan sessionturn.BrowserEvent
	refresh       func(context.Context) ([]messages.ToolDefinition, error)
	timerFactory  sessionturn.TimerFactory
	publish       func(context.Context, []messages.ToolDefinition) error
	ready         chan struct{}
	readyOnce     sync.Once
	stopOnce      sync.Once
	done          chan struct{}
	errors        chan error
	cancel        context.CancelFunc

	mu         sync.Mutex
	state      sessionturn.PublicationState
	pending    sessionturn.BrowserEvent
	hasPending bool
}

type wallTimerFactory struct{}

func (wallTimerFactory) NewTimer(d time.Duration) sessionturn.Timer {
	return wallTimer{time.NewTimer(d)}
}

type wallTimer struct{ timer *time.Timer }

func (t wallTimer) C() <-chan time.Time        { return t.timer.C }
func (t wallTimer) Stop() bool                 { return t.timer.Stop() }
func (t wallTimer) Reset(d time.Duration) bool { return t.timer.Reset(d) }

func startPublication(parent context.Context, request sessionturn.PublicationRequest) (sessionturn.Publication, error) {
	if request.Browser.Watch == nil || request.Browser.Refresh == nil || request.Publish == nil {
		return nil, errors.New("session turn publication requires watch, refresh, and publish callbacks")
	}
	if parent == nil {
		return nil, errors.New("session turn publication requires a non-nil context")
	}
	base := messages.CanonicalToolDefinitions(request.BaseDefinitions)
	initial := messages.CanonicalToolDefinitions(request.InitialDefinitions)
	if len(initial) == 0 {
		initial = append([]messages.ToolDefinition(nil), base...)
	}
	initial = mergeDefinitions(base, initial)
	digest, err := definitionDigest(initial)
	if err != nil {
		return nil, err
	}
	timerFactory := request.Browser.TimerFactory
	if timerFactory == nil {
		timerFactory = wallTimerFactory{}
	}
	ctx, cancel := context.WithCancel(parent)
	p := &publication{base: base, initial: initial, watch: request.Browser.Watch, refresh: request.Browser.Refresh, timerFactory: timerFactory, publish: request.Publish, ready: make(chan struct{}), done: make(chan struct{}), errors: make(chan error, 1), cancel: cancel, state: sessionturn.PublicationState{Lifecycle: sessionturn.PublicationStarting, DefinitionDigest: digest}}
	events := p.watch(ctx)
	if events == nil {
		cancel()
		return nil, errors.New("session turn browser watch returned a nil channel")
	}
	go p.run(ctx, events)
	return p, nil
}

func (p *publication) MarkReady() {
	if p != nil {
		p.readyOnce.Do(func() { close(p.ready) })
	}
}
func (p *publication) Errors() <-chan error {
	if p == nil {
		return nil
	}
	return p.errors
}
func (p *publication) State() sessionturn.PublicationState {
	if p == nil {
		return sessionturn.PublicationState{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}
func (p *publication) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.cancel()
		timer := time.NewTimer(publicationStopTimeout)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			if err := p.fail("stop", p.latestSequence(), context.DeadlineExceeded); err != nil {
				return
			}
		}
	})
}

func (p *publication) run(ctx context.Context, events <-chan sessionturn.BrowserEvent) {
	loop := &publicationLoop{ready: p.ready, events: events, timerFactory: p.timerFactory}
	defer close(p.done)
	defer loop.stopTimer()
	for p.runIteration(ctx, loop) {
	}
}

type publicationLoop struct {
	ready        <-chan struct{}
	events       <-chan sessionturn.BrowserEvent
	timerFactory sessionturn.TimerFactory
	timer        sessionturn.Timer
	timerC       <-chan time.Time
	pending      bool
}

func (p *publication) runIteration(ctx context.Context, loop *publicationLoop) bool {
	select {
	case <-ctx.Done():
		p.lifecycle(sessionturn.PublicationStopped)
		return false
	case <-loop.ready:
		return p.handleReady(ctx, loop)
	case event, ok := <-loop.events:
		return p.handleEvent(loop, event, ok)
	case <-loop.timerC:
		return p.handleTimer(ctx, loop)
	}
}

func (p *publication) handleReady(ctx context.Context, loop *publicationLoop) bool {
	loop.ready = nil
	p.lifecycle(sessionturn.PublicationReady)
	if !p.drainEvents(loop) {
		return false
	}
	loop.pending = false
	return p.refreshAndPublish(ctx, "session_ready") == nil
}

func (p *publication) handleEvent(loop *publicationLoop, event sessionturn.BrowserEvent, ok bool) bool {
	if !ok {
		p.lifecycle(sessionturn.PublicationStopped)
		return false
	}
	if !p.consume(event) || loop.ready != nil {
		return true
	}
	loop.pending = true
	loop.resetTimer()
	return true
}

func (p *publication) handleTimer(ctx context.Context, loop *publicationLoop) bool {
	loop.stopTimer()
	if !loop.pending {
		return true
	}
	if !p.drainEvents(loop) {
		return false
	}
	loop.pending = false
	return p.refreshAndPublish(ctx, "browser_event") == nil
}

func (p *publication) drainEvents(loop *publicationLoop) bool {
	for {
		select {
		case event, ok := <-loop.events:
			if !ok {
				p.lifecycle(sessionturn.PublicationStopped)
				return false
			}
			loop.pending = loop.pending || p.consume(event)
		default:
			return true
		}
	}
}

func (loop *publicationLoop) resetTimer() {
	if loop.timer == nil {
		loop.timer = loop.timerFactory.NewTimer(publicationSettleWindow)
	} else {
		loop.timer.Stop()
		loop.timer.Reset(publicationSettleWindow)
	}
	loop.timerC = loop.timer.C()
}

func (loop *publicationLoop) stopTimer() {
	if loop.timer == nil {
		return
	}
	loop.timer.Stop()
	loop.timerC = nil
}

func (p *publication) consume(event sessionturn.BrowserEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Sequence != 0 && event.Sequence <= p.state.LastSequence {
		return false
	}
	if event.Sequence > p.state.LastSequence {
		p.state.LastSequence = event.Sequence
	}
	switch event.Type {
	case sessionturn.BrowserEventSelectionChanged, sessionturn.BrowserEventCatalogChanged, sessionturn.BrowserEventGenerationChanged:
		if p.hasPending && sameTarget(event, p.pending) && event.Generation != 0 && p.pending.Generation != 0 && event.Generation < p.pending.Generation {
			return false
		}
		if event.Generation == 0 && p.hasPending && sameTarget(event, p.pending) {
			event.Generation = p.pending.Generation
		}
		p.pending, p.hasPending = event, true
		return true
	default:
		return false
	}
}

func (p *publication) refreshAndPublish(ctx context.Context, phase string) error {
	definitions, err := p.refresh(ctx)
	if err != nil {
		return p.fail(phase+"_refresh", p.latestSequence(), err)
	}
	canonical := mergeDefinitions(p.base, definitions)
	digest, err := definitionDigest(canonical)
	if err != nil {
		return p.fail(phase+"_digest", p.latestSequence(), err)
	}
	p.mu.Lock()
	unchanged := digest == p.state.DefinitionDigest
	event, hasEvent := p.pending, p.hasPending
	p.mu.Unlock()
	if !unchanged {
		if err := p.publish(ctx, canonical); err != nil {
			return p.fail(phase+"_publish", p.latestSequence(), err)
		}
		p.mu.Lock()
		p.state.DefinitionDigest = digest
		p.state.PublicationCount++
		p.mu.Unlock()
	}
	if hasEvent {
		p.mu.Lock()
		p.state.BrowserID, p.state.TargetID, p.state.Generation = event.BrowserID, event.TargetID, event.Generation
		p.hasPending = false
		p.mu.Unlock()
	}
	return nil
}

func (p *publication) latestSequence() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.LastSequence
}
func (p *publication) lifecycle(value sessionturn.PublicationLifecycle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Lifecycle != sessionturn.PublicationFailed {
		p.state.Lifecycle = value
	}
}
func (p *publication) fail(phase string, sequence uint64, err error) error {
	if err == nil {
		err = errors.New("unknown publication failure")
	}
	failure := &sessionturn.PublicationError{Phase: phase, Sequence: sequence, Err: err}
	p.mu.Lock()
	if p.state.Lifecycle == sessionturn.PublicationFailed {
		existing := p.state.Err
		p.mu.Unlock()
		return existing
	}
	p.state.Lifecycle = sessionturn.PublicationFailed
	p.state.Err = failure
	p.mu.Unlock()
	select {
	case p.errors <- failure:
	default:
	}
	return failure
}

func sameTarget(a, b sessionturn.BrowserEvent) bool {
	return a.BrowserID == b.BrowserID && a.TargetID == b.TargetID
}
func mergeDefinitions(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
	base = messages.CanonicalToolDefinitions(base)
	definitions = messages.CanonicalToolDefinitions(definitions)
	if len(base) == 0 {
		return definitions
	}
	merged := append([]messages.ToolDefinition(nil), base...)
	names := make(map[string]struct{}, len(base))
	for _, d := range base {
		names[d.Name] = struct{}{}
	}
	for _, d := range definitions {
		if _, ok := names[d.Name]; !ok {
			merged = append(merged, d)
		}
	}
	return messages.CanonicalToolDefinitions(merged)
}

type digestDefinition struct {
	Name, Description, ParameterSchema string
	Parameters                         []messages.ToolParameter
	ParametersClosed                   bool
}

func definitionDigest(definitions []messages.ToolDefinition) (string, error) {
	canonical := messages.CanonicalToolDefinitions(definitions)
	entries := make([]digestDefinition, 0, len(canonical))
	for _, d := range canonical {
		entries = append(entries, digestDefinition{Name: d.Name, Description: d.Description, Parameters: d.Parameters, ParameterSchema: string(d.ParameterSchema), ParametersClosed: d.ParametersClosed})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
