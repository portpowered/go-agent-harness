package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

const (
	testBrowserID = "browser-a"
	testTargetID  = "tab-a"
	testOrigin    = "https://fixture.test"
	testLoopback  = "http://127.0.0.1:9222"
)

// fakeError is a constant error value for scripted failures.
type fakeError string

func (e fakeError) Error() string { return string(e) }

const errFake fakeError = "fake failure"

// fakeBroker is a scripted broker for the doctor stages.
type fakeBroker struct {
	candidates  []webmcp.BrowserCandidate
	discoverErr error
	targets     []webmcp.Target
	targetsErr  error
	page        webmcp.PageContext
	selectErr   error
	catalog     webmcp.ToolCatalogSnapshot
	catalogErr  error
	closeErr    error
	selected    []webmcp.TargetSelector
}

func (b *fakeBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), b.discoverErr
}

func (b *fakeBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return append([]webmcp.Target(nil), b.targets...), b.targetsErr
}

func (b *fakeBroker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.selected = append(b.selected, selector)
	return b.page, b.selectErr
}

func (b *fakeBroker) Selected(context.Context) (webmcp.PageContext, error) { return b.page, nil }

func (b *fakeBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return b.catalog, b.catalogErr
}

func (b *fakeBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{}, errFake
}

func (b *fakeBroker) Cancel(context.Context, webmcp.CancelRequest) error { return errFake }

func (b *fakeBroker) Watch(context.Context) <-chan webmcp.BrokerEvent { return nil }

func (b *fakeBroker) Close() error { return b.closeErr }

// readyBroker returns a broker whose every stage succeeds for one loopback
// browser and one eligible page.
func readyBroker() *fakeBroker {
	return &fakeBroker{
		candidates: []webmcp.BrowserCandidate{{ID: testBrowserID, Product: "Chrome/Test", Protocol: "1.3", HTTPURL: testLoopback, Loopback: true}},
		targets: []webmcp.Target{
			{BrowserID: testBrowserID, ID: testTargetID, Type: targetTypePage, Title: "Fixture", Origin: testOrigin, Eligible: true},
			{ID: "worker-a", Type: "service_worker"},
		},
		page: webmcp.PageContext{
			Key:                   webmcp.PageKey{BrowserID: testBrowserID, TargetID: testTargetID},
			Title:                 "Fixture",
			Origin:                testOrigin + "/path?secret=1",
			Connected:             true,
			WebMCPDomainSupported: true,
		},
		catalog: webmcp.ToolCatalogSnapshot{
			Context:    webmcp.PageContext{Connected: true, CatalogReady: true, CatalogEvidence: "producer"},
			Generation: 3,
			Tools:      []webmcp.ToolDescriptor{{Name: "read_state"}},
		},
	}
}

func readyBrowser() config.BrowserConfig {
	return config.BrowserConfig{
		Tools:      config.BrowserToolsConfig{Enabled: true, Backend: config.BrowserToolsBackendWebMCP},
		Connection: config.BrowserConnectionConfig{CDPURL: testLoopback + "/json/version?token=secret"},
		Selection:  config.BrowserSelectionConfig{Browser: testBrowserID, Tab: testTargetID},
	}
}

func requestFor(browser config.BrowserConfig, broker *fakeBroker) Request {
	return Request{
		LoadConfig: func() (*config.Config, error) { return &config.Config{Browser: browser}, nil },
		Factory: func(config.BrowserConfig) (direct.Runtime, error) {
			if broker == nil {
				return direct.Runtime{}, nil
			}
			return direct.Runtime{Broker: broker}, nil
		},
	}
}

func checkNamed(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("report has no %q check: %+v", name, report.Checks)
	return Check{}
}

func requireFailure(t *testing.T, report Report, err error, wantCode string, check string, checkStatus string) {
	t.Helper()
	var doctorErr *Error
	if !errors.As(err, &doctorErr) {
		t.Fatalf("Diagnose error = %v, want *Error", err)
	}
	if report.Error == nil || report.Error.Code != wantCode {
		t.Fatalf("report error = %+v, want code %q", report.Error, wantCode)
	}
	if got := checkNamed(t, report, check).Status; got != checkStatus {
		t.Fatalf("%s check status = %q, want %q", check, got, checkStatus)
	}
}
