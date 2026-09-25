package sessionbroker

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type selectedRefresher interface {
	SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
}

type optionSelector interface {
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
}

// ready reports whether the delegate exists and the bootstrap succeeded.
func (b *Broker) ready(ctx context.Context) error {
	if b == nil || b.Broker == nil {
		return errors.New("WebMCP broker is unavailable")
	}
	return b.ensureInitialized(ctx)
}

// WaitInvocation forwards the terminal-result capability of the stateful
// production broker. Embedding webmcp.Broker alone would expose only the
// frozen base interface and make model-facing invoke calls return dispatch
// acknowledgements instead of completed page results.
func (b *Broker) WaitInvocation(ctx context.Context, id webmcp.InvocationID) (webmcp.InvokeResult, error) {
	if b == nil || b.Broker == nil {
		return webmcp.InvokeResult{}, errors.New("WebMCP invocation waiter is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.InvokeResult{}, err
	}
	waiter, ok := b.Broker.(webmcp.InvocationWaiter)
	if !ok {
		return webmcp.InvokeResult{}, errors.New("WebMCP broker does not support terminal invocation results")
	}
	return waiter.WaitInvocation(ctx, id)
}

// CapturePageScreenshot forwards the optional page-capture capability through
// the session coordinator so initialization and cleanup remain owned by the
// same browser capability lifetime as the other broker tools.
func (b *Broker) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.PageScreenshot{}, err
	}
	capturer, ok := b.Broker.(webmcp.PageScreenshotter)
	if !ok {
		return webmcp.PageScreenshot{}, webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{"capability": webmcp.PageCaptureScreenshotMethod},
		)
	}
	return capturer.CapturePageScreenshot(ctx)
}

// SelectedWithRefresh preserves the production broker's refresh extension;
// older injected brokers retain the frozen Selected behavior.
func (b *Broker) SelectedWithRefresh(ctx context.Context, refresh bool) (webmcp.PageContext, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	if refresher, ok := b.Broker.(selectedRefresher); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return b.Broker.Selected(ctx)
}

// SelectWithOptions preserves explicit target activation in the production
// broker while retaining compatibility with a base-interface-only delegate.
func (b *Broker) SelectWithOptions(ctx context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	if withOptions, ok := b.Broker.(optionSelector); ok {
		return withOptions.SelectWithOptions(ctx, selector, options)
	}
	return b.Broker.Select(ctx, selector)
}

// OpenTab preserves model-facing tab creation through the session lifecycle
// wrapper. Embedding the base Broker interface alone hides this optional
// capability even when the production broker and Chrome adapter support it.
func (b *Broker) OpenTab(ctx context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	opener, ok := b.Broker.(webmcp.BrokerTabOpener)
	if !ok {
		return webmcp.PageContext{}, tabUnsupportedError(openTabUnsupported, "open_tab")
	}
	return opener.OpenTab(ctx, request)
}

// NavigateSelectedTab preserves target-scoped navigation through the session
// lifecycle wrapper. This keeps model requests on the same target, including
// when Chrome is actively mirroring that target to a Cast receiver.
func (b *Broker) NavigateSelectedTab(ctx context.Context, targetURL string) (webmcp.PageContext, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	navigator, ok := b.Broker.(webmcp.BrokerTabNavigator)
	if !ok {
		return webmcp.PageContext{}, tabUnsupportedError("The connected browser cannot navigate the selected tab.", "navigate_tab")
	}
	return navigator.NavigateSelectedTab(ctx, targetURL)
}

// CreateTab preserves the unselected creation seam used by managed browser
// bootstrap to make an ordinary about:blank window visible.
func (b *Broker) CreateTab(ctx context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	if err := b.ready(ctx); err != nil {
		return webmcp.Target{}, err
	}
	creator, ok := b.Broker.(webmcp.BrokerTabCreator)
	if !ok {
		return webmcp.Target{}, tabUnsupportedError(openTabUnsupported, "open_tab")
	}
	return creator.CreateTab(ctx, request)
}

// CancelDirect preserves the cross-process cancellation extension for the
// direct CLI callers that receive the session's broker value.
func (b *Broker) CancelDirect(ctx context.Context, request webmcp.DirectCancelRequest) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	if canceller, ok := b.Broker.(webmcp.DirectCanceller); ok {
		return canceller.CancelDirect(ctx, request)
	}
	return errors.New("WebMCP broker does not support direct cancellation")
}
