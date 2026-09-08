package lifecycle

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const browserEventQueueCapacity = 16

func browserWatch(watch func(context.Context) <-chan rooms.BrowserEvent) func(context.Context) <-chan session.LiveCapabilityEvent {
	if watch == nil {
		return nil
	}
	return func(ctx context.Context) <-chan session.LiveCapabilityEvent {
		input := watch(ctx)
		if input == nil {
			return nil
		}
		output := make(chan session.LiveCapabilityEvent, browserEventQueueCapacity)
		go forwardBrowserEvents(ctx, input, output)
		return output
	}
}

func forwardBrowserEvents(ctx context.Context, input <-chan rooms.BrowserEvent, output chan<- session.LiveCapabilityEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			if !sendBrowserEvent(ctx, output, convertBrowserEvent(event)) {
				return
			}
		}
	}
}

func convertBrowserEvent(event rooms.BrowserEvent) session.LiveCapabilityEvent {
	return session.LiveCapabilityEvent{
		Type: event.Type, Sequence: event.Sequence, Timestamp: event.At,
		BrowserID: event.BrowserID, TargetID: event.TargetID,
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: event.InvocationID, ToolName: event.ToolName,
		State: event.State, Status: event.Status, ErrorCode: event.ErrorCode,
		Reason: event.Reason, CatalogReady: event.CatalogReady,
		ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown,
	}
}

func sendBrowserEvent(ctx context.Context, output chan<- session.LiveCapabilityEvent, event session.LiveCapabilityEvent) bool {
	select {
	case output <- event:
		return true
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

// browserCapabilityHandle adapts the room's legacy browser callbacks to the
// session-owned capability lifecycle. Once a host promotes its browser owner
// to session.LiveCapabilityHandle directly, this adapter disappears while
// the public behavior remains unchanged.
type browserCapabilityHandle struct {
	initialize  func(context.Context) error
	refresh     func(context.Context) ([]messages.ToolDefinition, error)
	watch       func(context.Context) <-chan session.LiveCapabilityEvent
	definitions []messages.ToolDefinition
	close       func() error
	closeOnce   sync.Once
	closeErr    error
}

func newBrowserCapabilityHandle(browser rooms.BrowserCapabilities) *browserCapabilityHandle {
	return &browserCapabilityHandle{
		initialize: browser.Initialize, refresh: browser.RefreshToolDefinitions,
		watch: browserWatch(browser.BrowserWatch), definitions: append([]messages.ToolDefinition(nil), browser.Definitions...), close: browser.Close,
	}
}

func (h *browserCapabilityHandle) Initialize(ctx context.Context) error {
	if h == nil || h.initialize == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("browser capability initialize context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.initialize(ctx)
}

func (h *browserCapabilityHandle) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	if h == nil {
		return nil, nil
	}
	if ctx == nil {
		if h.refresh == nil {
			return append([]messages.ToolDefinition(nil), h.definitions...), nil
		}
		return nil, errors.New("browser capability refresh context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.refresh != nil {
		return h.refresh(ctx)
	}
	return append([]messages.ToolDefinition(nil), h.definitions...), nil
}

func (h *browserCapabilityHandle) BrowserWatch(ctx context.Context) <-chan session.LiveCapabilityEvent {
	if h == nil || h.watch == nil {
		return nil
	}
	return h.watch(ctx)
}

func (h *browserCapabilityHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.close != nil {
			h.closeErr = h.close()
		}
	})
	return h.closeErr
}
