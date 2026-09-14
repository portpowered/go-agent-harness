package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// sessionTurnBrowserRequest is the only host-to-runtime conversion needed by
// the session planner. Browser ownership and event production remain in the
// CLI host; the session-turn service consumes only immutable event values.
func sessionTurnBrowserRequest(watch func(context.Context) <-chan webmcp.BrokerEvent, refresh func(context.Context) ([]messages.ToolDefinition, error)) sessionturn.BrowserRequest {
	request := sessionturn.BrowserRequest{Refresh: refresh}
	if watch != nil {
		request.Watch = sessionTurnBrowserWatch(watch)
	}
	return request
}

func sessionTurnBrowserWatch(watch func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan sessionturn.BrowserEvent {
	return func(ctx context.Context) <-chan sessionturn.BrowserEvent {
		input := watch(ctx)
		if input == nil {
			return nil
		}
		output := make(chan sessionturn.BrowserEvent, 16)
		go forwardSessionTurnEvents(ctx, input, output)
		return output
	}
}

func forwardSessionTurnEvents(ctx context.Context, input <-chan webmcp.BrokerEvent, output chan<- sessionturn.BrowserEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			converted, ok := sessionTurnBrowserEvent(event)
			if !ok {
				continue
			}
			select {
			case output <- converted:
			case <-ctx.Done():
				return
			}
		}
	}
}

func sessionTurnBrowserEvent(event webmcp.BrokerEvent) (sessionturn.BrowserEvent, bool) {
	var kind sessionturn.BrowserEventType
	switch event.Type {
	case webmcp.BrokerEventSelected:
		kind = sessionturn.BrowserEventSelectionChanged
	case webmcp.BrokerEventCatalogChanged:
		kind = sessionturn.BrowserEventCatalogChanged
	case webmcp.BrokerEventGenerationChanged:
		kind = sessionturn.BrowserEventGenerationChanged
	default:
		return sessionturn.BrowserEvent{}, false
	}
	return sessionturn.BrowserEvent{Type: kind, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, Sequence: event.Sequence}, true
}
