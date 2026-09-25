package sessionbroker

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Discover forwards after the capability bootstrap.
func (b *Broker) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return b.Broker.Discover(ctx, options)
}

// ListTargets forwards after the capability bootstrap.
func (b *Broker) ListTargets(ctx context.Context, selector webmcp.BrowserSelector) ([]webmcp.Target, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return b.Broker.ListTargets(ctx, selector)
}

// Select forwards after the capability bootstrap.
func (b *Broker) Select(ctx context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	return b.Broker.Select(ctx, selector)
}

// Selected forwards after the capability bootstrap.
func (b *Broker) Selected(ctx context.Context) (webmcp.PageContext, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	return b.Broker.Selected(ctx)
}

// ListTools forwards after the capability bootstrap.
func (b *Broker) ListTools(ctx context.Context, options webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.ToolCatalogSnapshot{}, err
	}
	return b.Broker.ListTools(ctx, options)
}

// Invoke forwards after the capability bootstrap.
func (b *Broker) Invoke(ctx context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.InvokeResult{}, err
	}
	return b.Broker.Invoke(ctx, request)
}

// Cancel forwards after the capability bootstrap.
func (b *Broker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	return b.Broker.Cancel(ctx, request)
}

// Watch forwards after the capability bootstrap; a failed bootstrap yields
// an already-closed stream.
func (b *Broker) Watch(ctx context.Context) <-chan webmcp.BrokerEvent {
	if err := b.ensureInitialized(ctx); err != nil {
		closed := make(chan webmcp.BrokerEvent)
		close(closed)
		return closed
	}
	return b.Broker.Watch(ctx)
}

// WatchBrowserEvents forwards the adapter-owned semantic stream without
// making the recording observer a second consumer of TargetSession.Events.
func (b *Broker) WatchBrowserEvents(ctx context.Context) <-chan webmcp.BrowserEvent {
	if b == nil || b.Broker == nil {
		return nil
	}
	watcher, ok := b.Broker.(webmcp.BrowserEventWatcher)
	if !ok {
		return nil
	}
	return watcher.WatchBrowserEvents(ctx)
}
