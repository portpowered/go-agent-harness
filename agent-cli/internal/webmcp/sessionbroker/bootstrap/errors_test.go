package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func classifiedOf(t *testing.T, err error) *webmcp.ClassifiedError {
	t.Helper()
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		t.Fatalf("error = %T %v, want classified error", err, err)
	}
	return classified
}

func TestCapabilityErrorKeepsManagedRemediationSafe(t *testing.T) {
	secret := "download failed at /private/profile with token=secret"
	acquisitionErr := &chrome.ManagedChromeAcquisitionError{
		FallbackCategory: "download_failed",
		Platform:         "darwin-arm64",
		Cause:            errors.New(secret),
	}
	launchErr := &chrome.ManagedBrowserLaunchError{Phase: "acquisition", Mode: "headful", Cause: acquisitionErr}

	classified := classifiedOf(t, capabilityError(launchErr))
	if classified.Code != webmcp.ErrorEndpointUnreachable {
		t.Fatalf("managed launch code = %q, want %q", classified.Code, webmcp.ErrorEndpointUnreachable)
	}
	if classified.Details[detailPhase] != "acquisition" || classified.Details["mode"] != "headful" {
		t.Fatalf("managed launch details = %#v, want phase and mode", classified.Details)
	}
	if remediation, ok := classified.Details[detailRemediation].(string); !ok || !strings.Contains(remediation, "install Chrome 151") {
		t.Fatalf("managed launch remediation = %q, want Chrome prerequisite guidance", remediation)
	}
	if !strings.Contains(classified.Message, "download_failed") || strings.Contains(classified.Message, secret) {
		t.Fatalf("managed launch message = %q, want safe fallback category without nested secret", classified.Message)
	}
}

func TestCapabilityErrorBoundsUnsafeManagedLabels(t *testing.T) {
	launch := classifiedOf(t, capabilityError(&chrome.ManagedBrowserLaunchError{Phase: "bad phase/..", Mode: strings.Repeat("m", 40)}))
	if launch.Details[detailPhase] != fallbackLaunchPhase || launch.Details["mode"] != fallbackLaunchModeLabel {
		t.Fatalf("unsafe launch labels = %#v, want bounded fallbacks", launch.Details)
	}
	lifecycle := classifiedOf(t, capabilityError(&chrome.ManagedBrowserLifecycleError{Phase: "shutdown", Cause: errors.New("/secret/path")}))
	if lifecycle.Details[detailPhase] != "shutdown" || lifecycle.Details[detailRemediation] != fallbackRemediation {
		t.Fatalf("lifecycle details = %#v, want safe phase and generic remediation", lifecycle.Details)
	}
	if strings.Contains(lifecycle.Message, "/secret/path") {
		t.Fatalf("lifecycle message leaked its cause: %q", lifecycle.Message)
	}
}

func TestCapabilityErrorPassesThroughCancellationAndClassifiedErrors(t *testing.T) {
	if capabilityError(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := capabilityError(err); !errors.Is(got, err) {
			t.Fatalf("capabilityError(%v) = %v, want unchanged", err, got)
		}
	}
	classified := webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "ambiguous", nil)
	if got := capabilityError(classified); !errors.Is(got, classified) {
		t.Fatalf("classified error was rewrapped: %v", got)
	}
	discoveryErr := &discovery.DiscoveryError{Code: discovery.CodeNoEligibleTab, Message: "none"}
	if got := classifiedOf(t, capabilityError(discoveryErr)); got.Code != webmcp.ErrorNoEligibleTab {
		t.Fatalf("discovery error code = %q, want no_eligible_tab", got.Code)
	}
}

func TestAdoptSelectionRejectsIncompleteSelectionAndUsesBaseSelect(t *testing.T) {
	err := adoptSelection(context.Background(), &baseBroker{}, discovery.Selection{BrowserID: "browser"}, true)
	if got := classifiedOf(t, err); got.Code != webmcp.ErrorStaleSelection || got.Details["reason"] != "persisted_selection_incomplete" {
		t.Fatalf("incomplete selection error = %v", err)
	}
	broker := &baseBroker{}
	if err := adoptSelection(context.Background(), broker, discovery.Selection{BrowserID: "browser", TargetID: "tab"}, true); err != nil || broker.selectCalls != 1 {
		t.Fatalf("base select adoption = %v calls=%d", err, broker.selectCalls)
	}
	if err := adoptSelection(context.Background(), nil, discovery.Selection{}, false); !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("nil broker adoption = %v, want ErrClosed", err)
	}
}

func TestManagedStartupRecoveryFailures(t *testing.T) {
	noEligible := &discovery.DiscoveryError{Code: discovery.CodeNoEligibleTab, Message: "none", Retryable: true}
	service := &loadingDiscovery{reconnectDiscovery: reconnectDiscovery{err: noEligible}}

	// A broker without CreateTab falls back to endpoint verification.
	state, err := runWithState(managedBrowser("about:blank"), service, &capabilityBroker{})
	if err != nil || state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("fallback recovery = %v/%q, want connected-unselected", err, state)
	}
	createErr := errors.New("create failed")
	broker := &creatingBootstrapBroker{capabilityBroker: &capabilityBroker{}, createErr: createErr}
	if _, err := runWithState(managedBrowser("about:blank"), service, broker); !errors.Is(err, createErr) {
		t.Fatalf("create failure = %v, want wrapped create error", err)
	}
	openErr := errors.New("open failed")
	opener := &capabilityBroker{openErr: openErr}
	if _, err := runWithState(managedBrowser("https://example.test/"), service, opener); !errors.Is(err, openErr) {
		t.Fatalf("open failure = %v, want wrapped open error", err)
	}
	if _, err := runWithState(managedBrowser("https://example.test/"), service, &baseBroker{}); err != nil {
		t.Fatalf("broker without OpenTab must fall back to verification: %v", err)
	}
}

func TestRecoveryRejectsNonRecoverableSelectionErrors(t *testing.T) {
	b := &bootstrapper{browser: externalBrowser(), broker: &baseBroker{}}
	hard := webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "attach failed", nil)
	if err := b.recoverConnectedUnselected(context.Background(), hard); !errors.Is(err, hard) {
		t.Fatalf("recoverConnectedUnselected = %v, want original error", err)
	}
	if err := b.recoverRestoredSelection(context.Background(), hard); !errors.Is(err, hard) {
		t.Fatalf("recoverRestoredSelection = %v, want original error", err)
	}
	staleClassified := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "stale", nil)
	staleClassified.Retryable = true
	if !recoverableRestoredSelectionError(staleClassified) || recoverableSelectionError(staleClassified) {
		t.Fatal("a retryable classified stale selection is recoverable only for persisted restores")
	}
	if retryableCatalogDeadline(webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "no details", nil)) {
		t.Fatal("a protocol error without catalog details is not a late catalog")
	}
}
