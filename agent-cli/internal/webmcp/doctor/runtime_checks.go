package doctor

import (
	"context"
	"sort"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

// runtimeDiagnosis carries one run's browser-facing checks. Each stage
// records its check and returns the primary failure that ends the run. It
// lives only for the duration of one diagnoseRuntime call, so it holds that
// call's context.
type runtimeDiagnosis struct {
	ctx     context.Context
	browser config.BrowserConfig
	runtime direct.Runtime
	report  *Report
}

// diagnoseRuntime runs discovery, version, target, policy, selection,
// WebMCP, and catalog checks against the constructed runtime.
func diagnoseRuntime(ctx context.Context, browser config.BrowserConfig, runtime direct.Runtime, report *Report) error {
	diagnosis := &runtimeDiagnosis{ctx: ctx, browser: browser, runtime: runtime, report: report}
	candidates, err := diagnosis.discover()
	if err != nil {
		return err
	}
	candidate, err := diagnosis.checkVersion(candidates)
	if err != nil {
		return err
	}
	targets, err := diagnosis.listTargets(candidate)
	if err != nil {
		return err
	}
	selected, err := diagnosis.chooseSelection(candidate, targets)
	if err != nil || selected == nil {
		return err
	}
	if err := diagnosis.selectPage(selected); err != nil {
		return err
	}
	return diagnosis.checkCatalog(selected)
}

func (d *runtimeDiagnosis) discover() ([]webmcp.BrowserCandidate, error) {
	connection := d.browser.Connection
	candidates, err := d.runtime.Broker.Discover(d.ctx, webmcp.DiscoverOptions{
		BrowserID:        webmcp.BrowserID(d.browser.Selection.Browser),
		ExplicitOnly:     connection.CDPURL != "" || connection.WSEndpoint != "",
		AllowProcessScan: connection.AllowProcessScan,
		AllowRemoteCDP:   connection.AllowRemoteCDP,
	})
	if err != nil {
		d.report.notReady(err, webmcp.ErrorEndpointNotFound, phaseDetails(checkDiscovery))
		d.report.setCheck(checkDiscovery, CheckFail, "Browser endpoint discovery failed.", phaseDetails(checkDiscovery))
		return nil, err
	}
	if len(candidates) == 0 {
		primary := webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": direct.EndpointKind(d.browser),
			"source":        d.report.Endpoint.Source,
		})
		d.report.notReady(primary, webmcp.ErrorEndpointNotFound, nil)
		d.report.setCheck(checkDiscovery, CheckFail, "No browser endpoint was discovered.", map[string]any{keyCandidateCount: 0})
		return nil, primary
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, candidate := range candidates {
		if !candidateIsLoopback(candidate) && !connection.AllowRemoteCDP {
			primary := remoteEndpointDeniedError(d.browser)
			d.report.notReady(primary, webmcp.ErrorRemoteEndpointDenied, nil)
			d.report.setCheck(checkDiscovery, CheckFail, "Discovery returned a non-loopback browser while remote CDP is disabled.", map[string]any{keyRequiredFlag: requiredRemoteFlag})
			return nil, primary
		}
		d.report.Browsers = append(d.report.Browsers, browserFromCandidate(candidate))
	}
	if d.report.Endpoint.Address == "" {
		d.report.Endpoint = EndpointForCandidate(candidates[0])
	}
	d.report.setCheck(checkDiscovery, CheckPass, "Browser endpoint discovered.", map[string]any{keyCandidateCount: len(candidates)})
	return candidates, nil
}

// checkVersion chooses the browser and records its product and protocol.
func (d *runtimeDiagnosis) checkVersion(candidates []webmcp.BrowserCandidate) (webmcp.BrowserCandidate, error) {
	candidate, err := chooseCandidate(candidates, d.browser.Selection.Browser)
	if err != nil {
		d.report.notReady(err, webmcp.ErrorAmbiguousBrowser, phaseDetails("browser_selection"))
		d.report.setCheck(checkSelection, CheckFail, "Browser selection is ambiguous or stale.", phaseDetails("browser_selection"))
		return webmcp.BrowserCandidate{}, err
	}
	setBrowserVersion(d.report, candidate)
	version, available, err := browserVersion(d.ctx, d.runtime, candidate)
	if err != nil {
		d.report.notReady(err, webmcp.ErrorBrowserProtocol, phaseDetails(checkVersion))
		d.report.setCheck(checkVersion, CheckFail, "The browser protocol version check failed.", phaseDetails(checkVersion))
		return webmcp.BrowserCandidate{}, err
	}
	if !available {
		primary := direct.RuntimeUnavailableError(checkVersion)
		d.report.notReady(primary, webmcp.ErrorBrowserProtocol, nil)
		d.report.setCheck(checkVersion, CheckFail, "Browser protocol metadata is unavailable.", phaseDetails(checkVersion))
		return webmcp.BrowserCandidate{}, primary
	}
	d.report.applyVersion(candidate.ID, version)
	product, protocol := "", ""
	if index := d.report.browserIndex(candidate.ID); index >= 0 {
		product = d.report.Browsers[index].Product
		protocol = d.report.Browsers[index].Protocol
	}
	d.report.setCheck(checkVersion, CheckPass, "Browser and DevTools protocol metadata are available.", map[string]any{"browser": product, "protocol": protocol})
	return candidate, nil
}

