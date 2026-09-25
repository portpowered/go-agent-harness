package doctor

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	messageDomainSupported   = "The CDP WebMCP domain is supported; page-tool readiness is checked separately."
	messageSelectionValid    = "The exact browser and target selection is valid."
	messagePageToolsMissing  = "The selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline."
	evidenceLegacyReadyState = "legacy_ready_context"
)

// selectPage attaches to the selected target and records the selection and
// WebMCP domain checks.
func (d *runtimeDiagnosis) selectPage(selected *webmcp.Target) error {
	page, err := selectTarget(d.ctx, d.runtime.Broker, selected, d.browser.Selection.ActivateTab)
	if err != nil {
		if isPageToolsUnverified(err) {
			return d.report.recordSelectedWithoutPageTools(selected, err)
		}
		d.report.notReady(err, webmcp.ErrorTargetAttachFailed, map[string]any{
			keyBrowserID: string(selected.BrowserID),
			keyTargetID:  string(selected.ID),
			keyPhase:     "select",
		})
		d.report.setCheck(checkSelection, CheckFail, "The selected target could not be attached.", phaseDetails("select"))
		d.report.setCheck(checkWebMCP, CheckFail, "WebMCP enablement failed for the selected target.", phaseDetails("enable"))
		return err
	}
	d.report.markSelected(selectedPage(*selected, page))
	d.report.setCheck(checkSelection, CheckPass, messageSelectionValid, map[string]any{
		keyBrowserID: string(page.Key.BrowserID),
		keyTargetID:  string(page.Key.TargetID),
	})
	domainSupported := page.WebMCPDomainSupported || page.Ready
	if !domainSupported || !page.Connected {
		primary := webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "the selected target does not provide WebMCP", map[string]any{
			keyBrowserID:          string(page.Key.BrowserID),
			keyTargetID:           string(page.Key.TargetID),
			"required_capability": checkWebMCP,
		})
		d.report.notReady(primary, webmcp.ErrorUnsupportedWebMCP, nil)
		d.report.WebMCP = ValueUnsupported
		d.report.WebMCPDomain = d.report.WebMCP
		d.report.PageTools = ValueUnsupported
		d.report.setCheck(checkWebMCP, CheckFail, "The selected target does not support WebMCP.", map[string]any{"supported": false})
		return primary
	}
	d.report.WebMCP = ValueSupported
	d.report.WebMCPDomain = d.report.WebMCP
	d.report.setCheck(checkWebMCP, CheckPass, messageDomainSupported, map[string]any{"supported": true, keyPageTools: "pending"})
	return nil
}

// recordSelectedWithoutPageTools reports a valid selection on a supported
// WebMCP domain whose page tools produced no affirmative catalog evidence.
func (r *Report) recordSelectedWithoutPageTools(selected *webmcp.Target, cause error) error {
	markTargetSelected(r, selected, false)
	r.WebMCP = ValueSupported
	r.WebMCPDomain = r.WebMCP
	r.PageTools = ValueUnverified
	r.Catalog = Catalog{Evidence: ValueUnverified}
	r.setCheck(checkSelection, CheckPass, messageSelectionValid, map[string]any{
		keyBrowserID: string(selected.BrowserID),
		keyTargetID:  string(selected.ID),
	})
	r.setCheck(checkWebMCP, CheckPass, messageDomainSupported, map[string]any{
		"supported":  true,
		keyPageTools: ValueUnverified,
	})
	primary := pageToolsUnverifiedError(cause, selected, 0)
	r.notReady(primary, webmcp.ErrorBrowserProtocol, nil)
	r.setCheck(checkCatalog, CheckFail, messagePageToolsMissing, r.Error.Details)
	return primary
}

// selectedPage merges the attached page context over the listed target,
// falling back to the listing for any identity or display field the context
// leaves empty.
func selectedPage(target webmcp.Target, page webmcp.PageContext) Target {
	selected := targetFromTarget(target)
	selected.Selected = true
	selected.Attached = page.Connected
	selected.WebMCPDomainSupported = page.WebMCPDomainSupported || page.Ready
	selected.PageToolsReady = page.CatalogReady
	selected.PageToolsKnown = page.CatalogReady
	selected.PageToolsEvidence = page.CatalogEvidence
	selected.Origin = normalize.RedactedOrigin(page.Origin)
	selected.Title = normalize.BoundedText(page.Title, maxTitleLength)
	if page.Key.BrowserID != "" {
		selected.BrowserID = string(page.Key.BrowserID)
	}
	if page.Key.TargetID != "" {
		selected.TargetID = string(page.Key.TargetID)
	}
	if selected.Origin == "" {
		selected.Origin = normalize.RedactedOrigin(target.Origin)
	}
	if selected.Title == "" {
		selected.Title = normalize.BoundedText(target.Title, maxTitleLength)
	}
	return selected
}

// checkCatalog synchronizes the selected page's catalog and records whether
// it provides affirmative page-tool evidence.
func (d *runtimeDiagnosis) checkCatalog(selected *webmcp.Target) error {
	catalog, err := d.runtime.Broker.ListTools(d.ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		if isPageToolsUnverified(err) {
			return d.report.recordPageToolsUnverified(err, selected, catalog.Generation)
		}
		d.report.notReady(err, webmcp.ErrorBrowserProtocol, phaseDetails(checkCatalog))
		d.report.setCheck(checkCatalog, CheckFail, "The WebMCP catalog could not be synchronized.", phaseDetails(checkCatalog))
		return err
	}
	d.report.Catalog = catalogFrom(catalog)
	if !d.report.Catalog.Ready {
		return d.report.recordPageToolsUnverified(nil, selected, catalog.Generation)
	}
	d.report.PageTools = "ready"
	d.report.setCheck(checkCatalog, CheckPass, "The WebMCP catalog is ready.", map[string]any{"generation": catalog.Generation, "tool_count": len(catalog.Tools), "evidence": d.report.Catalog.Evidence})
	d.report.Status = StatusReady
	return nil
}

func (r *Report) recordPageToolsUnverified(cause error, selected *webmcp.Target, generation uint64) error {
	primary := pageToolsUnverifiedError(cause, selected, generation)
	r.PageTools = ValueUnverified
	r.notReady(primary, webmcp.ErrorBrowserProtocol, nil)
	r.setCheck(checkCatalog, CheckFail, messagePageToolsMissing, r.Error.Details)
	return primary
}

func catalogFrom(catalog webmcp.ToolCatalogSnapshot) Catalog {
	result := Catalog{
		Ready:          catalog.Context.CatalogReady && catalog.Context.Connected,
		Generation:     catalog.Generation,
		ToolCount:      len(catalog.Tools),
		ToolCountKnown: catalog.Context.CatalogReady,
		Evidence:       catalog.Context.CatalogEvidence,
	}
	// Older injected brokers only expose Ready. Keep the compatibility path
	// while production brokers use CatalogReady as the affirmative evidence
	// boundary.
	if !result.Ready && catalog.Context.Ready && catalog.Context.Connected {
		result.Ready = true
		result.ToolCountKnown = true
		if result.Evidence == "" {
			result.Evidence = evidenceLegacyReadyState
		}
	}
	return result
}
