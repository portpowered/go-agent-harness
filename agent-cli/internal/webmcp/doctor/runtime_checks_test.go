package doctor

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

type stageCase struct {
	name        string
	mutate      func(*fakeBroker, *config.BrowserConfig)
	code        string
	check       string
	checkStatus string
}

func unverifiedError() error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "unverified", map[string]any{"reason_code": reasonPageToolsUnverified})
}

func discoveryStageCases() []stageCase {
	return []stageCase{
		{name: "discover error", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.discoverErr = errFake }, code: string(webmcp.ErrorEndpointNotFound), check: checkDiscovery, checkStatus: CheckFail},
		{name: "no candidates", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.candidates = nil }, code: string(webmcp.ErrorEndpointNotFound), check: checkDiscovery, checkStatus: CheckFail},
		{name: "remote candidate", mutate: func(b *fakeBroker, _ *config.BrowserConfig) {
			b.candidates[0].Loopback = false
			b.candidates[0].HTTPURL = "http://browser.example:9222"
		}, code: string(webmcp.ErrorRemoteEndpointDenied), check: checkDiscovery, checkStatus: CheckFail},
		{name: "stale browser", mutate: func(_ *fakeBroker, c *config.BrowserConfig) { c.Selection.Browser = "browser-z" }, code: string(webmcp.ErrorStaleSelection), check: checkSelection, checkStatus: CheckFail},
		{name: "ambiguous browser", mutate: func(b *fakeBroker, c *config.BrowserConfig) {
			c.Selection.Browser = ""
			b.candidates = append(b.candidates, webmcp.BrowserCandidate{ID: "browser-b", Loopback: true})
		}, code: string(webmcp.ErrorAmbiguousBrowser), check: checkSelection, checkStatus: CheckFail},
		{name: "version unavailable", mutate: func(b *fakeBroker, _ *config.BrowserConfig) {
			b.candidates[0].Product, b.candidates[0].Protocol = "", ""
		}, code: string(webmcp.ErrorBrowserProtocol), check: checkVersion, checkStatus: CheckFail},
		{name: "targets error", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.targetsErr = errFake }, code: string(webmcp.ErrorEndpointUnreachable), check: checkTargets, checkStatus: CheckFail},
	}
}

func selectionStageCases() []stageCase {
	return []stageCase{
		{name: "denied origin", mutate: func(_ *fakeBroker, c *config.BrowserConfig) { c.Policy.DeniedOrigins = []string{testOrigin} }, code: string(webmcp.ErrorOriginDenied), check: checkPolicy, checkStatus: CheckFail},
		{name: "not allowed origin", mutate: func(_ *fakeBroker, c *config.BrowserConfig) { c.Policy.AllowedOrigins = []string{"https://other.test"} }, code: string(webmcp.ErrorOriginDenied), check: checkPolicy, checkStatus: CheckFail},
		{name: "stale tab", mutate: func(_ *fakeBroker, c *config.BrowserConfig) { c.Selection.Tab = "tab-z" }, code: string(webmcp.ErrorStaleSelection), check: checkSelection, checkStatus: CheckFail},
		{name: "persisted selection missing", mutate: func(_ *fakeBroker, c *config.BrowserConfig) {
			c.Selection.Tab, c.Selection.AutoSelect = "", config.BrowserAutoSelectPersisted
		}, code: string(webmcp.ErrorStaleSelection), check: checkSelection, checkStatus: CheckFail},
		{name: "invalid auto select", mutate: func(_ *fakeBroker, c *config.BrowserConfig) {
			c.Selection.Tab, c.Selection.AutoSelect = "", "sometimes"
		}, code: string(webmcp.ErrorStaleSelection), check: checkSelection, checkStatus: CheckFail},
		{name: "ambiguous single", mutate: func(b *fakeBroker, c *config.BrowserConfig) {
			c.Selection.Tab, c.Selection.AutoSelect = "", config.BrowserAutoSelectSingle
			b.targets = append(b.targets, webmcp.Target{ID: "tab-b", Type: targetTypePage, Origin: testOrigin, Eligible: true})
		}, code: string(webmcp.ErrorAmbiguousTab), check: checkSelection, checkStatus: CheckFail},
		{name: "single without targets", mutate: func(b *fakeBroker, c *config.BrowserConfig) {
			c.Selection.Tab, c.Selection.AutoSelect = "", config.BrowserAutoSelectSingle
			b.targets = nil
		}, code: string(webmcp.ErrorNoEligibleTab), check: checkSelection, checkStatus: CheckFail},
		{name: "off without targets", mutate: func(b *fakeBroker, c *config.BrowserConfig) { c.Selection.Tab = ""; b.targets = nil }, code: string(webmcp.ErrorNoEligibleTab), check: checkSelection, checkStatus: CheckFail},
	}
}

