package bootstrap

import (
	"context"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// optionSelector is the production broker's activation-aware selection
// extension; base-interface-only delegates use Select.
type optionSelector interface {
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
}

// verifyEndpoint proves the configured endpoint is reachable without
// selecting or mutating any target.
func verifyEndpoint(ctx context.Context, browser config.BrowserConfig, broker webmcp.Broker) error {
	if broker == nil {
		return webmcp.ErrClosed
	}
	connection := browser.Connection
	_, err := broker.Discover(ctx, webmcp.DiscoverOptions{
		BrowserID:        webmcp.BrowserID(strings.TrimSpace(browser.Selection.Browser)),
		ExplicitOnly:     strings.TrimSpace(connection.CDPURL) != "" || strings.TrimSpace(connection.WSEndpoint) != "",
		AllowProcessScan: connection.AllowProcessScan,
		AllowRemoteCDP:   connection.AllowRemoteCDP,
	})
	return capabilityError(err)
}

// adoptSelection makes a reconciled discovery selection the broker's exact
// selected target.
func adoptSelection(ctx context.Context, broker webmcp.Broker, selected discovery.Selection, activate bool) error {
	if broker == nil {
		return webmcp.ErrClosed
	}
	if strings.TrimSpace(selected.BrowserID) == "" || strings.TrimSpace(selected.TargetID) == "" {
		return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
			"browser_id":          strings.TrimSpace(selected.BrowserID),
			"target_id":           strings.TrimSpace(selected.TargetID),
			"selected_generation": selected.Generation,
			"reason":              "persisted_selection_incomplete",
		})
	}
	selector := webmcp.TargetSelector{
		BrowserID: webmcp.BrowserID(selected.BrowserID),
		TargetID:  webmcp.TargetID(selected.TargetID),
	}
	var err error
	if withOptions, ok := broker.(optionSelector); ok {
		_, err = withOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	} else {
		_, err = broker.Select(ctx, selector)
	}
	// Selection attaches and starts the target event consumer before it waits
	// for affirmative page-tool evidence. A late catalog is therefore an
	// operation-level result: keep the exact connected selection usable for a
	// later model-facing list/retry instead of failing the whole session
	// capability bootstrap. Do not generalize this to every retryable error;
	// discovery, attachment, and lifecycle failures must still fail closed.
	if retryableCatalogDeadline(err) {
		return nil
	}
	return capabilityError(err)
}
