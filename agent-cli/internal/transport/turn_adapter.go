package transport

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
)

// SessionTurnBrowserRequest translates broker events at the host boundary.
// The sessionturn service owns event scheduling and publication state.
func SessionTurnBrowserRequest(watch func(context.Context) <-chan webmcp.BrokerEvent, refresh func(context.Context) ([]messages.ToolDefinition, error)) sessionturn.BrowserRequest {
	request := sessionturn.BrowserRequest{Refresh: refresh}
	if watch == nil {
		return request
	}
	request.Watch = func(ctx context.Context, emit func(sessionturn.BrowserEvent) bool) error {
		input := watch(ctx)
		if input == nil {
			return errors.New("browser event watch returned a nil channel")
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case event, ok := <-input:
				if !ok {
					return nil
				}
				converted, ok := browserEvent(event)
				if ok && !emit(converted) {
					return nil
				}
			}
		}
	}
	return request
}

// StartSessionTurnPublication forwards provider updates through the already
// prepared service runtime. It does not prepare or retain runtime state.
func StartSessionTurnPublication(ctx context.Context, runtime sessionturn.Runtime, loop *agentloop.AgentLoop, browser sessionturn.BrowserRequest, base, initial []messages.ToolDefinition) (sessionturn.Publication, error) {
	if loop == nil || browser.Watch == nil || browser.Refresh == nil {
		return nil, nil
	}
	request := sessionturn.PublicationRequest{
		BaseDefinitions:    base,
		InitialDefinitions: initial,
		Browser:            browser,
		Publish: func(ctx context.Context, definitions []messages.ToolDefinition) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: definitions})})
		},
	}
	if runtime != nil {
		return runtime.StartPublication(ctx, request)
	}
	return sessionturnwire.NewDefaultService().StartPublication(ctx, request)
}

func StopPublication(publication sessionturn.Publication) {
	if publication != nil {
		publication.Stop()
	}
}

func MarkPublicationReady(publication sessionturn.Publication) {
	if publication != nil {
		publication.MarkReady()
	}
}

func browserEvent(event webmcp.BrokerEvent) (sessionturn.BrowserEvent, bool) {
	var kind sessionturn.BrowserEventType
	switch event.Type {
	case webmcp.BrokerEventSelected:
		kind = sessionturn.BrowserEventSelectionChanged
	case webmcp.BrokerEventCatalogChanged:
		kind = sessionturn.BrowserEventCatalogChanged
	case webmcp.BrokerEventGenerationChanged:
		kind = sessionturn.BrowserEventGenerationChanged
	case webmcp.BrokerEventInvocationCreated, webmcp.BrokerEventInvocationTerminal, webmcp.BrokerEventSessionClosed:
		return sessionturn.BrowserEvent{}, false
	default:
		return sessionturn.BrowserEvent{}, false
	}
	return sessionturn.BrowserEvent{Type: kind, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, Sequence: event.Sequence}, true
}
