package livehost

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const liveCapabilityEventBuffer = 32

// MapBrowserEvents adapts the CLI browser observer to the provider-neutral
// live capability stream. The adapter owns a bounded queue and exits with the
// invocation context so browser resources cannot outlive the session.
func MapBrowserEvents(source func(context.Context) <-chan webmcp.BrowserEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return mapCapabilityEvents(source, browserCapabilityEvent)
}

// MapBrokerEvents adapts the legacy broker observer to the same neutral live
// capability stream while retaining its typed lifecycle state.
func MapBrokerEvents(source func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return mapCapabilityEvents(source, brokerCapabilityEvent)
}

func mapCapabilityEvents[Event any](source func(context.Context) <-chan Event, mapEvent func(Event) runtimeSession.LiveCapabilityEvent) func(context.Context) <-chan runtimeSession.LiveCapabilityEvent {
	return func(ctx context.Context) <-chan runtimeSession.LiveCapabilityEvent {
		if source == nil || mapEvent == nil || ctx == nil {
			return nil
		}
		input := source(ctx)
		if input == nil {
			return nil
		}
		output := make(chan runtimeSession.LiveCapabilityEvent, liveCapabilityEventBuffer)
		go forwardCapabilityEvents(ctx, input, output, mapEvent)
		return output
	}
}

func forwardCapabilityEvents[Event any](ctx context.Context, input <-chan Event, output chan<- runtimeSession.LiveCapabilityEvent, mapEvent func(Event) runtimeSession.LiveCapabilityEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			mapped := mapEvent(event)
			select {
			case output <- mapped:
			case <-ctx.Done():
				return
			}
		}
	}
}

func browserCapabilityEvent(event webmcp.BrowserEvent) runtimeSession.LiveCapabilityEvent {
	return runtimeSession.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: string(event.InvocationID), ToolName: event.ToolName,
		Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason,
		CatalogReady: event.CatalogReady, ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown,
	}
}

func brokerCapabilityEvent(event webmcp.BrokerEvent) runtimeSession.LiveCapabilityEvent {
	return runtimeSession.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, InvocationID: string(event.InvocationID),
		ToolName: event.ToolName, State: string(event.State), Reason: event.Reason,
	}
}