// listTargets records the browser's targets, filling any missing browser ID
// from the candidate.
func (d *runtimeDiagnosis) listTargets(candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	targets, err := d.runtime.Broker.ListTargets(d.ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		d.report.notReady(err, webmcp.ErrorEndpointUnreachable, phaseDetails(checkTargets))
		d.report.setCheck(checkTargets, CheckFail, "Browser target discovery failed.", phaseDetails(checkTargets))
		return nil, err
	}
	d.report.Targets, d.report.PageTargets = targetsFrom(targets, candidate.ID)
	for index := range targets {
		if targets[index].BrowserID == "" {
			targets[index].BrowserID = candidate.ID
		}
	}
	d.report.EligiblePages = countEligiblePages(targets)
	d.report.setCheck(checkTargets, CheckPass, "Browser targets are available.", map[string]any{"page_targets": d.report.PageTargets, "eligible_pages": d.report.EligiblePages})
	return targets, nil
}

// chooseSelection applies origin policy and resolves the configured target.
// A nil target with a nil error means the run ends without a selection.
func (d *runtimeDiagnosis) chooseSelection(candidate webmcp.BrowserCandidate, targets []webmcp.Target) (*webmcp.Target, error) {
	eligible, err := selectionTargets(targets, d.browser)
	if err != nil {
		d.report.notReady(err, webmcp.ErrorOriginDenied, phaseDetails(checkPolicy))
		d.report.setCheck(checkPolicy, CheckFail, "The selected origin is denied by browser policy.", phaseDetails(checkPolicy))
		return nil, err
	}
	d.report.setCheck(checkPolicy, CheckPass, "Origin policy permits the eligible target set.", map[string]any{"eligible_pages": len(eligible)})

	selected, warning, err := chooseTarget(eligible, d.browser.Selection)
	if err != nil {
		d.report.notReady(err, webmcp.ErrorNoEligibleTab, phaseDetails("target_selection"))
		d.report.setCheck(checkSelection, CheckFail, "No valid WebMCP target selection is available.", phaseDetails("target_selection"))
		return nil, err
	}
	if warning != "" {
		d.report.recordUnselected(warning)
		return nil, nil
	}
	if selected == nil {
		primary := webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, messageNoEligibleTab, map[string]any{
			keyBrowserID:      string(candidate.ID),
			"filters":         map[string]any{"origin": d.browser.Selection.Origin},
			keyCandidateCount: len(eligible),
		})
		d.report.notReady(primary, webmcp.ErrorNoEligibleTab, nil)
		d.report.setCheck(checkSelection, CheckFail, "No eligible WebMCP target was found.", map[string]any{keyCandidateCount: len(eligible)})
		return nil, primary
	}
	return selected, nil
}

// recordUnselected reports a ready endpoint whose page tools stay unchecked
// until an exact tab is selected.
func (r *Report) recordUnselected(warning string) {
	r.PageTools = ValueNotChecked
	r.Catalog = Catalog{Evidence: ValueNotChecked}
	r.addWarning("Endpoint is ready, but page tools are unverified; select a tab before checking them: " + warning)
	r.setCheck(checkSelection, CheckWarn, "No target was selected; endpoint is ready, but page tools are unverified until an exact tab is selected.", map[string]any{
		"selected":           false,
		keyPageTools:         ValueNotChecked,
		"selection_required": true,
		"selection_action":   "agent webmcp tabs then agent webmcp select",
	})
	r.setCheck(checkWebMCP, CheckSkipped, "Select an eligible target to probe WebMCP.enable.", map[string]any{
		"domain":     ValueNotChecked,
		keyPageTools: ValueNotChecked,
	})
	r.setCheck(checkCatalog, CheckSkipped, "Page tools are unverified until a target is selected and checked.", map[string]any{
		"catalog":          ValueNotChecked,
		keyPageTools:       ValueNotChecked,
		"selection_action": "agent webmcp select",
	})
	r.Status = StatusNotReady
}
