package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
)

func (c *WebMCPOperationsCommand) selectDirectTarget(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
	candidate, target, _, err := c.resolveDirectReplacementTarget(ctx, cmd, values, broker, browser)
	if err != nil {
		return nil, err
	}
	activate := browser.Selection.ActivateTab
	if directFlagChanged(cmd, "activate") {
		activate = values.activate
	}
	page, err := selectDirectTarget(ctx, broker, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}, activate)
	if err != nil {
		return nil, err
	}
	if page.Key.BrowserID == "" {
		page.Key.BrowserID = candidate.ID
	}
	if page.Key.TargetID == "" {
		page.Key.TargetID = target.ID
	}
	if page.Origin == "" {
		page.Origin = target.Origin
	}
	if page.Generation == 0 {
		page.Generation = target.Generation
	}
	data, err := c.contextWithCatalog(ctx, broker, page, false)
	if err != nil {
		return nil, err
	}
	if browser.Selection.Persist {
		if err := c.saveDirectSelection(WebMCPSelection{
			Version:           WebMCPSelectionVersion,
			EndpointID:        string(candidate.ID),
			BrowserID:         string(page.Key.BrowserID),
			BrowserInstanceID: candidate.BrowserInstanceID,
			TargetID:          string(page.Key.TargetID),
			Origin:            safeOrigin(page.Origin),
			ContinuityMarker:  target.ContinuityMarker,
			Generation:        page.Generation,
			SelectedAt:        time.Now().UTC(),
		}); err != nil {
			return nil, fmt.Errorf("persist WebMCP selection: %w", err)
		}
	}
	return data, nil
}

func selectDirectTarget(ctx context.Context, broker webmcp.Broker, selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if selectorWithOptions, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	if activate {
		return webmcp.PageContext{}, webmcpRuntimeUnavailableError("target_activation")
	}
	return broker.Select(ctx, selector)
}

func (c *WebMCPOperationsCommand) ensureDirectSelection(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, broker webmcp.Broker, browser config.BrowserConfig) (webmcp.PageContext, error) {
	candidate, target, _, err := c.resolveDirectTarget(ctx, cmd, values, broker, browser)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	return selectDirectTarget(ctx, broker, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}, browser.Selection.ActivateTab)
}

func selectedDirectContext(ctx context.Context, broker webmcp.Broker, refresh bool) (webmcp.PageContext, error) {
	if refresher, ok := broker.(interface {
		SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
	}); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return broker.Selected(ctx)
}
