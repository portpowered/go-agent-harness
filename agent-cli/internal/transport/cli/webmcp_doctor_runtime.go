package cli

import (
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"sort"
)

func diagnoseWebMCPDoctorRuntime(ctx context.Context, browser config.BrowserConfig, runtime WebMCPDoctorRuntime, report *WebMCPDoctorReport) error {
	discoverOptions := webmcp.DiscoverOptions{
		BrowserID:        webmcp.BrowserID(browser.Selection.Browser),
		ExplicitOnly:     browser.Connection.CDPURL != "" || browser.Connection.WSEndpoint != "",
		AllowProcessScan: browser.Connection.AllowProcessScan,
		AllowRemoteCDP:   browser.Connection.AllowRemoteCDP,
	}
	candidates, discoverErr := runtime.Broker.Discover(ctx, discoverOptions)
	if discoverErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(discoverErr, webmcp.ErrorEndpointNotFound, map[string]any{"phase": "discovery"})
		report.setCheck("discovery", doctorCheckFail, "Browser endpoint discovery failed.", map[string]any{"phase": "discovery"})
		return discoverErr
	}
	if len(candidates) == 0 {
		primary := webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": endpointKindFor(browser),
			"source":        report.Endpoint.Source,
		})
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorEndpointNotFound, nil)
		report.setCheck("discovery", doctorCheckFail, "No browser endpoint was discovered.", map[string]any{"candidate_count": 0})
		return primary
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, candidate := range candidates {
		if !doctorCandidateIsLoopback(candidate) && !browser.Connection.AllowRemoteCDP {
			primary := webmcp.NewClassifiedError(webmcp.ErrorRemoteEndpointDenied, "remote browser endpoints require explicit permission", map[string]any{
				"endpoint_kind": endpointKindFor(browser),
				"network_class": "non_loopback",
				"required_flag": "browser-allow-remote-cdp",
			})
			report.Status = doctorStatusNotReady
			report.Error = doctorErrorDataFor(primary, webmcp.ErrorRemoteEndpointDenied, nil)
			report.setCheck("discovery", doctorCheckFail, "Discovery returned a non-loopback browser while remote CDP is disabled.", map[string]any{"required_flag": "browser-allow-remote-cdp"})
			return primary
		}
		report.Browsers = append(report.Browsers, doctorBrowserFromCandidate(candidate))
	}
	if report.Endpoint.Address == "" {
		report.Endpoint = doctorEndpointForCandidate(candidates[0])
	}
	report.setCheck("discovery", doctorCheckPass, "Browser endpoint discovered.", map[string]any{"candidate_count": len(candidates)})

	candidate, candidateErr := chooseDoctorCandidate(candidates, browser.Selection.Browser)
	if candidateErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(candidateErr, webmcp.ErrorAmbiguousBrowser, map[string]any{"phase": "browser_selection"})
		report.setCheck("selection", doctorCheckFail, "Browser selection is ambiguous or stale.", map[string]any{"phase": "browser_selection"})
		return candidateErr
	}
	setBrowserVersion(report, candidate)
	version, versionErr, versionAvailable := doctorVersion(ctx, runtime, candidate)
	if versionErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(versionErr, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "version"})
		report.setCheck("version", doctorCheckFail, "The browser protocol version check failed.", map[string]any{"phase": "version"})
		return versionErr
	}
	if !versionAvailable {
		primary := webmcpRuntimeUnavailableError("version")
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
		report.setCheck("version", doctorCheckFail, "Browser protocol metadata is unavailable.", map[string]any{"phase": "version"})
		return primary
	}
	if version.Browser != "" || version.ProtocolVersion != "" {
		for index := range report.Browsers {
			if report.Browsers[index].ID == string(candidate.ID) {
				if version.Browser != "" {
					report.Browsers[index].Product = boundedDoctorText(version.Browser, 160)
				}
				if version.ProtocolVersion != "" {
					report.Browsers[index].Protocol = boundedDoctorText(version.ProtocolVersion, 80)
				}
			}
		}
	}
	versionBrowser, versionProtocol := "", ""
	for _, browser := range report.Browsers {
		if browser.ID == string(candidate.ID) {
			versionBrowser = browser.Product
			versionProtocol = browser.Protocol
			break
		}
	}
	report.setCheck("version", doctorCheckPass, "Browser and DevTools protocol metadata are available.", map[string]any{"browser": versionBrowser, "protocol": versionProtocol})

	targets, targetErr := runtime.Broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if targetErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(targetErr, webmcp.ErrorEndpointUnreachable, map[string]any{"phase": "targets"})
		report.setCheck("targets", doctorCheckFail, "Browser target discovery failed.", map[string]any{"phase": "targets"})
		return targetErr
	}
	report.Targets, report.PageTargets = doctorTargetsFrom(targets, candidate.ID)
	for index := range targets {
		if targets[index].BrowserID == "" {
			targets[index].BrowserID = candidate.ID
		}
	}
	report.EligiblePages = countEligibleDoctorPages(targets)
	report.setCheck("targets", doctorCheckPass, "Browser targets are available.", map[string]any{"page_targets": report.PageTargets, "eligible_pages": report.EligiblePages})

	selectionTargets, policyErr := doctorSelectionTargets(targets, browser)
	if policyErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(policyErr, webmcp.ErrorOriginDenied, map[string]any{"phase": "policy"})
		report.setCheck("policy", doctorCheckFail, "The selected origin is denied by browser policy.", map[string]any{"phase": "policy"})
		return policyErr
	}
	report.setCheck("policy", doctorCheckPass, "Origin policy permits the eligible target set.", map[string]any{"eligible_pages": len(selectionTargets)})

	selectedTarget, warning, selectErr := chooseDoctorTarget(selectionTargets, browser.Selection)
	if selectErr != nil {
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(selectErr, webmcp.ErrorNoEligibleTab, map[string]any{"phase": "target_selection"})
		report.setCheck("selection", doctorCheckFail, "No valid WebMCP target selection is available.", map[string]any{"phase": "target_selection"})
		return selectErr
	}
	if warning != "" {
		report.PageTools = "not_checked"
		report.Catalog = WebMCPDoctorCatalog{Evidence: "not_checked"}
		report.addWarning("Endpoint is ready, but page tools are unverified; select a tab before checking them: " + warning)
		report.setCheck("selection", doctorCheckWarn, "No target was selected; endpoint is ready, but page tools are unverified until an exact tab is selected.", map[string]any{
			"selected":           false,
			"page_tools":         "not_checked",
			"selection_required": true,
			"selection_action":   "agent webmcp tabs then agent webmcp select",
		})
		report.setCheck("webmcp", doctorCheckSkipped, "Select an eligible target to probe WebMCP.enable.", map[string]any{
			"domain":     "not_checked",
			"page_tools": "not_checked",
		})
		report.setCheck("catalog", doctorCheckSkipped, "Page tools are unverified until a target is selected and checked.", map[string]any{
			"catalog":          "not_checked",
			"page_tools":       "not_checked",
			"selection_action": "agent webmcp select",
		})
		report.Status = doctorStatusNotReady
		return nil
	}
	if selectedTarget == nil {
		primary := webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{
			"browser_id":      string(candidate.ID),
			"filters":         map[string]any{"origin": browser.Selection.Origin},
			"candidate_count": len(selectionTargets),
		})
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorNoEligibleTab, nil)
		report.setCheck("selection", doctorCheckFail, "No eligible WebMCP target was found.", map[string]any{"candidate_count": len(selectionTargets)})
		return primary
	}

	selectedContext, selectionErr := selectDoctorTarget(ctx, runtime.Broker, selectedTarget, browser.Selection.ActivateTab)
	if selectionErr != nil {
		if isDoctorPageToolsUnverified(selectionErr) {
			markDoctorTargetSelected(report, selectedTarget, false)
			report.WebMCP = "supported"
			report.WebMCPDomain = report.WebMCP
			report.PageTools = "unverified"
			report.Catalog = WebMCPDoctorCatalog{Evidence: "unverified"}
			report.setCheck("selection", doctorCheckPass, "The exact browser and target selection is valid.", map[string]any{
				"browser_id": string(selectedTarget.BrowserID),
				"target_id":  string(selectedTarget.ID),
			})
			report.setCheck("webmcp", doctorCheckPass, "The CDP WebMCP domain is supported; page-tool readiness is checked separately.", map[string]any{
				"supported":  true,
				"page_tools": "unverified",
			})
			primary := doctorPageToolsUnverifiedError(selectionErr, selectedTarget, 0)
			report.Status = doctorStatusNotReady
			report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
			report.setCheck("catalog", doctorCheckFail, "The selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline.", report.Error.Details)
			return primary
		}
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(selectionErr, webmcp.ErrorTargetAttachFailed, map[string]any{
			"browser_id": string(selectedTarget.BrowserID),
			"target_id":  string(selectedTarget.ID),
			"phase":      "select",
		})
		report.setCheck("selection", doctorCheckFail, "The selected target could not be attached.", map[string]any{"phase": "select"})
		report.setCheck("webmcp", doctorCheckFail, "WebMCP enablement failed for the selected target.", map[string]any{"phase": "enable"})
		return selectionErr
	}
	selectedPage := doctorTargetFromTarget(*selectedTarget)
	selectedPage.Selected = true
	selectedPage.Attached = selectedContext.Connected
	selectedPage.WebMCPDomainSupported = selectedContext.WebMCPDomainSupported || selectedContext.Ready
	selectedPage.PageToolsReady = selectedContext.CatalogReady
	selectedPage.PageToolsKnown = selectedContext.CatalogReady
	selectedPage.PageToolsEvidence = selectedContext.CatalogEvidence
	selectedPage.Origin = safeOrigin(selectedContext.Origin)
	selectedPage.Title = boundedDoctorText(selectedContext.Title, 160)
	if selectedContext.Key.BrowserID != "" {
		selectedPage.BrowserID = string(selectedContext.Key.BrowserID)
	}
	if selectedContext.Key.TargetID != "" {
		selectedPage.TargetID = string(selectedContext.Key.TargetID)
	}
	if selectedPage.Origin == "" {
		selectedPage.Origin = safeOrigin(selectedTarget.Origin)
	}
	if selectedPage.Title == "" {
		selectedPage.Title = boundedDoctorText(selectedTarget.Title, 160)
	}
	report.SelectedPage = &selectedPage
	for index := range report.Targets {
		if report.Targets[index].BrowserID == selectedPage.BrowserID && report.Targets[index].TargetID == selectedPage.TargetID {
			report.Targets[index].Selected = true
			report.Targets[index].Attached = selectedPage.Attached
		}
	}
	report.setCheck("selection", doctorCheckPass, "The exact browser and target selection is valid.", map[string]any{
		"browser_id": string(selectedContext.Key.BrowserID),
		"target_id":  string(selectedContext.Key.TargetID),
	})
	domainSupported := selectedContext.WebMCPDomainSupported || selectedContext.Ready
	if domainSupported && selectedContext.Connected {
		report.WebMCP = "supported"
		report.WebMCPDomain = report.WebMCP
		report.setCheck("webmcp", doctorCheckPass, "The CDP WebMCP domain is supported; page-tool readiness is checked separately.", map[string]any{"supported": true, "page_tools": "pending"})
	} else {
		primary := webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "the selected target does not provide WebMCP", map[string]any{
			"browser_id":          string(selectedContext.Key.BrowserID),
			"target_id":           string(selectedContext.Key.TargetID),
			"required_capability": "webmcp",
		})
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorUnsupportedWebMCP, nil)
		report.WebMCP = "unsupported"
		report.WebMCPDomain = report.WebMCP
		report.PageTools = "unsupported"
		report.setCheck("webmcp", doctorCheckFail, "The selected target does not support WebMCP.", map[string]any{"supported": false})
		return primary
	}

	catalog, catalogErr := runtime.Broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	if catalogErr != nil {
		if isDoctorPageToolsUnverified(catalogErr) {
			primary := doctorPageToolsUnverifiedError(catalogErr, selectedTarget, catalog.Generation)
			report.PageTools = "unverified"
			report.Status = doctorStatusNotReady
			report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
			report.setCheck("catalog", doctorCheckFail, "The selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline.", report.Error.Details)
			return primary
		}
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(catalogErr, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "catalog"})
		report.setCheck("catalog", doctorCheckFail, "The WebMCP catalog could not be synchronized.", map[string]any{"phase": "catalog"})
		return catalogErr
	}
	report.Catalog = WebMCPDoctorCatalog{
		Ready:          catalog.Context.CatalogReady && catalog.Context.Connected,
		Generation:     catalog.Generation,
		ToolCount:      len(catalog.Tools),
		ToolCountKnown: catalog.Context.CatalogReady,
		Evidence:       catalog.Context.CatalogEvidence,
	}
	// Older injected brokers only expose Ready. Keep the compatibility path
	// while production brokers use CatalogReady as the affirmative evidence
	// boundary.
	if !report.Catalog.Ready && catalog.Context.Ready && catalog.Context.Connected {
		report.Catalog.Ready = true
		report.Catalog.ToolCountKnown = true
		if report.Catalog.Evidence == "" {
			report.Catalog.Evidence = "legacy_ready_context"
		}
	}
	if !report.Catalog.Ready {
		report.PageTools = "unverified"
		primary := doctorPageToolsUnverifiedError(nil, selectedTarget, catalog.Generation)
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
		report.setCheck("catalog", doctorCheckFail, "The selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline.", report.Error.Details)
		return primary
	}
	report.PageTools = "ready"
	report.setCheck("catalog", doctorCheckPass, "The WebMCP catalog is ready.", map[string]any{"generation": catalog.Generation, "tool_count": len(catalog.Tools), "evidence": report.Catalog.Evidence})
	report.Status = doctorStatusReady
	return nil
}
