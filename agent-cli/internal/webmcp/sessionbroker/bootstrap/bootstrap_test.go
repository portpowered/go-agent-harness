package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
)

const testEndpoint = "http://127.0.0.1:9222"

func externalBrowser() config.BrowserConfig {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = testEndpoint
	return browser
}

func managedBrowser(startupURL string) config.BrowserConfig {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Managed.Open = startupURL
	return browser
}

// runWithState runs one bootstrap and returns its final state and error.
func runWithState(browser config.BrowserConfig, service production.DiscoveryService, broker webmcp.Broker) (webmcp.BrowserCapabilityState, error) {
	var state webmcp.BrowserCapabilityState
	err := New(browser, service, broker, func(got webmcp.BrowserCapabilityState) { state = got })(context.Background())
	return state, err
}

func TestBootstrapKeepsConnectedSelectionForLateCatalog(t *testing.T) {
	lateCatalog := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "page-tool catalog evidence is not ready", map[string]any{
		"reason_code": "page_tools_unverified",
		"reason":      "deadline_exceeded",
	})
	lateCatalog.Retryable = true
	base := &capabilityBroker{baseBroker: baseBroker{
		selected:  webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-late", TargetID: "tab-late"}, Connected: true, Generation: 7},
		selectErr: lateCatalog,
	}}
	selected := discovery.Selection{BrowserID: "browser-late", TargetID: "tab-late", Generation: 7}
	browser := config.DefaultBrowserConfig()
	browser.Selection.Browser = selected.BrowserID
	browser.Selection.Tab = selected.TargetID

	if err := New(browser, &reconnectDiscovery{selected: selected}, base, nil)(context.Background()); err != nil {
		t.Fatalf("late catalog must not fail session bootstrap: %v", err)
	}
	if base.selectCalls != 1 {
		t.Fatalf("selection calls = %d, want one exact adoption", base.selectCalls)
	}
}

func TestBootstrapStillFailsClosedForNonCatalogSelectionErrors(t *testing.T) {
	selectionErr := webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "target attachment failed", map[string]any{"reason": "transport_failure"})
	selectionErr.Retryable = true
	base := &capabilityBroker{baseBroker: baseBroker{selectErr: selectionErr}}
	selected := discovery.Selection{BrowserID: "browser", TargetID: "tab"}
	browser := config.DefaultBrowserConfig()
	browser.Selection.Browser = selected.BrowserID
	browser.Selection.Tab = selected.TargetID

	err := New(browser, &reconnectDiscovery{selected: selected}, base, nil)(context.Background())
	if !errors.Is(err, selectionErr) {
		t.Fatalf("selection error = %v, want original classified attachment error", err)
	}
}

func TestBootstrapKeepsReachableAmbiguityConnectedAndUnselected(t *testing.T) {
	selectionErr := &discovery.DiscoveryError{Code: discovery.CodeAmbiguousTab, Message: "multiple browser tabs matched", Retryable: true}
	base := &capabilityBroker{}
	browser := externalBrowser()
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	service := &loadingDiscovery{reconnectDiscovery: reconnectDiscovery{err: selectionErr}}

	state, err := runWithState(browser, service, base)
	if err != nil {
		t.Fatalf("reachable ambiguous bootstrap: %v", err)
	}
	if state != webmcp.BrowserCapabilityConnectedUnselected || base.selectCalls != 0 {
		t.Fatalf("state/select calls = %q/%d, want connected_unselected without selection", state, base.selectCalls)
	}
}

