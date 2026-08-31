package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrSessionDynamicToolPublication identifies a failed live page-tool
// refresh or provider session.update delivery. The last successful surface is
// deliberately retained when this error is reported.
var ErrSessionDynamicToolPublication = errors.New("session dynamic tool publication failed")

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
	StaticStableDefinitions   []messages.ToolDefinition
	LastSuccessfulDefinitions []messages.ToolDefinition
	LastSuccessfulDigest      string
	LatestEventSequence       uint64
	Lifecycle                 SessionDynamicToolPublicationLifecycle
	Err                       error
	PublicationCount          uint64
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

	ready     chan struct{}
	readyOnce sync.Once
	startOnce sync.Once
	stopOnce  sync.Once
	done      chan struct{}
	errCh     chan error

	mu      sync.Mutex
	state   SessionDynamicToolPublicationState
	started bool
	cancel  context.CancelFunc
}

func newSessionDynamicToolPublisher(
	staticStableDefinitions []messages.ToolDefinition,
	initialDefinitions []messages.ToolDefinition,
	watch func(context.Context) <-chan webmcp.BrokerEvent,
	refresh func(context.Context) ([]messages.ToolDefinition, error),
) *sessionDynamicToolPublisher {
	if watch == nil || refresh == nil {
		return nil
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
	publisher := newSessionDynamicToolPublisher(
		opts.ToolDefinitionBase,
		opts.ToolDefinitions,
		opts.BrowserWatch,
		opts.RefreshToolDefinitions,
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
	for {
		select {
		case <-ctx.Done():
			p.setLifecycle(SessionDynamicToolPublicationStopped)
			return
		case <-ready:
			ready = nil
			p.setLifecycle(SessionDynamicToolPublicationReady)
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
			if err := p.refreshAndPublish(ctx, loop, "broker_event"); err != nil {
				return
			}
		}
	}
}

func (p *sessionDynamicToolPublisher) consumeEvent(event webmcp.BrokerEvent) bool {
	p.mu.Lock()
	if event.Sequence != 0 && event.Sequence <= p.state.LatestEventSequence {
		p.mu.Unlock()
		return false
	}
	if event.Sequence > p.state.LatestEventSequence {
		p.state.LatestEventSequence = event.Sequence
	}
	p.mu.Unlock()

	switch event.Type {
	case webmcp.BrokerEventSelected, webmcp.BrokerEventCatalogChanged, webmcp.BrokerEventGenerationChanged:
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
	p.mu.Unlock()
	if unchanged {
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

	p.mu.Lock()
	p.state.LastSuccessfulDefinitions = append([]messages.ToolDefinition(nil), canonical...)
	p.state.LastSuccessfulDigest = digest
	p.state.PublicationCount++
	p.mu.Unlock()
	return nil
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
