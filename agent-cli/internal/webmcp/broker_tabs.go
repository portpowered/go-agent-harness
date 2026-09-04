package webmcp

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// OpenTab creates one page target and immediately adopts it as the selected
// WebMCP page. Browser creation is optional at the runtime boundary so older
// and replay-only adapters fail with a normal tool result instead of breaking
// the frozen Broker interface.
func (b *StatefulBroker) OpenTab(ctx context.Context, request OpenTabRequest) (PageContext, error) {
	creationRequest := request
	creationRequest.Activate = false
	opened, err := b.CreateTab(ctx, creationRequest)
	if err != nil {
		return PageContext{}, err
	}
	selected, err := b.selectWithOptions(ctx, TargetSelector{BrowserID: opened.BrowserID, TargetID: opened.ID}, SelectOptions{Activate: request.Activate})
	if err == nil {
		return selected, nil
	}
	// Selection deliberately remains connected when only affirmative catalog
	// evidence missed its short diagnostic deadline. Opening the requested page
	// succeeded; report that selected context so the model can refresh/list its
	// tools instead of interpreting a slow page as a failed tab creation.
	if isCatalogEvidenceError(err) {
		b.mu.Lock()
		pending := b.selected
		if pending != nil && (pending.context.Key.BrowserID != opened.BrowserID || pending.context.Key.TargetID != opened.ID) {
			pending = nil
		}
		b.mu.Unlock()
		if pending != nil {
			// A newly navigated page commonly registers its WebMCP producer just
			// after the one-second attach diagnostic. Give that exact selected
			// session one additional bounded catalog interval before returning a
			// connected-but-not-ready result to the model.
			if retryErr := b.waitForCatalog(ctx, pending, false); retryErr != nil && !isCatalogEvidenceError(retryErr) {
				return PageContext{}, retryErr
			}
		}
		selected, selectedErr := b.Selected(ctx)
		if selectedErr == nil && selected.Key.BrowserID == opened.BrowserID && selected.Key.TargetID == opened.ID {
			return selected, nil
		}
	}
	return PageContext{}, err
}

// NavigateSelectedTab changes the document loaded by the exact selected page
// while retaining its browser target identity. Chrome tab-mirroring routes are
// target-scoped, so this is the operation callers need when a cast tab should
// switch websites without opening and casting a different tab.
func (b *StatefulBroker) NavigateSelectedTab(ctx context.Context, rawURL string) (PageContext, error) {
	if err := contextError(ctx); err != nil {
		return PageContext{}, err
	}
	normalizedURL, err := normalizeOpenTabURL(rawURL)
	if err != nil || normalizedURL == "about:blank" {
		return PageContext{}, classified(ErrorInvalidToolInput, "The selected tab requires an absolute HTTP URL.", map[string]any{
			"phase":  "navigate_tab",
			"reason": "invalid_url",
		}, err)
	}
	if b == nil {
		return PageContext{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageContext{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected == nil {
		return PageContext{}, staleSelectionError("", "", 0, "selection_not_connected")
	}

	selected.dispatchMu.Lock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return PageContext{}, ErrClosed
	}
	if b.selected != selected {
		err := staleSelectionForSession(selected, "selection_changed")
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return PageContext{}, err
	}
	if err := b.captureSelectionStateErrorLocked(selected, "navigate_tab", "selection_not_connected"); err != nil {
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return PageContext{}, err
	}
	navigator, ok := selected.session.(TargetTabNavigator)
	if !ok {
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return PageContext{}, classified(ErrorBrowserProtocol, "the selected browser page does not support in-place navigation", map[string]any{
			"phase":       "navigate_tab",
			"reason_code": "unsupported_operation",
		}, nil)
	}
	b.mu.Unlock()

	if err := navigator.NavigateTab(ctx, normalizedURL); err != nil {
		selected.dispatchMu.Unlock()
		return PageContext{}, err
	}
	selected.dispatchMu.Unlock()
	// Navigation events are applied under dispatchMu. Flush only after the
	// command releases it, then reacquire it to prevent a concurrent selected-
	// page operation from invalidating the context while it is returned.
	b.flushSession(selected)
	selected.dispatchMu.Lock()
	defer selected.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return PageContext{}, ErrClosed
	}
	if b.selected != selected {
		return PageContext{}, staleSelectionForSession(selected, "selection_changed")
	}
	if err := b.captureSelectionStateErrorLocked(selected, "navigate_tab", "selection_changed"); err != nil {
		return PageContext{}, err
	}
	return selected.context, nil
}

// CreateTab creates a browser page without attaching WebMCP. This is distinct
// from OpenTab so a managed browser can make an ordinary about:blank window
// visible while remaining connected-unselected.
func (b *StatefulBroker) CreateTab(ctx context.Context, request OpenTabRequest) (Target, error) {
	if err := contextError(ctx); err != nil {
		return Target{}, err
	}
	if b == nil {
		return Target{}, ErrClosed
	}
	normalizedURL, err := normalizeOpenTabURL(request.URL)
	if err != nil {
		return Target{}, classified(ErrorInvalidToolInput, "The browser tab URL is invalid.", map[string]any{
			"phase":  "open_tab",
			"reason": "absolute_http_url_required",
		}, err)
	}

	candidate, err := b.candidateFor(ctx, request.BrowserID)
	if err != nil {
		return Target{}, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		return Target{}, err
	}
	opener, ok := handle.(BrowserTabOpener)
	if !ok {
		return Target{}, classified(ErrorBrowserProtocol, "The connected browser cannot open a new tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "unsupported_operation",
		}, nil)
	}
	opened, err := opener.OpenTab(ctx, normalizedURL)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), TargetSelector{BrowserID: candidate.ID}, "open_tab", err); failure != nil {
			return Target{}, failure
		}
		return Target{}, classified(ErrorBrowserProtocol, "The browser could not open the requested tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "create_target_failed",
		}, err)
	}
	if opened.ID == "" {
		return Target{}, classified(ErrorBrowserProtocol, "The browser did not return a target for the new tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "target_id_missing",
		}, nil)
	}
	if opened.BrowserID == "" {
		opened.BrowserID = candidate.ID
	}
	if request.Activate {
		if err := handle.Activate(ctx, opened.ID); err != nil {
			return Target{}, classified(ErrorBrowserProtocol, "The browser could not activate the requested tab.", map[string]any{
				"browser_id": string(candidate.ID),
				"phase":      "open_tab",
				"reason":     "activate_target_failed",
			}, err)
		}
	}
	return opened, nil
}

func normalizeOpenTabURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "about:blank" {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.User != nil || parsed.Hostname() == "" {
		return "", errors.New("absolute HTTP URL required")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", errors.New("absolute HTTP URL required")
	}
	return parsed.String(), nil
}

var _ BrokerTabOpener = (*StatefulBroker)(nil)
var _ BrokerTabCreator = (*StatefulBroker)(nil)
var _ BrokerTabNavigator = (*StatefulBroker)(nil)