func TestBootstrapUsesSingleSelectionForManagedDefault(t *testing.T) {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	selected := discovery.Selection{BrowserID: "managed-browser", TargetID: "managed-tab", Origin: "https://example.test"}
	service := &loadingDiscovery{reconnectDiscovery: reconnectDiscovery{selected: selected}}
	broker := &capabilityBroker{baseBroker: baseBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: webmcp.BrowserID(selected.BrowserID), TargetID: webmcp.TargetID(selected.TargetID)},
		Connected: true,
		Ready:     true,
	}}}

	state, err := runWithState(browser, service, broker)
	if err != nil {
		t.Fatalf("bootstrap managed browser: %v", err)
	}
	if len(service.options) != 1 || service.options[0].AutoSelect != discovery.AutoSelectSingle || service.options[0].Reason != bootstrapPhase {
		t.Fatalf("reconnect options = %+v, want one single auto-select", service.options)
	}
	if broker.selectCalls != 1 || !broker.selectOpts.Activate || state != webmcp.BrowserCapabilitySelected {
		t.Fatalf("select calls/activate/state = %d/%v/%q", broker.selectCalls, broker.selectOpts.Activate, state)
	}
}

func TestBootstrapReopensVisibleManagedTabWhenWarmBrowserIsEmpty(t *testing.T) {
	service := &loadingDiscovery{reconnectDiscovery: reconnectDiscovery{err: &discovery.DiscoveryError{Code: discovery.CodeNoEligibleTab, Message: "no eligible browser tab was found", Retryable: true}}}
	delegate := &capabilityBroker{}
	broker := &creatingBootstrapBroker{capabilityBroker: delegate}

	state, err := runWithState(managedBrowser("about:blank"), service, broker)
	if err != nil {
		t.Fatalf("bootstrap empty warm managed browser: %v", err)
	}
	if broker.createCalls != 1 || broker.createRequest.URL != "about:blank" || !broker.createRequest.Activate {
		t.Fatalf("managed create-tab recovery = calls:%d request:%+v", broker.createCalls, broker.createRequest)
	}
	if delegate.openCalls != 0 || state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("open calls/state = %d/%q, want no WebMCP selection and connected-unselected", delegate.openCalls, state)
	}
}

func TestBootstrapReopensManagedStartupAfterStalePersistedTarget(t *testing.T) {
	browser := managedBrowser("https://www.youtube.com/")
	service := &loadingDiscovery{present: true, reconnectDiscovery: reconnectDiscovery{err: &discovery.DiscoveryError{Code: discovery.CodeStaleSelection, Message: "persisted target is gone", Retryable: true}}}
	delegate := &capabilityBroker{}

	state, err := runWithState(browser, service, delegate)
	if err != nil {
		t.Fatalf("recover stale managed startup selection: %v", err)
	}
	if delegate.openCalls != 1 || delegate.openRequest.URL != browser.Managed.Open || !delegate.openRequest.Activate || state != webmcp.BrowserCapabilitySelected {
		t.Fatalf("managed stale recovery = calls:%d request:%+v state:%q", delegate.openCalls, delegate.openRequest, state)
	}
}

func TestBootstrapAcceptsCompositeTargetReference(t *testing.T) {
	browser := externalBrowser()
	browser.Selection.Tab = "browser-a/tab-a"
	service := &reconnectDiscovery{selected: discovery.Selection{BrowserID: "browser-a", TargetID: "tab-a"}}

	if err := New(browser, service, &capabilityBroker{}, nil)(context.Background()); err != nil {
		t.Fatalf("composite target bootstrap: %v", err)
	}
	if len(service.options) != 1 || service.options[0].BrowserID != "browser-a" || service.options[0].TargetID != "tab-a" {
		t.Fatalf("reconnect options = %+v, want split composite reference", service.options)
	}

	browser.Selection.Browser = "browser-b"
	err := New(browser, service, &capabilityBroker{}, nil)(context.Background())
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorStaleSelection || classified.Details["reason"] != "selector_browser_mismatch" {
		t.Fatalf("mismatched composite reference error = %v, want stale selector mismatch", err)
	}
}

func TestBootstrapReturnsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(externalBrowser(), plainDiscovery{}, &baseBroker{}, nil)(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bootstrap error = %v, want context.Canceled", err)
	}
	// A nil context is part of the legacy bootstrap contract.
	if err := New(externalBrowser(), plainDiscovery{}, &baseBroker{}, nil)(nil); err != nil {
		t.Fatalf("nil-context bootstrap: %v", err)
	}
}

