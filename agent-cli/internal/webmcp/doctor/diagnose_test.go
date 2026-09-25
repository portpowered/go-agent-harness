package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

func TestDiagnoseReadyRecordsEveryCheckAndRedacts(t *testing.T) {
	broker := readyBroker()
	report, err := Diagnose(context.Background(), requestFor(readyBrowser(), broker))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if report.Status != StatusReady || report.PageTools != "ready" || report.WebMCP != ValueSupported {
		t.Fatalf("report = %+v", report)
	}
	for _, check := range report.Checks {
		if check.Status != CheckPass {
			t.Fatalf("check %s = %s, want pass", check.Name, check.Status)
		}
	}
	if report.Endpoint.Address != testLoopback+"/json/version" || report.Endpoint.Scope != scopeLoopback {
		t.Fatalf("endpoint = %+v", report.Endpoint)
	}
	if report.SelectedPage == nil || report.SelectedPage.Origin != testOrigin || !report.SelectedPage.Attached {
		t.Fatalf("selected page = %+v", report.SelectedPage)
	}
	if report.PageTargets != 1 || report.EligiblePages != 1 || len(report.Targets) != 2 || report.Targets[0].BrowserID != testBrowserID {
		t.Fatalf("targets = %+v pages=%d eligible=%d", report.Targets, report.PageTargets, report.EligiblePages)
	}
	if report.Catalog.ToolCount != 1 || report.Catalog.Generation != 3 || report.Browsers[0].Product != "Chrome/Test" {
		t.Fatalf("catalog/browsers = %+v/%+v", report.Catalog, report.Browsers)
	}
}

func TestDiagnoseRejectsNegativeTimeoutWithoutWrapping(t *testing.T) {
	request := requestFor(readyBrowser(), readyBroker())
	request.CommandTimeout = -time.Second
	report, err := Diagnose(context.Background(), request)
	var doctorErr *Error
	if err == nil || errors.As(err, &doctorErr) {
		t.Fatalf("Diagnose error = %v, want the raw invalid-input error", err)
	}
	if report.Status != StatusInvalidConfiguration || report.Error.Code != string(webmcp.ErrorInvalidToolInput) {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnoseConfigurationFailures(t *testing.T) {
	cases := map[string]func() (*config.Config, error){
		"load error": func() (*config.Config, error) { return nil, errFake },
		"nil config": func() (*config.Config, error) { return nil, nil },
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			report, err := Diagnose(context.Background(), Request{LoadConfig: load})
			requireFailure(t, report, err, ErrorInvalidConfiguration, checkConfiguration, CheckFail)
			if report.Status != StatusInvalidConfiguration {
				t.Fatalf("status = %q", report.Status)
			}
		})
	}
	report, err := Diagnose(context.Background(), Request{})
	requireFailure(t, report, err, ErrorInvalidConfiguration, checkConfiguration, CheckFail)
}

func TestDiagnoseDisabledBackendWarns(t *testing.T) {
	browser := readyBrowser()
	browser.Tools.Enabled = false
	report, err := Diagnose(context.Background(), requestFor(browser, readyBroker()))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if checkNamed(t, report, checkActivation).Status != CheckWarn || len(report.Warnings) != 1 {
		t.Fatalf("activation = %+v warnings=%v", checkNamed(t, report, checkActivation), report.Warnings)
	}
}

func TestDiagnoseEndpointPolicyFailures(t *testing.T) {
	invalid := readyBrowser()
	invalid.Connection.CDPURL = "ftp://user@host.test"
	report, err := Diagnose(context.Background(), requestFor(invalid, readyBroker()))
	requireFailure(t, report, err, string(webmcp.ErrorBrowserProtocol), checkEndpoint, CheckFail)

	remote := readyBrowser()
	remote.Connection.CDPURL = "http://browser.example:9222"
	report, err = Diagnose(context.Background(), requestFor(remote, readyBroker()))
	requireFailure(t, report, err, string(webmcp.ErrorRemoteEndpointDenied), checkEndpoint, CheckFail)
}

func TestDiagnoseRuntimeConstructionFailures(t *testing.T) {
	request := requestFor(readyBrowser(), nil)
	report, err := Diagnose(context.Background(), request)
	requireFailure(t, report, err, string(webmcp.ErrorBrowserProtocol), checkDiscovery, CheckUnavailable)

	closed := false
	request.Factory = func(config.BrowserConfig) (direct.Runtime, error) {
		return direct.Runtime{Close: func() error { closed = true; return nil }}, errFake
	}
	report, err = Diagnose(context.Background(), request)
	requireFailure(t, report, err, string(webmcp.ErrorBrowserProtocol), checkDiscovery, CheckUnavailable)
	if !closed || report.Status != StatusUnavailable || checkNamed(t, report, checkCleanup).Status != CheckPass {
		t.Fatalf("closed=%t report=%+v", closed, report)
	}
}

func TestDiagnoseCleanupFailures(t *testing.T) {
	broker := readyBroker()
	broker.closeErr = errFake
	report, err := Diagnose(context.Background(), requestFor(readyBrowser(), broker))
	requireFailure(t, report, err, ErrorCleanupFailed, checkCleanup, CheckFail)
	if report.Status != StatusCleanupError {
		t.Fatalf("status = %q", report.Status)
	}

	broker.catalogErr = errFake
	report, err = Diagnose(context.Background(), requestFor(readyBrowser(), broker))
	requireFailure(t, report, err, string(webmcp.ErrorBrowserProtocol), checkCleanup, CheckFail)
	if report.Error.Details["cleanup_error"] != true || !errors.Is(err, errFake) {
		t.Fatalf("error = %+v / %v", report.Error, err)
	}
}

func TestErrorFormatting(t *testing.T) {
	var nilErr *Error
	if nilErr.Error() != messageDoctorFailed || nilErr.Unwrap() != nil {
		t.Fatal("nil *Error formatting changed")
	}
	plain := &Error{Cause: errFake}
	if plain.Error() != messageDoctorFailed || !errors.Is(plain, errFake) {
		t.Fatalf("plain error = %q", plain.Error())
	}
	coded := &Error{Report: Report{Error: &ErrorData{Code: "x", Message: "y"}}}
	if !strings.Contains(coded.Error(), "x: y") {
		t.Fatalf("coded error = %q", coded.Error())
	}
}
