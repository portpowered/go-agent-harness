// Package browserconversation contains the stateless CLI-to-runtime browser
// transport adapter. It translates protocol-owned values into the reusable
// browser-conversation contract and retains no policy or mutable run state.
package browserconversation

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtime "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

type Adapter struct{ inner webmcp.Broker }

func New(inner webmcp.Broker) *Adapter { return &Adapter{inner: inner} }

func (a *Adapter) Discover(ctx context.Context, options runtime.BrowserDiscoveryOptions) ([]runtime.BrowserCandidate, error) {
	if a == nil || a.inner == nil {
		return nil, errors.New("browser conversation adapter has no broker")
	}
	values, err := a.inner.Discover(ctx, webmcp.DiscoverOptions{ExplicitOnly: options.ExplicitOnly})
	if err != nil {
		return nil, err
	}
	result := make([]runtime.BrowserCandidate, len(values))
	for i, value := range values {
		result[i] = runtime.BrowserCandidate{ID: string(value.ID)}
	}
	return result, nil
}

func (a *Adapter) ListTargets(ctx context.Context, selector runtime.BrowserSelector) ([]runtime.BrowserTarget, error) {
	if a == nil || a.inner == nil {
		return nil, errors.New("browser conversation adapter has no broker")
	}
	values, err := a.inner.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: webmcp.BrowserID(selector.BrowserID)})
	if err != nil {
		return nil, err
	}
	result := make([]runtime.BrowserTarget, len(values))
	for i, value := range values {
		result[i] = runtime.BrowserTarget{BrowserID: string(value.BrowserID), ID: string(value.ID)}
	}
	return result, nil
}

func (a *Adapter) Select(ctx context.Context, selector runtime.BrowserTargetSelector) (runtime.BrowserPageContext, error) {
	if a == nil || a.inner == nil {
		return runtime.BrowserPageContext{}, errors.New("browser conversation adapter has no broker")
	}
	value, err := a.inner.Select(ctx, webmcp.TargetSelector{BrowserID: webmcp.BrowserID(selector.BrowserID), TargetID: webmcp.TargetID(selector.TargetID)})
	return pageContext(value), err
}

func (a *Adapter) Selected(ctx context.Context) (runtime.BrowserPageContext, error) {
	if a == nil || a.inner == nil {
		return runtime.BrowserPageContext{}, errors.New("browser conversation adapter has no broker")
	}
	value, err := a.inner.Selected(ctx)
	return pageContext(value), err
}

func (a *Adapter) ListTools(ctx context.Context, options runtime.BrowserListToolsOptions) (runtime.BrowserToolCatalog, error) {
	if a == nil || a.inner == nil {
		return runtime.BrowserToolCatalog{}, errors.New("browser conversation adapter has no broker")
	}
	value, err := a.inner.ListTools(ctx, webmcp.ListToolsOptions{Refresh: options.Refresh, IncludeSchemas: options.IncludeSchemas})
	return catalog(value), err
}

func (a *Adapter) Invoke(ctx context.Context, request runtime.BrowserInvokeRequest) (runtime.BrowserInvokeResult, error) {
	if a == nil || a.inner == nil {
		return runtime.BrowserInvokeResult{}, errors.New("browser conversation adapter has no broker")
	}
	value, err := a.inner.Invoke(ctx, webmcp.InvokeRequest{ToolRef: webmcp.ToolRef(request.ToolRef), Input: append(json.RawMessage(nil), request.Input...), Reason: request.Reason, ModelCallID: request.ModelCallID})
	return runtime.BrowserInvokeResult{InvocationID: string(value.InvocationID), State: string(value.State), Output: append(json.RawMessage(nil), value.Output...), ErrorCode: value.ErrorCode}, err
}

func (a *Adapter) Cancel(ctx context.Context, request runtime.BrowserCancelRequest) error {
	if a == nil || a.inner == nil {
		return errors.New("browser conversation adapter has no broker")
	}
	return a.inner.Cancel(ctx, webmcp.CancelRequest{InvocationID: webmcp.InvocationID(request.InvocationID), Reason: request.Reason})
}

func (a *Adapter) Watch(ctx context.Context) <-chan runtime.BrowserEvent {
	if a == nil || a.inner == nil {
		return nil
	}
	watcher, ok := a.inner.(webmcp.BrowserEventWatcher)
	if !ok {
		return nil
	}
	return convertEvents(ctx, watcher.WatchBrowserEvents(ctx))
}

// WatchEvents converts an already-owned WebMCP event stream without creating
// a broker or retaining subscription state.
func WatchEvents(ctx context.Context, source <-chan webmcp.BrowserEvent) <-chan runtime.BrowserEvent {
	return convertEvents(ctx, source)
}