func TestBootstrapWithoutReconnectVerifiesEndpoint(t *testing.T) {
	broker := &baseBroker{}
	state, err := runWithState(externalBrowser(), plainDiscovery{}, broker)
	if err != nil || state != webmcp.BrowserCapabilityConnectedUnselected || !broker.discoverOpt.ExplicitOnly {
		t.Fatalf("err/state/explicit = %v/%q/%v, want verified connected-unselected", err, state, broker.discoverOpt.ExplicitOnly)
	}

	broker.discoverErr = errors.New("dial refused")
	_, err = runWithState(externalBrowser(), plainDiscovery{}, broker)
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserProtocol || classified.Details[detailPhase] != bootstrapPhase {
		t.Fatalf("unreachable endpoint error = %v, want classified bootstrap failure", err)
	}
	if err := New(externalBrowser(), plainDiscovery{}, nil, nil)(context.Background()); !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("nil broker error = %v, want ErrClosed", err)
	}
}

func TestBootstrapPersistedRecordFailuresFailClosed(t *testing.T) {
	loadErr := errors.New("selection file unreadable")
	service := &loadingDiscovery{loadErr: loadErr}
	if _, err := runWithState(externalBrowser(), service, &baseBroker{}); !errors.Is(err, loadErr) {
		t.Fatalf("load error = %v, want wrapped load failure", err)
	}
	if _, err := runWithState(externalBrowser(), loaderOnlyDiscovery{}, &baseBroker{}); err == nil {
		t.Fatal("persisted record without strict reconnect must fail closed")
	}
	hardStale := &discovery.DiscoveryError{Code: discovery.CodeStaleSelection, Message: "identity changed"}
	service = &loadingDiscovery{present: true, reconnectDiscovery: reconnectDiscovery{err: hardStale}}
	if _, err := runWithState(externalBrowser(), service, &baseBroker{}); err == nil {
		t.Fatal("non-retryable stale restore must fail closed")
	}
}

func TestBootstrapPersistWithoutLoader(t *testing.T) {
	browser := externalBrowser()
	browser.Selection.Persist = true
	selected := discovery.Selection{BrowserID: "browser", TargetID: "tab"}

	state, err := runWithState(browser, &reconnectDiscovery{selected: selected}, &capabilityBroker{})
	if err != nil || state != webmcp.BrowserCapabilitySelected {
		t.Fatalf("persisted restore = %v/%q, want selected", err, state)
	}
	stale := &discovery.DiscoveryError{Code: discovery.CodeStaleSelection, Retryable: true}
	if state, err := runWithState(browser, &reconnectDiscovery{err: stale}, &capabilityBroker{}); err != nil || state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("stale restore = %v/%q, want connected-unselected", err, state)
	}
	missing := webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no record", nil)
	if state, err := runWithState(browser, &reconnectDiscovery{err: missing}, &capabilityBroker{}); err != nil || state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("missing record = %v/%q, want endpoint verification", err, state)
	}
	hard := webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "attach failed", nil)
	if _, err := runWithState(browser, &reconnectDiscovery{err: hard}, &capabilityBroker{}); !errors.Is(err, hard) {
		t.Fatalf("hard restore error = %v, want original classified error", err)
	}
}

func TestBootstrapExplicitAutoSelectFailsClosedOnHardError(t *testing.T) {
	browser := externalBrowser()
	browser.Selection.Origin = "https://example.test"
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	hard := &discovery.DiscoveryError{Code: discovery.CodeBrowserDisconnected, Message: "gone"}
	service := &reconnectDiscovery{err: hard}

	if _, err := runWithState(browser, service, &capabilityBroker{}); err == nil {
		t.Fatal("hard explicit auto-select error must fail closed")
	}
	if len(service.options) != 1 || service.options[0].Origin != "https://example.test" {
		t.Fatalf("reconnect options = %+v, want origin-scoped single selection", service.options)
	}
}
