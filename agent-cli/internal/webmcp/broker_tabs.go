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
	if err := contextError(ctx); err != nil {
		return PageContext{}, err
	}
	if b == nil {
		return PageContext{}, ErrClosed
	}
	normalizedURL, err := normalizeOpenTabURL(request.URL)
	if err != nil {
		return PageContext{}, classified(ErrorInvalidToolInput, "The browser tab URL is invalid.", map[string]any{
			"phase":  "open_tab",
			"reason": "absolute_http_url_required",
		}, err)
	}

	candidate, err := b.candidateFor(ctx, request.BrowserID)
	if err != nil {
		return PageContext{}, err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		return PageContext{}, err
	}
	opener, ok := handle.(BrowserTabOpener)
	if !ok {
		return PageContext{}, classified(ErrorBrowserProtocol, "The connected browser cannot open a new tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "unsupported_operation",
		}, nil)
	}
	opened, err := opener.OpenTab(ctx, normalizedURL)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), TargetSelector{BrowserID: candidate.ID}, "open_tab", err); failure != nil {
			return PageContext{}, failure
		}
		return PageContext{}, classified(ErrorBrowserProtocol, "The browser could not open the requested tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "create_target_failed",
		}, err)
	}
	if opened.ID == "" {
		return PageContext{}, classified(ErrorBrowserProtocol, "The browser did not return a target for the new tab.", map[string]any{
			"browser_id": string(candidate.ID),
			"phase":      "open_tab",
			"reason":     "target_id_missing",
		}, nil)
	}
	return b.selectWithOptions(ctx, TargetSelector{BrowserID: candidate.ID, TargetID: opened.ID}, SelectOptions{Activate: request.Activate})
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
