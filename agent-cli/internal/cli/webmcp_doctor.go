package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/spf13/cobra"
)

const (
	doctorResultVersion = "webmcp.doctor.v1"

	doctorStatusReady                = "ready"
	doctorStatusNotReady             = "not_ready"
	doctorStatusUnavailable          = "unavailable"
	doctorStatusInvalidConfiguration = "invalid_configuration"
	doctorStatusCleanupError         = "cleanup_error"

	doctorCheckPass        = "pass"
	doctorCheckWarn        = "warning"
	doctorCheckFail        = "fail"
	doctorCheckUnavailable = "unavailable"
	doctorCheckSkipped     = "skipped"

	doctorErrorInvalidConfiguration = "invalid_configuration"
	doctorErrorCleanupFailed        = "cleanup_failed"

	doctorTestedChromeRow   = "Stable Chrome for Testing 152.0.7977.64 (mac-arm64, revision 1669021)"
	doctorTestedChromeFlags = "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport"
)

// WebMCPDoctorVersionFunc supplies the browser version/protocol check. It is
// separate from the broker because the broker's stable interface deliberately
// contains only browser operations needed by model-facing tools.
type WebMCPDoctorVersionFunc func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error)

// WebMCPDiscoveryService is the discovery service consumed by the production
// composition. Keeping this interface at the CLI boundary lets
// command tests inject a discovery fake without importing a browser protocol
// package or depending on a concrete service implementation.
type WebMCPDiscoveryService interface {
	DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error)
	ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error)
	Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error)
	Selected() (discovery.Selection, bool)
	RefreshSelection(context.Context) (discovery.Selection, error)
}

// WebMCPDoctorRuntime is the request-scoped set of seams used by doctor.
// Broker is the real stateful broker in production-capable compositions;
// VersionFunc or Catalog supplies the endpoint/version check. Close is
// optional and owns any runtime resources not owned by Broker.
type WebMCPDoctorRuntime struct {
	Broker      webmcp.Broker
	Discovery   WebMCPDiscoveryService
	VersionFunc WebMCPDoctorVersionFunc
	Catalog     webmcp.DevToolsCatalog
	Close       func() error
	// Navigate and PageState are optional run-scoped browser observations used
	// by probe.scenario.v2. Keeping them outside BrowserRuntime preserves the
	// neutral broker contract for callers that do not need probe evidence.
	Navigate  func(context.Context, string) error
	PageState func(context.Context) (json.RawMessage, error)
}

// WebMCPDoctorFactory constructs one diagnostic runtime for a resolved
// browser configuration. Construction is lazy and is never called when
// configuration or endpoint policy validation has already failed.
type WebMCPDoctorFactory func(config.BrowserConfig) (WebMCPDoctorRuntime, error)

// WebMCPRuntimeFactory and WebMCPDoctorRuntimeFactory are descriptive aliases
// for callers that name the injected seam after the runtime rather than the
// command.
type WebMCPRuntimeFactory = WebMCPDoctorFactory
type WebMCPDoctorRuntimeFactory = WebMCPDoctorFactory

// WebMCPDoctorReport is the stable, machine-readable diagnostic result. All
// URL-bearing fields are reduced to a redacted endpoint or origin-only value;
// websocket paths, credentials, query strings, fragments, and page URLs are
// never included.
type WebMCPDoctorReport struct {
	Version       string                 `json:"version"`
	Status        string                 `json:"status"`
	Endpoint      WebMCPDoctorEndpoint   `json:"endpoint"`
	Browsers      []WebMCPDoctorBrowser  `json:"browsers"`
	Targets       []WebMCPDoctorTarget   `json:"targets"`
	PageTargets   int                    `json:"page_targets"`
	EligiblePages int                    `json:"eligible_pages"`
	SelectedPage  *WebMCPDoctorTarget    `json:"selected_page"`
	WebMCP        string                 `json:"webmcp"`
	WebMCPDomain  string                 `json:"webmcp_domain"`
	PageTools     string                 `json:"page_tools"`
	Catalog       WebMCPDoctorCatalog    `json:"catalog"`
	Checks        []WebMCPDoctorCheck    `json:"checks"`
	Warnings      []string               `json:"warnings"`
	Error         *WebMCPDoctorErrorData `json:"error"`
}

