package sessionbroker

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtime "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// liveCapabilityEventBuffer bounds each live capability projection queue.
const liveCapabilityEventBuffer = 32

// LiveBrowserEvents adapts a WebMCP browser observer to the provider-neutral
// live capability stream. Each subscription owns a bounded queue and exits
// with its context so browser resources cannot outlive the session.
func LiveBrowserEvents(source func(context.Context) <-chan webmcp.BrowserEvent) func(context.Context) <-chan session.LiveCapabilityEvent {
	return liveCapabilityEvents(source, browserCapabilityEvent)
}

// LiveBrokerEvents adapts the legacy broker observer to the same live
// capability stream while retaining its typed lifecycle state.
func LiveBrokerEvents(source func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan session.LiveCapabilityEvent {
	return liveCapabilityEvents(source, brokerCapabilityEvent)
}

func liveCapabilityEvents[Event any](source func(context.Context) <-chan Event, convert func(Event) session.LiveCapabilityEvent) func(context.Context) <-chan session.LiveCapabilityEvent {
	return func(ctx context.Context) <-chan session.LiveCapabilityEvent {
		if source == nil || ctx == nil {
			return nil
		}
		return projectBufferedEvents(ctx, source(ctx), convert, liveCapabilityEventBuffer)
	}
}

func browserCapabilityEvent(event webmcp.BrowserEvent) session.LiveCapabilityEvent {
	return session.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: string(event.InvocationID), ToolName: event.ToolName,
		Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason,
		CatalogReady: event.CatalogReady, ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown,
	}
}

func brokerCapabilityEvent(event webmcp.BrokerEvent) session.LiveCapabilityEvent {
	return session.LiveCapabilityEvent{
		Type: string(event.Type), Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, InvocationID: string(event.InvocationID),
		ToolName: event.ToolName, State: string(event.State), Reason: event.Reason,
	}
}

// ToRuntimeEvents projects an already-owned WebMCP event stream onto the
// runtime browser-conversation contract without retaining subscription state.
func ToRuntimeEvents(ctx context.Context, source <-chan webmcp.BrowserEvent) <-chan runtime.BrowserEvent {
	return projectEvents(ctx, source, runtimeEvent)
}

// ToWebMCPEvents is the reverse projection used at the session boundary
// where older observability ports still speak WebMCP.
func ToWebMCPEvents(ctx context.Context, source <-chan runtime.BrowserEvent) <-chan webmcp.BrowserEvent {
	return projectEvents(ctx, source, webmcpEvent)
}

// projectEvents converts every event from source until source closes or ctx
// ends. A nil source yields a nil stream.
func projectEvents[From, To any](ctx context.Context, source <-chan From, convert func(From) To) <-chan To {
	return projectBufferedEvents(ctx, source, convert, 0)
}

// projectBufferedEvents is projectEvents with a bounded output queue.
func projectBufferedEvents[From, To any](ctx context.Context, source <-chan From, convert func(From) To, buffer int) <-chan To {
	if source == nil {
		return nil
	}
	out := make(chan To, buffer)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-source:
				if !ok {
					return
				}
				select {
				case out <- convert(event):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func webmcpEvent(event runtime.BrowserEvent) webmcp.BrowserEvent {
	converted := webmcp.BrowserEvent{Type: webmcp.BrowserEventType(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: webmcp.BrowserID(event.BrowserID), TargetID: webmcp.TargetID(event.TargetID), Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, InvocationID: webmcp.InvocationID(event.InvocationID), ToolName: event.ToolName, FrameID: webmcp.FrameID(event.FrameID), Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason, Input: append(json.RawMessage(nil), event.Input...), Output: append(json.RawMessage(nil), event.Output...), ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, CatalogReady: event.CatalogReady, RemovedToolNames: append([]string(nil), event.RemovedToolNames...)}
	converted.Tools = make([]webmcp.ToolDescriptor, len(event.Tools))
	for i, tool := range event.Tools {
		converted.Tools[i] = webmcp.ToolDescriptor{Ref: webmcp.ToolRef(tool.Ref), Name: tool.Name, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), FrameID: webmcp.FrameID(tool.FrameID), Generation: tool.Generation}
	}
	return converted
}

func runtimeEvent(event webmcp.BrowserEvent) runtime.BrowserEvent {
	converted := runtime.BrowserEvent{Type: string(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, InvocationID: string(event.InvocationID), ToolName: event.ToolName, FrameID: string(event.FrameID), Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason, Input: append(json.RawMessage(nil), event.Input...), Output: append(json.RawMessage(nil), event.Output...), ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, CatalogReady: event.CatalogReady, RemovedToolNames: append([]string(nil), event.RemovedToolNames...)}
	converted.Tools = make([]runtime.BrowserToolDescriptor, len(event.Tools))
	for i, tool := range event.Tools {
		converted.Tools[i] = runtime.BrowserToolDescriptor{Ref: string(tool.Ref), Name: tool.Name, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), FrameID: string(tool.FrameID), Generation: tool.Generation}
	}
	return converted
}