func pageStageCases() []stageCase {
	return []stageCase{
		{name: "attach failure", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.selectErr = errFake }, code: string(webmcp.ErrorTargetAttachFailed), check: checkWebMCP, checkStatus: CheckFail},
		{name: "unverified at select", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.selectErr = unverifiedError() }, code: string(webmcp.ErrorBrowserProtocol), check: checkCatalog, checkStatus: CheckFail},
		{name: "unsupported webmcp", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.page.WebMCPDomainSupported = false }, code: string(webmcp.ErrorUnsupportedWebMCP), check: checkWebMCP, checkStatus: CheckFail},
		{name: "catalog error", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.catalogErr = errFake }, code: string(webmcp.ErrorBrowserProtocol), check: checkCatalog, checkStatus: CheckFail},
		{name: "catalog unverified error", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.catalogErr = unverifiedError() }, code: string(webmcp.ErrorBrowserProtocol), check: checkCatalog, checkStatus: CheckFail},
		{name: "catalog not ready", mutate: func(b *fakeBroker, _ *config.BrowserConfig) { b.catalog.Context.CatalogReady = false }, code: string(webmcp.ErrorBrowserProtocol), check: checkCatalog, checkStatus: CheckFail},
	}
}

func TestDiagnoseRuntimeStageFailures(t *testing.T) {
	cases := append(append(discoveryStageCases(), selectionStageCases()...), pageStageCases()...)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := readyBroker()
			browser := readyBrowser()
			testCase.mutate(broker, &browser)
			report, err := Diagnose(context.Background(), requestFor(browser, broker))
			requireFailure(t, report, err, testCase.code, testCase.check, testCase.checkStatus)
			if report.Status != StatusNotReady {
				t.Fatalf("status = %q, want not_ready", report.Status)
			}
		})
	}
}

func TestDiagnoseUnselectedTargetLeavesPageToolsUnchecked(t *testing.T) {
	browser := readyBrowser()
	browser.Selection.Tab = ""
	report, err := Diagnose(context.Background(), requestFor(browser, readyBroker()))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != StatusNotReady || report.PageTools != ValueNotChecked || len(report.Warnings) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if checkNamed(t, report, checkSelection).Status != CheckWarn || checkNamed(t, report, checkCatalog).Status != CheckSkipped {
		t.Fatalf("checks = %+v", report.Checks)
	}
}

func TestDiagnoseSingleAutoSelectAndLegacyCatalog(t *testing.T) {
	broker := readyBroker()
	broker.catalog.Context = webmcp.PageContext{Connected: true, Ready: true}
	broker.page.Key = webmcp.PageKey{}
	broker.page.Origin, broker.page.Title = "", ""
	browser := readyBrowser()
	browser.Selection.Tab, browser.Selection.AutoSelect = "", config.BrowserAutoSelectSingle
	report, err := Diagnose(context.Background(), requestFor(browser, broker))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Catalog.Evidence != evidenceLegacyReadyState || !report.Catalog.ToolCountKnown {
		t.Fatalf("catalog = %+v", report.Catalog)
	}
	if report.SelectedPage.TargetID != testTargetID || report.SelectedPage.Origin != testOrigin || report.SelectedPage.Title != "Fixture" {
		t.Fatalf("selected page = %+v", report.SelectedPage)
	}
}

func TestDiagnoseUsesVersionSeams(t *testing.T) {
	broker := readyBroker()
	request := requestFor(readyBrowser(), broker)
	baseFactory := request.Factory
	request.Factory = func(browser config.BrowserConfig) (runtime direct.Runtime, err error) {
		runtime, err = baseFactory(browser)
		runtime.VersionFunc = func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
			return webmcp.BrowserVersion{Browser: "Chrome/Version", ProtocolVersion: "1.4"}, nil
		}
		return runtime, err
	}
	report, err := Diagnose(context.Background(), request)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if details := checkNamed(t, report, checkVersion).Details; details["browser"] != "Chrome/Version" || details["protocol"] != "1.4" {
		t.Fatalf("version details = %+v", details)
	}

	request.Factory = func(browser config.BrowserConfig) (runtime direct.Runtime, err error) {
		runtime, err = baseFactory(browser)
		runtime.VersionFunc = func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
			return webmcp.BrowserVersion{}, errFake
		}
		return runtime, err
	}
	report, err = Diagnose(context.Background(), request)
	requireFailure(t, report, err, string(webmcp.ErrorBrowserProtocol), checkVersion, CheckFail)
}

func TestSelectTargetUsesActivationWhenSupported(t *testing.T) {
	broker := &activatingBroker{fakeBroker: readyBroker()}
	target := &webmcp.Target{BrowserID: testBrowserID, ID: testTargetID}
	if _, err := selectTarget(context.Background(), broker, target, true); err != nil {
		t.Fatalf("selectTarget: %v", err)
	}
	if !broker.activated || len(broker.selected) != 0 {
		t.Fatalf("activated=%t plain selects=%d", broker.activated, len(broker.selected))
	}
}

type activatingBroker struct {
	*fakeBroker
	activated bool
}

func (b *activatingBroker) SelectWithOptions(_ context.Context, _ webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.activated = options.Activate
	return b.page, nil
}
