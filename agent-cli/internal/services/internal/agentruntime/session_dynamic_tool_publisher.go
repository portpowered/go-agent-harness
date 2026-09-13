package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
	toolpublicationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication/wire"
)

// ErrSessionDynamicToolPublication and the diagnostic aliases keep existing
// agentruntime callers source-compatible. Publication policy lives in the
// runtime service; this file only translates CLI transport seams.
var ErrSessionDynamicToolPublication = toolpublication.ErrSessionDynamicToolPublication

const sessionDynamicToolPublicationSettleWindow = toolpublication.SettleWindow

type SessionDynamicToolPublicationLifecycle = toolpublication.Lifecycle

const (
	SessionDynamicToolPublicationCreated   = toolpublication.LifecycleCreated
	SessionDynamicToolPublicationReady     = toolpublication.LifecycleReady
	SessionDynamicToolPublicationFailed    = toolpublication.LifecycleFailed
	SessionDynamicToolPublicationStopped   = toolpublication.LifecycleStopped
	SessionDynamicToolPublicationWatchGone = toolpublication.LifecycleWatchGone
)

type SessionDynamicToolPublicationState = toolpublication.State
type SessionDynamicToolPublicationError = toolpublication.PublicationError

const dynamicToolEventBufferSize = 16

// Deprecated: this compatibility adapter remains at the CLI seam while
// publication policy is owned by go-agent-runtime/services/toolpublication.
type sessionDynamicToolPublisher struct {
	publisher toolpublication.Publisher
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
	service := toolpublicationwire.NewService(toolpublicationwire.Dependencies{})
	publisher, err := service.NewPublisher(toolpublication.Options{
		StaticStableDefinitions: staticStableDefinitions,
		InitialDefinitions:      initialDefinitions,
		Watch:                   adaptDynamicToolWatch(watch),
		Refresh:                 refresh,
		TimerFactory:            adaptDynamicToolTimerFactory(timerFactory),
	})
	if err != nil {
		return nil
	}
	return &sessionDynamicToolPublisher{publisher: publisher}
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

// start only converts the loop's session-event port. The service owns watch,
// timer, readiness, publication state, error identity, and cancellation.
func (p *sessionDynamicToolPublisher) start(parent context.Context, loop *agentloop.AgentLoop) {
	if p == nil || p.publisher == nil {
		return
	}
	p.publisher.Start(parent, agentLoopSessionUpdateSink{loop: loop})
}

func (p *sessionDynamicToolPublisher) markSessionReady() {
	if p == nil || p.publisher == nil {
		return
	}
	p.publisher.Ready()
}

func (p *sessionDynamicToolPublisher) errors() <-chan error {
	if p == nil || p.publisher == nil {
		return nil
	}
	return p.publisher.Errors()
}

func (p *sessionDynamicToolPublisher) stop() {
	if p == nil || p.publisher == nil {
		return
	}
	p.publisher.Stop()
}

type agentLoopSessionUpdateSink struct {
	loop *agentloop.AgentLoop
}

func (s agentLoopSessionUpdateSink) SendSessionUpdate(ctx context.Context, definitions []messages.ToolDefinition) error {
	if s.loop == nil {
		return errors.New("session agent loop is nil")
	}
	return s.loop.SendSessionEvent(ctx, messages.StreamMessage{
		Type: messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
			Tools: definitions,
		}),
	})
}

func adaptDynamicToolWatch(watch func(context.Context) <-chan webmcp.BrokerEvent) toolpublication.EventWatch {
	return func(ctx context.Context) <-chan toolpublication.Event {
		source := watch(ctx)
		if source == nil {
			return nil
		}
		converted := make(chan toolpublication.Event, dynamicToolEventBufferSize)
		go translateDynamicToolEvents(ctx, source, converted)
		return converted
	}
}

func translateDynamicToolEvents(ctx context.Context, source <-chan webmcp.BrokerEvent, converted chan<- toolpublication.Event) {
	defer close(converted)
	for {
		event, ok := nextDynamicToolEvent(ctx, source)
		if !ok {
			return
		}
		convertedEvent, supported := convertDynamicToolEvent(event)
		if !supported {
			continue
		}
		select {
		case converted <- convertedEvent:
		case <-ctx.Done():
			return
		}
	}
}

func nextDynamicToolEvent(ctx context.Context, source <-chan webmcp.BrokerEvent) (webmcp.BrokerEvent, bool) {
	select {
	case <-ctx.Done():
		return webmcp.BrokerEvent{}, false
	case event, ok := <-source:
		return event, ok
	}
}

func convertDynamicToolEvent(event webmcp.BrokerEvent) (toolpublication.Event, bool) {
	var eventType toolpublication.EventType
	switch event.Type {
	case webmcp.BrokerEventSelected:
		eventType = toolpublication.EventSelected
	case webmcp.BrokerEventCatalogChanged:
		eventType = toolpublication.EventCatalogChanged
	case webmcp.BrokerEventGenerationChanged:
		eventType = toolpublication.EventGenerationChanged
	case webmcp.BrokerEventInvocationCreated, webmcp.BrokerEventInvocationTerminal, webmcp.BrokerEventSessionClosed:
		return toolpublication.Event{}, false
	default:
		return toolpublication.Event{}, false
	}
	return toolpublication.Event{
		Type:       eventType,
		Sequence:   event.Sequence,
		BrowserID:  string(event.BrowserID),
		TargetID:   string(event.TargetID),
		Generation: event.Generation,
	}, true
}

func adaptDynamicToolTimerFactory(factory webmcp.TimerFactory) toolpublication.TimerFactory {
	if factory == nil {
		return dynamicToolWallTimerFactory{}
	}
	return dynamicToolTimerFactory{factory: factory}
}

type dynamicToolWallTimerFactory struct{}

func (dynamicToolWallTimerFactory) NewTimer(duration time.Duration) toolpublication.Timer {
	return dynamicToolWallTimer{timer: time.NewTimer(duration)}
}

type dynamicToolWallTimer struct{ timer *time.Timer }

func (t dynamicToolWallTimer) C() <-chan time.Time { return t.timer.C }
func (t dynamicToolWallTimer) Stop() bool          { return t.timer.Stop() }
func (t dynamicToolWallTimer) Reset(duration time.Duration) bool {
	return t.timer.Reset(duration)
}

type dynamicToolTimerFactory struct {
	factory webmcp.TimerFactory
}

func (f dynamicToolTimerFactory) NewTimer(duration time.Duration) toolpublication.Timer {
	timer := f.factory.NewTimer(duration)
	if timer == nil {
		return nil
	}
	return dynamicToolTimer{timer: timer}
}

type dynamicToolTimer struct {
	timer webmcp.Timer
}

func (t dynamicToolTimer) C() <-chan time.Time { return t.timer.C() }
func (t dynamicToolTimer) Stop() bool          { return t.timer.Stop() }
func (t dynamicToolTimer) Reset(duration time.Duration) bool {
	return t.timer.Reset(duration)
}
