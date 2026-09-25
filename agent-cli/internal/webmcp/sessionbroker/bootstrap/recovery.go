package bootstrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// managedBlankStartupURL is the managed startup page that is created without
// requiring a WebMCP selection.
const managedBlankStartupURL = "about:blank"

// recoverRestoredSelection is the recovery used when a *persisted* selection
// cannot be restored. It admits everything the shared recovery admits, plus a
// retryable stale selection: a remembered tab that was closed, reloaded, or
// replaced is ordinary drift, not an operator-named target that must fail
// closed.
func (b *bootstrapper) recoverRestoredSelection(ctx context.Context, selectionErr error) error {
	if !recoverableRestoredSelectionError(selectionErr) {
		return capabilityError(selectionErr)
	}
	return b.markReachableUnselected(ctx)
}

func (b *bootstrapper) recoverConnectedUnselected(ctx context.Context, selectionErr error) error {
	if !recoverableSelectionError(selectionErr) {
		return capabilityError(selectionErr)
	}
	// A warm managed Chrome can remain alive after its final window is closed.
	// That state is reachable but unusable: discovery returns no eligible tab
	// and the user sees no browser. Recreate and foreground about:blank without
	// requiring WebMCP selection; a configured website uses the model-facing
	// open-and-select operation. External endpoints retain their no-mutation
	// recovery path.
	if b.browser.UsesManagedBrowser() && noSelectionError(selectionErr) {
		if recovered, recoveryErr := b.recoverManagedStartup(ctx); recovered {
			return recoveryErr
		}
	}
	return b.markReachableUnselected(ctx)
}

// markReachableUnselected verifies the endpoint and leaves the browser
// connected but unselected.
func (b *bootstrapper) markReachableUnselected(ctx context.Context) error {
	if err := verifyEndpoint(ctx, b.browser, b.broker); err != nil {
		return err
	}
	b.mark(webmcp.BrowserCapabilityConnectedUnselected)
	return nil
}

// recoverManagedStartup recreates the managed startup page. It reports false
// when the broker cannot perform the needed tab operation, so the caller can
// fall back to ordinary recovery.
func (b *bootstrapper) recoverManagedStartup(ctx context.Context) (bool, error) {
	startupURL := b.browser.ManagedStartupURL()
	request := webmcp.OpenTabRequest{URL: startupURL, Activate: true}
	if startupURL == managedBlankStartupURL {
		creator, ok := b.broker.(webmcp.BrokerTabCreator)
		if !ok {
			return false, nil
		}
		if _, err := creator.CreateTab(ctx, request); err != nil {
			return true, capabilityError(err)
		}
		b.mark(webmcp.BrowserCapabilityConnectedUnselected)
		return true, nil
	}
	opener, ok := b.broker.(webmcp.BrokerTabOpener)
	if !ok {
		return false, nil
	}
	if _, err := opener.OpenTab(ctx, request); err != nil {
		return true, capabilityError(err)
	}
	b.mark(webmcp.BrowserCapabilitySelected)
	return true, nil
}
