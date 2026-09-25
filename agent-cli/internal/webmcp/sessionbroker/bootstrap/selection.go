package bootstrap

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// selectConfigured reconciles an operator-configured selection. An explicitly
// named target is always reconciled exactly; the reconnect service never
// substitutes a different target. A configured browser/origin without a
// target only admits automatic single-target selection when the operator
// explicitly requested it.
func (b *bootstrapper) selectConfigured(ctx context.Context, selected selectors) (bool, error) {
	if b.reconnector == nil {
		return false, nil
	}
	if selected.targetID != "" {
		selection, err := b.reconnect(ctx, discovery.ReconnectOptions{
			BrowserID: selected.browserID,
			TargetID:  selected.targetID,
			Origin:    selected.origin,
		})
		if err != nil {
			return true, capabilityError(err)
		}
		return true, b.adopt(ctx, selection, selected.activate)
	}
	if !selected.explicit() || b.browser.Selection.AutoSelect != config.BrowserAutoSelectSingle {
		return false, nil
	}
	selection, err := b.reconnect(ctx, discovery.ReconnectOptions{
		AutoSelect: discovery.AutoSelectSingle,
		BrowserID:  selected.browserID,
		Origin:     selected.origin,
	})
	if err != nil {
		return true, b.recoverConnectedUnselected(ctx, err)
	}
	return true, b.adopt(ctx, selection, selected.activate)
}

// selectDefault handles a session without an explicit selector: an
// explicitly persisted selection takes precedence over automatic selection.
func (b *bootstrapper) selectDefault(ctx context.Context, selected selectors) (bool, error) {
	if handled, err := b.restorePersisted(ctx, selected); handled {
		return true, err
	}
	return b.autoSelectSingle(ctx, selected)
}

func (b *bootstrapper) restorePersisted(ctx context.Context, selected selectors) (bool, error) {
	if b.loader != nil {
		return b.restoreLoaded(ctx, selected)
	}
	if !b.browser.Selection.Persist || b.reconnector == nil {
		return false, nil
	}
	// Compatibility fakes may expose Reconnect without the optional loader. A
	// missing record is ordinary; every other failure is retained as the
	// session's classified failed state.
	selection, err := b.reconnect(ctx, discovery.ReconnectOptions{AutoSelect: discovery.AutoSelectPersisted})
	switch {
	case err == nil:
		return true, b.adopt(ctx, selection, selected.activate)
	case recoverableRestoredSelectionError(err):
		return true, b.recoverRestoredSelection(ctx, err)
	case !noSelectionError(err):
		return true, capabilityError(err)
	}
	return false, nil
}

func (b *bootstrapper) restoreLoaded(ctx context.Context, selected selectors) (bool, error) {
	_, present, err := b.loader.LoadPersistedSelection(ctx)
	if err != nil {
		return true, capabilityError(err)
	}
	if !present {
		return false, nil
	}
	if b.reconnector == nil {
		return true, capabilityError(errors.New("strict persisted selection restore is unavailable"))
	}
	selection, err := b.reconnect(ctx, discovery.ReconnectOptions{AutoSelect: discovery.AutoSelectPersisted})
	if err == nil {
		return true, b.adopt(ctx, selection, selected.activate)
	}
	// A managed browser may have been deliberately closed after persisting its
	// last selected target. On the next session the new Chrome instance has a
	// different target identity, but its configured startup page is still the
	// intended current tab. Recreate and select that page so first-call
	// operations such as Cast do not inherit a connected-but-unselected dead end.
	if b.browser.UsesManagedBrowser() && staleSelectionError(err) {
		if recovered, recoveryErr := b.recoverManagedStartup(ctx); recovered {
			return true, recoveryErr
		}
	}
	// A persisted record that no longer resolves is the ordinary drift case:
	// the remembered tab was closed, reloaded, or is now one of several equally
	// eligible pages. That must leave the browser connected but unselected --
	// the model then lists tabs, asks the customer, and selects an exact
	// target, after which the page tools are published. Failing the whole
	// capability here instead advertises no page tools, drops the
	// connected-unselected grounding, and leaves the model free to tell the
	// customer that browser access does not exist. Non-recoverable failures
	// still fail closed.
	return true, b.recoverRestoredSelection(ctx, err)
}

// autoSelectSingle uses the single-target reconnect semantics. Endpoint-free
// managed WebMCP sessions are the default browser experience, so they always
// use it; external browser configurations retain their opt-in behavior.
func (b *bootstrapper) autoSelectSingle(ctx context.Context, selected selectors) (bool, error) {
	single := b.browser.Selection.AutoSelect == config.BrowserAutoSelectSingle ||
		(b.browser.UsesManagedBrowser() && b.browser.BrowserBackendEnabled())
	if !single || b.reconnector == nil {
		return false, nil
	}
	selection, err := b.reconnect(ctx, discovery.ReconnectOptions{AutoSelect: discovery.AutoSelectSingle})
	if err != nil {
		// #312's recovery already subsumes the endpoint-usability concern: it
		// verifies the endpoint and marks the session connected-but-unselected,
		// so an exact model selector can still resolve the target
		// deterministically, and the model never reads the browser as absent.
		return true, b.recoverConnectedUnselected(ctx, err)
	}
	return true, b.adopt(ctx, selection, selected.activate)
}