// WebMCPDoctorEndpoint describes the configured or discovered endpoint
// without retaining any credential-bearing or websocket-secret material.
type WebMCPDoctorEndpoint struct {
	Source  string `json:"source"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

type WebMCPDoctorBrowser struct {
	ID       string `json:"id"`
	Product  string `json:"product"`
	Protocol string `json:"protocol"`
	Scope    string `json:"scope"`
}

type WebMCPDoctorTarget struct {
	BrowserID             string `json:"browser_id"`
	TargetID              string `json:"target_id"`
	Type                  string `json:"type"`
	Title                 string `json:"title"`
	Origin                string `json:"origin"`
	Eligible              bool   `json:"eligible"`
	EligibilityReason     string `json:"eligibility_reason,omitempty"`
	Attached              bool   `json:"attached"`
	Selected              bool   `json:"selected"`
	WebMCPDomainSupported bool   `json:"webmcp_domain_supported"`
	PageToolsReady        bool   `json:"page_tools_ready"`
	PageToolsKnown        bool   `json:"page_tools_known"`
	PageToolsEvidence     string `json:"page_tools_evidence,omitempty"`
}

type WebMCPDoctorCatalog struct {
	Ready          bool   `json:"ready"`
	Generation     uint64 `json:"generation"`
	ToolCount      int    `json:"tool_count"`
	ToolCountKnown bool   `json:"tool_count_known"`
	Evidence       string `json:"evidence,omitempty"`
}

type WebMCPDoctorCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// WebMCPDoctorErrorData follows the classified error shape used by the
// broker while allowing doctor-only availability and cleanup classifications.
type WebMCPDoctorErrorData struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// WebMCPDoctorError is returned after a report has been rendered. Its report
// is also available to programmatic callers, while Unwrap preserves the
// original classified cause for errors.Is/errors.As checks.
type WebMCPDoctorError struct {
	Report WebMCPDoctorReport
	Cause  error
}

func (e *WebMCPDoctorError) Error() string {
	if e == nil {
		return "webmcp doctor failed"
	}
	if e.Report.Error != nil {
		return fmt.Sprintf("webmcp doctor: %s: %s", e.Report.Error.Code, e.Report.Error.Message)
	}
	return "webmcp doctor failed"
}

func (e *WebMCPDoctorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// WebMCPDoctorCommand implements `agent webmcp doctor`.
type WebMCPDoctorCommand struct {
	globalFlags  *flags.GlobalFlags
	factory      WebMCPDoctorFactory
	json         bool
	browserFlags *flags.BrowserFlags
}

// NewWebMCPDoctorCommand constructs doctor with an optional injected runtime
// factory. The default uses the production discovery and browser runtime.
func NewWebMCPDoctorCommand(globalFlags *flags.GlobalFlags, factories ...WebMCPDoctorFactory) *WebMCPDoctorCommand {
	factory := defaultWebMCPDoctorFactory(globalFlags)
	if len(factories) > 0 && factories[0] != nil {
		factory = factories[0]
	}
	return &WebMCPDoctorCommand{
		globalFlags:  globalFlags,
		factory:      factory,
		browserFlags: flags.NewBrowserFlags(),
	}
}

// Generate returns the Cobra command for `agent webmcp doctor`.
func (c *WebMCPDoctorCommand) Generate() *cobra.Command {
	if c.browserFlags == nil {
		c.browserFlags = flags.NewBrowserFlags()
	}
	cmd := &cobra.Command{
		Use:          "doctor",
		Short:        "Diagnose WebMCP browser readiness",
		Long:         "Diagnose WebMCP browser readiness. Check browser configuration, endpoint reachability, target selection, WebMCP support, catalog readiness, and cleanup without starting a model session.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := c.diagnose(cmd.Context(), cmd)
			if writeErr := writeWebMCPDoctorReport(cmd.OutOrStdout(), report, c.json); writeErr != nil {
				return writeErr
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&c.json, "json", false, "Write one machine-readable JSON diagnostic object")
	registerSessionBrowserFlags(cmd, c.browserFlags)
	return cmd
}

// Run executes doctor without command-line overrides. It is useful to
// embedding callers that already supplied the config directory on GlobalFlags.
func (c *WebMCPDoctorCommand) Run(ctx context.Context, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	report, err := c.diagnose(ctx, nil)
	if writeErr := writeWebMCPDoctorReport(out, report, c.json); writeErr != nil {
		return writeErr
	}
	return err
}

func (c *WebMCPDoctorCommand) diagnose(ctx context.Context, cmd *cobra.Command) (report WebMCPDoctorReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report = newWebMCPDoctorReport()

	var (
		primary        error
		factoryRuntime WebMCPDoctorRuntime
		runtimeOwned   bool
	)
	defer func() {
		if runtimeOwned {
			closeErr := closeWebMCPDoctorRuntime(factoryRuntime)
			if closeErr != nil {
				report.setCheck("cleanup", doctorCheckFail, "Diagnostic cleanup failed.", map[string]any{"phase": "cleanup"})
				if primary == nil {
					primary = closeErr
					report.Status = doctorStatusCleanupError
					report.Error = &WebMCPDoctorErrorData{
						Code:      doctorErrorCleanupFailed,
						Message:   "WebMCP doctor cleanup failed.",
						Retryable: false,
						Details:   map[string]any{"phase": "cleanup"},
					}
				} else {
					primary = errors.Join(primary, closeErr)
					if report.Error == nil {
						report.Error = &WebMCPDoctorErrorData{
							Code:      doctorErrorCleanupFailed,
							Message:   "WebMCP doctor cleanup failed.",
							Retryable: false,
							Details:   map[string]any{"phase": "cleanup"},
						}
					} else if report.Error.Details == nil {
						report.Error.Details = map[string]any{}
					}
					if report.Error != nil {
						report.Error.Details["cleanup_error"] = true
					}
				}
			} else {
				report.setCheck("cleanup", doctorCheckPass, "Diagnostic runtime cleanup completed.", nil)
			}
		}
		if primary != nil {
			err = &WebMCPDoctorError{Report: report, Cause: primary}
		}
	}()

	loaded, loadErr := resolveSessionBrowserConfig(c.globalFlags, cmd, c.browserFlags)
	if loadErr != nil {
		primary = loadErr
		report.Status = doctorStatusInvalidConfiguration
		report.Error = &WebMCPDoctorErrorData{
			Code:      doctorErrorInvalidConfiguration,
			Message:   "Browser configuration is invalid; fix the browser YAML, AGENT_BROWSER__... value, or --browser-* flag.",
			Retryable: false,
			Details:   map[string]any{"phase": "configuration"},
		}
		report.setCheck("configuration", doctorCheckFail, "Browser configuration is invalid.", map[string]any{"phase": "configuration"})
		return report, nil
	}
	if loaded == nil {
		primary = errors.New("browser configuration loader returned nil config")
		report.Status = doctorStatusInvalidConfiguration
		report.Error = &WebMCPDoctorErrorData{
			Code:      doctorErrorInvalidConfiguration,
			Message:   "Browser configuration could not be loaded.",
			Retryable: false,
			Details:   map[string]any{"phase": "configuration"},
		}
		report.setCheck("configuration", doctorCheckFail, "Browser configuration could not be loaded.", nil)
		return report, nil
	}
	report.Endpoint = doctorEndpointFor(loaded.Browser)
	report.setCheck("configuration", doctorCheckPass, "Browser configuration parsed and validated.", nil)
	if loaded.Browser.BrowserBackendEnabled() {
		report.setCheck("activation", doctorCheckPass, "WebMCP is enabled for model sessions.", map[string]any{"backend": loaded.Browser.Tools.Backend})
	} else {
		report.setCheck("activation", doctorCheckWarn, "WebMCP is disabled for model sessions; direct doctor checks remain active.", map[string]any{"backend": loaded.Browser.Tools.Backend, "enabled": false})
		report.addWarning("WebMCP is not enabled for model sessions; use --browser-tools=webmcp with agent session to admit the capability.")
	}

	if endpointErr := validateDoctorEndpoints(loaded.Browser); endpointErr != nil {
		primary = endpointErr
		report.Status = doctorStatusInvalidConfiguration
		report.Error = doctorErrorDataFor(endpointErr, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "endpoint"})
		report.setCheck("endpoint", doctorCheckFail, "The configured browser endpoint is invalid.", map[string]any{"phase": "endpoint"})
		return report, nil
	}
	if report.Endpoint.Scope == "non_loopback" && !loaded.Browser.Connection.AllowRemoteCDP {
		primary = webmcp.NewClassifiedError(webmcp.ErrorRemoteEndpointDenied, "remote browser endpoints require explicit permission", map[string]any{
			"endpoint_kind": endpointKindFor(loaded.Browser),
			"network_class": "non_loopback",
			"required_flag": "browser-allow-remote-cdp",
		})
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorRemoteEndpointDenied, nil)
		report.setCheck("endpoint", doctorCheckFail, "The endpoint is non-loopback and remote CDP permission is disabled.", map[string]any{"required_flag": "browser-allow-remote-cdp"})
		return report, nil
	}
	report.setCheck("endpoint", doctorCheckPass, "Endpoint policy permits the configured discovery scope.", map[string]any{"scope": report.Endpoint.Scope})

	factory := c.factory
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(c.globalFlags)
	}
	factoryRuntime, factoryErr := factory(loaded.Browser)
	runtimeOwned = factoryRuntime.Close != nil || factoryRuntime.Broker != nil
	if factoryErr != nil {
		primary = webmcpRuntimeFactoryError(factoryErr)
		report.Status = doctorStatusUnavailable
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "runtime_factory"})
		report.setCheck("discovery", doctorCheckUnavailable, "The diagnostic runtime could not be constructed.", map[string]any{"phase": "runtime_factory"})
		return report, nil
	}
	if factoryRuntime.Broker == nil {
		primary = webmcpRuntimeUnavailableError("runtime_factory")
		report.Status = doctorStatusUnavailable
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
		report.setCheck("discovery", doctorCheckUnavailable, "The diagnostic runtime returned no broker.", nil)
		return report, nil
	}

	primary = diagnoseWebMCPDoctorRuntime(ctx, loaded.Browser, factoryRuntime, &report)
	return report, nil
}

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

func newWebMCPDoctorReport() WebMCPDoctorReport {
	report := WebMCPDoctorReport{
		Version:      doctorResultVersion,
		Status:       doctorStatusNotReady,
		Warnings:     []string{},
		Checks:       make([]WebMCPDoctorCheck, 0, 11),
		Browsers:     []WebMCPDoctorBrowser{},
		Targets:      []WebMCPDoctorTarget{},
		WebMCP:       "not_checked",
		WebMCPDomain: "not_checked",
		PageTools:    "not_checked",
	}
	for _, name := range []string{"configuration", "activation", "endpoint", "discovery", "version", "targets", "selection", "policy", "webmcp", "catalog", "cleanup"} {
		report.Checks = append(report.Checks, WebMCPDoctorCheck{Name: name, Status: doctorCheckSkipped})
	}
	return report
}

func (r *WebMCPDoctorReport) setCheck(name, status, message string, details map[string]any) {
	if r == nil {
		return
	}
	for index := range r.Checks {
		if r.Checks[index].Name != name {
			continue
		}
		r.Checks[index] = WebMCPDoctorCheck{Name: name, Status: status, Message: message, Details: details}
		return
	}
	r.Checks = append(r.Checks, WebMCPDoctorCheck{Name: name, Status: status, Message: message, Details: details})
}

func (r *WebMCPDoctorReport) addWarning(warning string) {
	if r == nil || warning == "" || len(r.Warnings) >= 8 {
		return
	}
	for _, existing := range r.Warnings {
		if existing == warning {
			return
		}
	}
	r.Warnings = append(r.Warnings, boundedDoctorText(warning, 240))
}

func closeWebMCPDoctorRuntime(runtime WebMCPDoctorRuntime) (err error) {
	if runtime.Broker != nil {
		err = errors.Join(err, callDoctorClose(runtime.Broker.Close))
	}
	if runtime.Close != nil {
		// The runtime hook owns resources outside the broker (for example a
		// version-endpoint client or an adapter transport), so it is called
		// independently and its failure is joined with broker cleanup.
		err = errors.Join(err, callDoctorClose(runtime.Close))
	}
	return err
}

func callDoctorClose(closeFunc func() error) (err error) {
	if closeFunc == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("doctor cleanup panicked")
		}
	}()
	return closeFunc()
}

func doctorEndpointFor(browser config.BrowserConfig) WebMCPDoctorEndpoint {
	endpoint := WebMCPDoctorEndpoint{Source: "none configured", Scope: "unknown"}
	switch {
	case browser.Connection.CDPURL != "":
		endpoint.Source = "explicit HTTP URL"
		endpoint.Address = redactDoctorEndpoint(browser.Connection.CDPURL)
	case browser.Connection.WSEndpoint != "":
		endpoint.Source = "explicit WebSocket URL"
		endpoint.Address = redactDoctorEndpoint(browser.Connection.WSEndpoint)
	case browser.Connection.UserDataDir != "":
		endpoint.Source = "browser profile DevToolsActivePort"
		endpoint.Address = "<profile redacted>"
		endpoint.Scope = "local profile"
	case browser.Connection.AllowProcessScan:
		endpoint.Source = "process discovery"
		endpoint.Scope = "local process"
	}
	if endpoint.Address != "" && endpoint.Scope == "unknown" {
		endpoint.Scope = doctorEndpointScope(endpoint.Address)
	}
	return endpoint
}

func endpointKindFor(browser config.BrowserConfig) string {
	switch {
	case browser.Connection.CDPURL != "":
		return "http"
	case browser.Connection.WSEndpoint != "":
		return "websocket"
	case browser.Connection.UserDataDir != "":
		return "profile"
	default:
		return "discovery"
	}
}

func validateDoctorEndpoints(browser config.BrowserConfig) error {
	for _, endpoint := range []struct {
		name    string
		value   string
		schemes []string
	}{
		{name: "browser.connection.cdp_url", value: browser.Connection.CDPURL, schemes: []string{"http", "https"}},
		{name: "browser.connection.ws_endpoint", value: browser.Connection.WSEndpoint, schemes: []string{"ws", "wss"}},
	} {
		if endpoint.value == "" {
			continue
		}
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || !doctorContainsString(endpoint.schemes, strings.ToLower(parsed.Scheme)) {
			return fmt.Errorf("%s is not a valid browser endpoint", endpoint.name)
		}
		if parsed.User != nil {
			return fmt.Errorf("%s must not contain endpoint credentials", endpoint.name)
		}
	}
	return nil
}

func doctorContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func doctorEndpointScope(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return "loopback"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "loopback"
	}
	return "non_loopback"
}

func redactDoctorEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<redacted endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if parsed.Scheme == "ws" || parsed.Scheme == "wss" {
		parsed.Path = "/<redacted>"
		parsed.RawPath = ""
	}
	return parsed.String()
}

func chooseDoctorCandidate(candidates []webmcp.BrowserCandidate, browserID string) (webmcp.BrowserCandidate, error) {
	if browserID != "" {
		for _, candidate := range candidates {
			if string(candidate.ID) == browserID {
				return candidate, nil
			}
		}
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "browser_not_found",
		})
	}
	if len(candidates) != 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, string(candidate.ID))
		}
		sort.Strings(ids)
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
			"candidate_browser_ids": ids,
		})
	}
	return candidates[0], nil
}

func setBrowserVersion(report *WebMCPDoctorReport, candidate webmcp.BrowserCandidate) {
	if report == nil {
		return
	}
	for index := range report.Browsers {
		if report.Browsers[index].ID == string(candidate.ID) {
			if report.Browsers[index].Product == "" {
				report.Browsers[index].Product = boundedDoctorText(candidate.Product, 160)
			}
			if report.Browsers[index].Protocol == "" {
				report.Browsers[index].Protocol = boundedDoctorText(candidate.Protocol, 80)
			}
			return
		}
	}
}

func doctorVersion(ctx context.Context, runtime WebMCPDoctorRuntime, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error, bool) {
	if runtime.VersionFunc != nil {
		version, err := runtime.VersionFunc(ctx, candidate)
		return version, err, true
	}
	if runtime.Catalog != nil {
		version, err := runtime.Catalog.Version(ctx, candidate)
		return version, err, true
	}
	if candidate.Product != "" || candidate.Protocol != "" {
		return webmcp.BrowserVersion{Browser: candidate.Product, ProtocolVersion: candidate.Protocol, WebSocketDebuggerURL: candidate.BrowserWSURL, BrowserInstanceID: candidate.BrowserInstanceID}, nil, true
	}
	return webmcp.BrowserVersion{}, nil, false
}

func doctorBrowserFromCandidate(candidate webmcp.BrowserCandidate) WebMCPDoctorBrowser {
	scope := "unknown"
	if doctorCandidateIsLoopback(candidate) {
		scope = "loopback"
	} else if candidate.HTTPURL != "" {
		scope = doctorEndpointScope(candidate.HTTPURL)
	} else if candidate.BrowserWSURL != "" {
		scope = doctorEndpointScope(candidate.BrowserWSURL)
	}
	return WebMCPDoctorBrowser{
		ID:       string(candidate.ID),
		Product:  boundedDoctorText(candidate.Product, 160),
		Protocol: boundedDoctorText(candidate.Protocol, 80),
		Scope:    scope,
	}
}

func doctorCandidateIsLoopback(candidate webmcp.BrowserCandidate) bool {
	if candidate.Loopback {
		return true
	}
	if candidate.HTTPURL != "" {
		return doctorEndpointScope(candidate.HTTPURL) == "loopback"
	}
	if candidate.BrowserWSURL != "" {
		return doctorEndpointScope(candidate.BrowserWSURL) == "loopback"
	}
	return false
}

func doctorEndpointForCandidate(candidate webmcp.BrowserCandidate) WebMCPDoctorEndpoint {
	endpoint := WebMCPDoctorEndpoint{Source: "discovered browser endpoint", Scope: "unknown"}
	switch {
	case candidate.HTTPURL != "":
		endpoint.Source = "discovered HTTP URL"
		endpoint.Address = redactDoctorEndpoint(candidate.HTTPURL)
	case candidate.BrowserWSURL != "":
		endpoint.Source = "discovered WebSocket URL"
		endpoint.Address = redactDoctorEndpoint(candidate.BrowserWSURL)
	}
	if endpoint.Address != "" {
		endpoint.Scope = doctorEndpointScope(endpoint.Address)
	} else if candidate.Loopback {
		endpoint.Scope = "loopback"
	}
	return endpoint
}

func doctorTargetsFrom(targets []webmcp.Target, browserID webmcp.BrowserID) ([]WebMCPDoctorTarget, int) {
	normalized := append([]webmcp.Target(nil), targets...)
	for index := range normalized {
		if normalized[index].BrowserID == "" {
			normalized[index].BrowserID = browserID
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		leftBrowser, rightBrowser := normalized[i].BrowserID, normalized[j].BrowserID
		if leftBrowser != rightBrowser {
			return leftBrowser < rightBrowser
		}
		return normalized[i].ID < normalized[j].ID
	})

	result := make([]WebMCPDoctorTarget, 0, len(normalized))
	pageCount := 0
	for _, target := range normalized {
		if target.Type == "" || strings.EqualFold(target.Type, "page") {
			pageCount++
		}
		result = append(result, doctorTargetFromTarget(target))
	}
	return result, pageCount
}

func doctorTargetFromTarget(target webmcp.Target) WebMCPDoctorTarget {
	typeName := target.Type
	if typeName == "" {
		typeName = "page"
	}
	return WebMCPDoctorTarget{
		BrowserID:             string(target.BrowserID),
		TargetID:              string(target.ID),
		Type:                  boundedDoctorText(typeName, 40),
		Title:                 boundedDoctorText(target.Title, 160),
		Origin:                safeOrigin(target.Origin),
		Eligible:              target.Eligible,
		EligibilityReason:     boundedDoctorText(target.EligibilityReason, 160),
		Attached:              target.Attached,
		WebMCPDomainSupported: target.WebMCPDomainSupported,
		PageToolsReady:        target.PageToolsReady,
		PageToolsKnown:        target.PageToolsKnown,
		PageToolsEvidence:     target.PageToolsEvidence,
	}
}

func countEligibleDoctorPages(targets []webmcp.Target) int {
	count := 0
	for _, target := range targets {
		if (target.Type == "" || strings.EqualFold(target.Type, "page")) && target.Eligible {
			count++
		}
	}
	return count
}

func doctorSelectionTargets(targets []webmcp.Target, browser config.BrowserConfig) ([]webmcp.Target, error) {
	eligible := make([]webmcp.Target, 0, len(targets))
	var deniedTarget *webmcp.Target
	explicitDenied := false
	for _, target := range targets {
		if target.Type != "" && !strings.EqualFold(target.Type, "page") {
			continue
		}
		if !target.Eligible {
			continue
		}
		origin := safeOrigin(target.Origin)
		if browser.Selection.Origin != "" && origin != safeOrigin(browser.Selection.Origin) {
			continue
		}
		if deniedOrigin(origin, browser.Policy) {
			if browser.Selection.Tab != "" && string(target.ID) == browser.Selection.Tab {
				explicitDenied = true
			}
			if deniedTarget == nil {
				copyTarget := target
				deniedTarget = &copyTarget
			}
			continue
		}
		eligible = append(eligible, target)
	}
	if len(browser.Policy.AllowedOrigins) > 0 {
		allowed := make([]webmcp.Target, 0, len(eligible))
		for _, target := range eligible {
			if allowedOrigin(safeOrigin(target.Origin), browser.Policy.AllowedOrigins) {
				allowed = append(allowed, target)
				continue
			}
			if browser.Selection.Tab != "" && string(target.ID) == browser.Selection.Tab {
				explicitDenied = true
				deniedTarget = &target
			} else if deniedTarget == nil {
				copyTarget := target
				deniedTarget = &copyTarget
			}
		}
		eligible = allowed
	}
	if (explicitDenied || len(eligible) == 0) && deniedTarget != nil {
		origin := safeOrigin(deniedTarget.Origin)
		return nil, webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is denied by policy", map[string]any{
			"origin_digest": originDigest(origin),
			"policy":        originPolicyName(origin, browser.Policy),
		})
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	return eligible, nil
}

func chooseDoctorTarget(targets []webmcp.Target, selection config.BrowserSelectionConfig) (*webmcp.Target, string, error) {
	if selection.Tab != "" {
		for index := range targets {
			if string(targets[index].ID) == selection.Tab {
				selected := targets[index]
				return &selected, "", nil
			}
		}
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
			"browser_id":          string(selection.Browser),
			"target_id":           selection.Tab,
			"selected_generation": uint64(0),
			"reason":              "target_not_found",
		})
	}
	switch selection.AutoSelect {
	case config.BrowserAutoSelectSingle:
		if len(targets) == 0 {
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{"candidate_count": 0})
		}
		if len(targets) > 1 {
			ids := make([]string, 0, len(targets))
			for _, target := range targets {
				ids = append(ids, string(target.ID))
			}
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
				"browser_id":           string(targets[0].BrowserID),
				"candidate_target_ids": ids,
			})
		}
		selected := targets[0]
		return &selected, "", nil
	case config.BrowserAutoSelectPersisted:
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "persisted browser target selection is not current", map[string]any{
			"browser_id":          string(selection.Browser),
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "persisted_selection_missing",
		})
	case config.BrowserAutoSelectOff, "":
		if len(targets) == 0 {
			return nil, "", webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{"candidate_count": 0})
		}
		return nil, "No target selected; run `agent webmcp tabs` and `agent webmcp select` or set browser.selection.auto_select.", nil
	default:
		return nil, "", webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "browser target auto-selection is invalid", map[string]any{"reason": "invalid_auto_select"})
	}
}

func selectDoctorTarget(ctx context.Context, broker webmcp.Broker, target *webmcp.Target, activate bool) (webmcp.PageContext, error) {
	selector := webmcp.TargetSelector{BrowserID: target.BrowserID, TargetID: target.ID}
	if selectorWithOptions, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	return broker.Select(ctx, selector)
}

func deniedOrigin(origin string, policy config.BrowserPolicyConfig) bool {
	for _, denied := range policy.DeniedOrigins {
		if safeOrigin(denied) == origin {
			return true
		}
	}
	return false
}

func allowedOrigin(origin string, allowed []string) bool {
	for _, value := range allowed {
		if safeOrigin(value) == origin {
			return true
		}
	}
	return false
}

func originPolicyName(origin string, policy config.BrowserPolicyConfig) string {
	if deniedOrigin(origin, policy) {
		return "denied_origins"
	}
	return "allowed_origins"
}

func safeOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	}
	cleaned := raw
	if index := strings.IndexAny(cleaned, "?#"); index >= 0 {
		cleaned = cleaned[:index]
	}
	return boundedDoctorText(cleaned, 200)
}

func originDigest(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(digest[:])
}

func boundedDoctorText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r >= ' ' {
			return r
		}
		return -1
	}, value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}

func doctorErrorDataFor(err error, fallback webmcp.ErrorCode, details map[string]any) *WebMCPDoctorErrorData {
	result := webmcp.ResultErrorFor(err, fallback, details)
	return &WebMCPDoctorErrorData{Code: result.Code, Message: result.Message, Retryable: result.Retryable, Details: result.Details}
}

func writeWebMCPDoctorReport(out io.Writer, report WebMCPDoctorReport, asJSON bool) error {
	if out == nil {
		return errors.New("webmcp doctor output writer is required")
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return fmt.Errorf("write WebMCP doctor JSON: %w", err)
		}
		return nil
	}
	return writeWebMCPDoctorHuman(out, report)
}

func writeWebMCPDoctorHuman(out io.Writer, report WebMCPDoctorReport) error {
	var builder strings.Builder
	fmt.Fprintf(&builder, "WebMCP doctor: %s\n", report.Status)
	fmt.Fprintf(&builder, "Endpoint source: %s\n", report.Endpoint.Source)
	address := report.Endpoint.Address
	if address == "" {
		address = "none"
	}
	fmt.Fprintf(&builder, "Endpoint:        %s\n", address)
	fmt.Fprintf(&builder, "Scope:           %s\n", report.Endpoint.Scope)
	if len(report.Browsers) == 0 {
		builder.WriteString("Browser:         none\n")
	} else {
		for index, browser := range report.Browsers {
			label := "Browser"
			if index > 0 {
				label = fmt.Sprintf("Browser %d", index+1)
			}
			fmt.Fprintf(&builder, "%s:         %s id=%s\n", label, displayDoctorValue(browser.Product, "unknown"), browser.ID)
			fmt.Fprintf(&builder, "Protocol:        %s\n", displayDoctorValue(browser.Protocol, "unknown"))
		}
	}
	fmt.Fprintf(&builder, "WebMCP domain:   %s\n", report.WebMCP)
	fmt.Fprintf(&builder, "Page tools:      %s\n", displayDoctorValue(report.PageTools, "not_checked"))
	catalogStatus := "unverified"
	if report.Catalog.Ready {
		catalogStatus = "ready"
	}
	fmt.Fprintf(&builder, "Catalog:         %s (%d tools)\n", catalogStatus, report.Catalog.ToolCount)
	fmt.Fprintf(&builder, "Page targets:    %d\n", report.PageTargets)
	fmt.Fprintf(&builder, "Eligible pages:  %d\n", report.EligiblePages)
	if report.SelectedPage == nil {
		builder.WriteString("Selected page:   none\n")
	} else {
		fmt.Fprintf(&builder, "Selected page:   %s/%s (%q, %s)\n", report.SelectedPage.BrowserID, report.SelectedPage.TargetID, report.SelectedPage.Title, displayDoctorValue(report.SelectedPage.Origin, "origin unknown"))
	}
	builder.WriteString("Checks:\n")
	for _, check := range report.Checks {
		fmt.Fprintf(&builder, "  %-14s %s", check.Name+":", check.Status)
		if check.Message != "" {
			fmt.Fprintf(&builder, " — %s", check.Message)
		}
		builder.WriteByte('\n')
	}
	if len(report.Warnings) > 0 {
		builder.WriteString("Warnings:\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&builder, "  - %s\n", warning)
		}
	}
	if report.Error != nil {
		fmt.Fprintf(&builder, "Error:           %s — %s\n", report.Error.Code, report.Error.Message)
	}
	if _, err := io.WriteString(out, builder.String()); err != nil {
		return fmt.Errorf("write WebMCP doctor report: %w", err)
	}
	return nil
}

func displayDoctorValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
