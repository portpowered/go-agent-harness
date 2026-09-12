package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
)

// Service is the private implementation behind the public toolpublication
// contract. It contains no browser, CLI, or agent-loop dependencies.
type Service struct {
	defaultTimerFactory toolpublication.TimerFactory
}

var _ toolpublication.Service = (*Service)(nil)

// NewService constructs the private service graph from an optional host timer
// factory. A nil factory uses the private wall-clock implementation.
func NewService(defaultTimerFactory toolpublication.TimerFactory) *Service {
	return &Service{defaultTimerFactory: defaultTimerFactory}
}

func (s *Service) NewPublisher(options toolpublication.Options) (toolpublication.Publisher, error) {
	if options.Watch == nil {
		return nil, fmt.Errorf("%w: event watch is nil", toolpublication.ErrInvalidOptions)
	}
	if options.Refresh == nil {
		return nil, fmt.Errorf("%w: definition refresher is nil", toolpublication.ErrInvalidOptions)
	}
	timerFactory := options.TimerFactory
	if timerFactory == nil {
		timerFactory = s.defaultTimerFactory
	}
	if timerFactory == nil {
		return nil, fmt.Errorf("%w: timer factory is nil", toolpublication.ErrInvalidOptions)
	}
	base := messages.CanonicalToolDefinitions(options.StaticStableDefinitions)
	initial := messages.CanonicalToolDefinitions(options.InitialDefinitions)
	if len(initial) == 0 {
		initial = append([]messages.ToolDefinition(nil), base...)
	}
	initial = mergeDefinitions(base, initial)
	digest, err := definitionDigest(initial)
	if err != nil {
		return nil, fmt.Errorf("%w: initial definition digest: %w", toolpublication.ErrInvalidOptions, err)
	}
	return &publisher{
		staticStableDefinitions: append([]messages.ToolDefinition(nil), base...),
		watch:                   options.Watch,
		refresh:                 options.Refresh,
		timerFactory:            timerFactory,
		ready:                   make(chan struct{}),
		done:                    make(chan struct{}),
		errCh:                   make(chan error, 1),
		state: toolpublication.State{
			StaticStableDefinitions:   append([]messages.ToolDefinition(nil), base...),
			LastSuccessfulDefinitions: append([]messages.ToolDefinition(nil), initial...),
			LastSuccessfulDigest:      digest,
			Lifecycle:                 toolpublication.LifecycleCreated,
		},
	}, nil
}

type publisher struct {
	staticStableDefinitions []messages.ToolDefinition
	watch                   toolpublication.EventWatch
	refresh                 toolpublication.DefinitionRefresher
	timerFactory            toolpublication.TimerFactory

	ready     chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	errCh     chan error

	mu         sync.Mutex
	state      toolpublication.State
	pending    publicationEvent
	hasPending bool
	started    bool
	cancel     context.CancelFunc
	sink       toolpublication.SessionUpdateSink
}

type publicationEvent struct {
	browserID  string
	targetID   string
	generation uint64
	sequence   uint64
}

func (p *publisher) Start(parent context.Context, sink toolpublication.SessionUpdateSink) {
	if p == nil {
		return
	}
	p.startOnce.Do(func() {
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		p.mu.Lock()
		p.started = true
		p.cancel = cancel
		p.sink = sink
		p.mu.Unlock()
		events := p.watch(ctx)
		if events == nil {
			if err := p.fail("watch", 0, errors.New("event watch returned a nil channel")); err != nil {
				close(p.done)
				return
			}
			return
		}
		go p.run(ctx, events)
	})
}

func (p *publisher) Ready() {
	if p == nil {
		return
	}
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *publisher) Errors() <-chan error {
	if p == nil {
		return nil
	}
	return p.errCh
}

func (p *publisher) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}

func (p *publisher) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.mu.Lock()
		started := p.started
		cancel := p.cancel
		p.mu.Unlock()
		if !started {
			p.setLifecycle(toolpublication.LifecycleStopped)
			return
		}
		if cancel != nil {
			cancel()
		}
		<-p.done
	})
}

func (p *publisher) Snapshot() toolpublication.State {
	if p == nil {
		return toolpublication.State{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state
	state.StaticStableDefinitions = messages.CanonicalToolDefinitions(state.StaticStableDefinitions)
	state.LastSuccessfulDefinitions = messages.CanonicalToolDefinitions(state.LastSuccessfulDefinitions)
	return state
}

func (p *publisher) refreshAndPublish(ctx context.Context, phase string) error {
	definitions, err := p.refresh(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+"_refresh", p.latestEventSequence(), err)
	}
	canonical := mergeDefinitions(p.staticStableDefinitions, definitions)
	digest, err := definitionDigest(canonical)
	if err != nil {
		return p.fail(phase+"_digest", p.latestEventSequence(), err)
	}
	p.mu.Lock()
	unchanged := digest == p.state.LastSuccessfulDigest
	pending := p.pending
	hasPending := p.hasPending
	sink := p.sink
	p.mu.Unlock()
	if unchanged {
		p.commitSuccessfulPublication(pending, hasPending, canonical, digest, false)
		return nil
	}
	if sink == nil {
		return p.fail(phase+"_send", p.latestEventSequence(), errors.New("session update sink is nil"))
	}
	if err := sink.SendSessionUpdate(ctx, canonical); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+"_send", p.latestEventSequence(), err)
	}
	p.commitSuccessfulPublication(pending, hasPending, canonical, digest, true)
	return nil
}

func (p *publisher) commitSuccessfulPublication(event publicationEvent, hasEvent bool, definitions []messages.ToolDefinition, digest string, delivered bool) {
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
		p.hasPending = false
	}
}

func sameTarget(left, right publicationEvent) bool {
	return left.browserID == right.browserID && left.targetID == right.targetID
}

func (p *publisher) latestEventSequence() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.LatestEventSequence
}

func (p *publisher) setLifecycle(lifecycle toolpublication.Lifecycle) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Lifecycle == toolpublication.LifecycleFailed {
		return
	}
	p.state.Lifecycle = lifecycle
}

func (p *publisher) fail(phase string, sequence uint64, err error) error {
	if err == nil {
		err = errors.New("unknown publication failure")
	}
	publicationErr := &toolpublication.PublicationError{Phase: phase, Sequence: sequence, Err: err}
	p.mu.Lock()
	if p.state.Lifecycle == toolpublication.LifecycleFailed {
		existing := p.state.Err
		p.mu.Unlock()
		return existing
	}
	p.state.Lifecycle = toolpublication.LifecycleFailed
	p.state.Err = publicationErr
	p.mu.Unlock()
	select {
	case p.errCh <- publicationErr:
	default:
	}
	return publicationErr
}

func mergeDefinitions(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
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

func definitionDigest(definitions []messages.ToolDefinition) (string, error) {
	canonical := messages.CanonicalToolDefinitions(definitions)
	payload := make([]digestEntry, 0, len(canonical))
	for _, definition := range canonical {
		payload = append(payload, digestEntry{
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

type digestEntry struct {
	Name             string                   `json:"name"`
	Description      string                   `json:"description"`
	Parameters       []messages.ToolParameter `json:"parameters"`
	ParameterSchema  string                   `json:"parameter_schema,omitempty"`
	ParametersClosed bool                     `json:"parameters_closed"`
}