// WatchWebMCPEvents is the reverse value projection used only at the CLI
// session boundary where older observability ports still speak WebMCP.
func WatchWebMCPEvents(ctx context.Context, source <-chan runtime.BrowserEvent) <-chan webmcp.BrowserEvent {
	return streamWebMCPEvents(ctx, source)
}

func (a *Adapter) WaitInvocation(ctx context.Context, invocationID string) (runtime.BrowserInvokeResult, error) {
	waiter, ok := a.inner.(webmcp.InvocationWaiter)
	if !ok {
		return runtime.BrowserInvokeResult{}, errors.New("browser broker does not support invocation waiting")
	}
	value, err := waiter.WaitInvocation(ctx, webmcp.InvocationID(invocationID))
	return runtime.BrowserInvokeResult{InvocationID: string(value.InvocationID), State: string(value.State), Output: append(json.RawMessage(nil), value.Output...), ErrorCode: value.ErrorCode}, err
}

func (a *Adapter) Close() error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Close()
}

func pageContext(value webmcp.PageContext) runtime.BrowserPageContext {
	return runtime.BrowserPageContext{BrowserID: string(value.Key.BrowserID), TargetID: string(value.Key.TargetID), Generation: value.Generation, Connected: value.Connected}
}

func catalog(value webmcp.ToolCatalogSnapshot) runtime.BrowserToolCatalog {
	result := runtime.BrowserToolCatalog{Context: pageContext(value.Context), Generation: value.Generation, Tools: make([]runtime.BrowserToolDescriptor, len(value.Tools))}
	for i, tool := range value.Tools {
		result.Tools[i] = runtime.BrowserToolDescriptor{Ref: string(tool.Ref), Name: tool.Name, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), FrameID: string(tool.FrameID), Generation: tool.Generation}
	}
	return result
}

func convertEvents(ctx context.Context, source <-chan webmcp.BrowserEvent) <-chan runtime.BrowserEvent {
	return streamRuntimeEvents(ctx, source)
}

func streamWebMCPEvents(ctx context.Context, source <-chan runtime.BrowserEvent) <-chan webmcp.BrowserEvent {
	if source == nil {
		return nil
	}
	out := make(chan webmcp.BrowserEvent)
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
				converted := convertToWebMCPEvent(event)
				select {
				case out <- converted:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func streamRuntimeEvents(ctx context.Context, source <-chan webmcp.BrowserEvent) <-chan runtime.BrowserEvent {
	if source == nil {
		return nil
	}
	out := make(chan runtime.BrowserEvent)
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
				case out <- convertToRuntimeEvent(event):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func convertToWebMCPEvent(event runtime.BrowserEvent) webmcp.BrowserEvent {
	converted := webmcp.BrowserEvent{Type: webmcp.BrowserEventType(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: webmcp.BrowserID(event.BrowserID), TargetID: webmcp.TargetID(event.TargetID), Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, InvocationID: webmcp.InvocationID(event.InvocationID), ToolName: event.ToolName, FrameID: webmcp.FrameID(event.FrameID), Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason, Input: append(json.RawMessage(nil), event.Input...), Output: append(json.RawMessage(nil), event.Output...), ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, CatalogReady: event.CatalogReady, RemovedToolNames: append([]string(nil), event.RemovedToolNames...)}
	converted.Tools = make([]webmcp.ToolDescriptor, len(event.Tools))
	for i, tool := range event.Tools {
		converted.Tools[i] = webmcp.ToolDescriptor{Ref: webmcp.ToolRef(tool.Ref), Name: tool.Name, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), FrameID: webmcp.FrameID(tool.FrameID), Generation: tool.Generation}
	}
	return converted
}

func convertToRuntimeEvent(event webmcp.BrowserEvent) runtime.BrowserEvent {
	converted := runtime.BrowserEvent{Type: string(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, InvocationID: string(event.InvocationID), ToolName: event.ToolName, FrameID: string(event.FrameID), Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason, Input: append(json.RawMessage(nil), event.Input...), Output: append(json.RawMessage(nil), event.Output...), ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, CatalogReady: event.CatalogReady, RemovedToolNames: append([]string(nil), event.RemovedToolNames...)}
	converted.Tools = make([]runtime.BrowserToolDescriptor, len(event.Tools))
	for i, tool := range event.Tools {
		converted.Tools[i] = runtime.BrowserToolDescriptor{Ref: string(tool.Ref), Name: tool.Name, InputSchema: append(json.RawMessage(nil), tool.InputSchema...), FrameID: string(tool.FrameID), Generation: tool.Generation}
	}
	return converted
}

var _ runtime.Broker = (*Adapter)(nil)
