package sessionbroker

import (
	"context"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtime "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

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
	if source == nil {
		return nil
	}
	out := make(chan To)
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
