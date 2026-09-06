package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestSessionCapabilityBootstrapKeepsConnectedSelectionForLateCatalog(t *testing.T) {
	lateCatalog := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "page-tool catalog evidence is not ready", map[string]any{
		"reason_code": "page_tools_unverified",
		"reason":      "deadline_exceeded",
	})
	lateCatalog.Retryable = true

	base := &capabilityBroker{
		selected: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-late", TargetID: "tab-late"},
			Connected:  true,
			Generation: 7,
		},
		selectErr: lateCatalog,
	}
	selected := discovery.Selection{
		BrowserID:  "browser-late",
		TargetID:   "tab-late",
		Generation: 7,
	}
	browser := config.DefaultBrowserConfig()
	browser.Selection.Browser = selected.BrowserID
	browser.Selection.Tab = selected.TargetID

	bootstrap := sessionCapabilityBootstrap(browser, bootstrapSelectionDiscovery{selected: selected}, base)
	if err := bootstrap(context.Background()); err != nil {
		t.Fatalf("late catalog must not fail session bootstrap: %v", err)
	}
	if base.selectCalls != 1 {
		t.Fatalf("selection calls = %d, want one exact adoption", base.selectCalls)
	}
	if got, err := base.Selected(context.Background()); err != nil || got.Key != base.selected.Key {
		t.Fatalf("connected selection after bootstrap = %#v, err=%v", got, err)
	}
}

func TestSessionCapabilityBootstrapStillFailsClosedForNonCatalogSelectionErrors(t *testing.T) {
	selectionErr := webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "target attachment failed", map[string]any{
		"reason": "transport_failure",
	})
	selectionErr.Retryable = true
	base := &capabilityBroker{selectErr: selectionErr}
	selected := discovery.Selection{BrowserID: "browser", TargetID: "tab"}
	browser := config.DefaultBrowserConfig()
	browser.Selection.Browser = selected.BrowserID
	browser.Selection.Tab = selected.TargetID

	err := sessionCapabilityBootstrap(browser, bootstrapSelectionDiscovery{selected: selected}, base)(context.Background())
	if !errors.Is(err, selectionErr) {
		t.Fatalf("selection error = %v, want original classified attachment error", err)
	}
}

func TestSessionCapabilityBootstrapKeepsReachableAmbiguityConnectedAndUnselected(t *testing.T) {
	selectionErr := &discovery.DiscoveryError{
		Code:      discovery.CodeAmbiguousTab,
		Message:   "multiple browser tabs matched",
		Retryable: true,
		Details: map[string]any{
			"browser_id":           "browser-a",
			"candidate_target_ids": []string{"target-a", "target-b"},
		},
	}
	base := &capabilityBroker{}
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = "http://127.0.0.1:9222"
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	discoveryService := ambiguousBootstrapSelectionDiscovery{
		bootstrapSelectionDiscovery: bootstrapSelectionDiscovery{},
		err:                         selectionErr,
	}
	var state webmcp.BrowserCapabilityState
	bootstrap := sessionCapabilityBootstrapWithState(browser, discoveryService, base, func(got webmcp.BrowserCapabilityState) {
		state = got
	})

	if err := bootstrap(context.Background()); err != nil {
		t.Fatalf("reachable ambiguous bootstrap: %v", err)
	}
	if state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("browser capability state = %q, want connected_unselected", state)
	}
	if base.selectCalls != 0 {
		t.Fatalf("ambiguous bootstrap selected a page %d times", base.selectCalls)
	}
}

type bootstrapSelectionDiscovery struct {
	sessionBrokerDiscovery
	selected discovery.Selection
}

type ambiguousBootstrapSelectionDiscovery struct {
	bootstrapSelectionDiscovery
	err error
}

type stalePersistedBootstrapDiscovery struct {
	bootstrapSelectionDiscovery
}

func (d stalePersistedBootstrapDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{BrowserID: "managed-old", TargetID: "youtube-old"}, true, nil
}

func (d stalePersistedBootstrapDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	return discovery.Selection{}, &discovery.DiscoveryError{Code: discovery.CodeStaleSelection, Message: "persisted target is gone", Retryable: true}
}

func (d ambiguousBootstrapSelectionDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{}, false, nil
}

func (d ambiguousBootstrapSelectionDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	return discovery.Selection{}, d.err
}

func (d bootstrapSelectionDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	return d.selected, nil
}

type recordingBootstrapSelectionDiscovery struct {
	bootstrapSelectionDiscovery
	reconnectOptions []discovery.ReconnectOptions
}

func (d *recordingBootstrapSelectionDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{}, false, nil
}

func (d *recordingBootstrapSelectionDiscovery) Reconnect(ctx context.Context, inputs discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	d.reconnectOptions = append(d.reconnectOptions, options...)
	return d.selected, nil
}

func TestSessionCapabilityBootstrapUsesSingleSelectionForManagedDefault(t *testing.T) {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	selected := discovery.Selection{
		BrowserID: "managed-browser",
		TargetID:  "managed-tab",
		Origin:    "https://example.test",
	}
	discoveryService := &recordingBootstrapSelectionDiscovery{
		bootstrapSelectionDiscovery: bootstrapSelectionDiscovery{selected: selected},
	}
	broker := &capabilityBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: webmcp.BrowserID(selected.BrowserID), TargetID: webmcp.TargetID(selected.TargetID)},
		Origin:    selected.Origin,
		Connected: true,
		Ready:     true,
	}}

	if err := sessionCapabilityBootstrap(browser, discoveryService, broker)(context.Background()); err != nil {
		t.Fatalf("bootstrap managed browser: %v", err)
	}
	if len(discoveryService.reconnectOptions) != 1 {
		t.Fatalf("reconnect calls = %d, want 1", len(discoveryService.reconnectOptions))
	}
	if got := discoveryService.reconnectOptions[0].AutoSelect; got != discovery.AutoSelectSingle {
		t.Fatalf("auto-select = %q, want %q", got, discovery.AutoSelectSingle)
	}
	if broker.selectCalls != 1 {
		t.Fatalf("selection calls = %d, want 1", broker.selectCalls)
	}
	if !broker.selectOpts.Activate {
		t.Fatal("managed startup target was not activated for a visible browser session")
	}
}

func TestSessionCapabilityBootstrapReopensVisibleManagedTabWhenWarmBrowserIsEmpty(t *testing.T) {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Managed.Open = "about:blank"
	discoveryService := ambiguousBootstrapSelectionDiscovery{
		err: &discovery.DiscoveryError{
			Code:      discovery.CodeNoEligibleTab,
			Message:   "no eligible browser tab was found",
			Retryable: true,
		},
	}
	delegate := &capabilityBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: "managed-browser", TargetID: "managed-tab-new"},
		URL:       "about:blank",
		Connected: true,
	}}
	broker := &creatingBootstrapBroker{capabilityBroker: delegate}
	var state webmcp.BrowserCapabilityState
	bootstrap := sessionCapabilityBootstrapWithState(browser, discoveryService, broker, func(got webmcp.BrowserCapabilityState) {
		state = got
	})

	if err := bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap empty warm managed browser: %v", err)
	}
	if broker.createCalls != 1 || broker.createRequest.URL != "about:blank" || !broker.createRequest.Activate {
		t.Fatalf("managed create-tab recovery = calls:%d request:%+v", broker.createCalls, broker.createRequest)
	}
	if delegate.openCalls != 0 {
		t.Fatalf("managed about:blank recovery selected WebMCP %d times, want zero", delegate.openCalls)
	}
	if state != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("browser capability state = %q, want connected-unselected", state)
	}
}

func TestSessionCapabilityBootstrapReopensManagedStartupAfterStalePersistedTarget(t *testing.T) {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Managed.Open = "https://www.youtube.com/"
	delegate := &capabilityBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: "managed-new", TargetID: "youtube-new"},
		URL:       browser.Managed.Open,
		Connected: true,
	}}
	var state webmcp.BrowserCapabilityState
	bootstrap := sessionCapabilityBootstrapWithState(browser, stalePersistedBootstrapDiscovery{}, delegate, func(got webmcp.BrowserCapabilityState) {
		state = got
	})

	if err := bootstrap(context.Background()); err != nil {
		t.Fatalf("recover stale managed startup selection: %v", err)
	}
	if delegate.openCalls != 1 || delegate.openRequest.URL != browser.Managed.Open || !delegate.openRequest.Activate {
		t.Fatalf("managed stale recovery = calls:%d request:%+v", delegate.openCalls, delegate.openRequest)
	}
	if state != webmcp.BrowserCapabilitySelected {
		t.Fatalf("browser capability state = %q, want selected", state)
	}
}

type creatingBootstrapBroker struct {
	*capabilityBroker
	createCalls   int
	createRequest webmcp.OpenTabRequest
}

func (b *creatingBootstrapBroker) CreateTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	b.createCalls++
	b.createRequest = request
	return webmcp.Target{BrowserID: "managed-browser", ID: "managed-tab-new", Type: "page", URL: request.URL}, nil
}

func TestSessionCapabilityErrorKeepsManagedRemediationSafe(t *testing.T) {
	secret := "download failed at /private/profile with token=secret"
	acquisitionErr := &chrome.ManagedChromeAcquisitionError{
		FallbackCategory: "download_failed",
		Platform:         "darwin-arm64",
		Cause:            errors.New(secret),
	}
	launchErr := &chrome.ManagedBrowserLaunchError{
		Phase: "acquisition",
		Mode:  "headful",
		Cause: acquisitionErr,
	}

	err := sessionCapabilityError(launchErr)
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		t.Fatalf("managed launch error = %T %v, want classified error", err, err)
	}
	if classified.Code != webmcp.ErrorEndpointUnreachable {
		t.Fatalf("managed launch code = %q, want %q", classified.Code, webmcp.ErrorEndpointUnreachable)
	}
	if classified.Details["phase"] != "acquisition" || classified.Details["mode"] != "headful" {
		t.Fatalf("managed launch details = %#v, want phase and mode", classified.Details)
	}
	if remediation, _ := classified.Details["remediation"].(string); !strings.Contains(remediation, "install Chrome 151") {
		t.Fatalf("managed launch remediation = %q, want Chrome prerequisite guidance", remediation)
	}
	if !strings.Contains(classified.Message, "download_failed") || strings.Contains(classified.Message, secret) {
		t.Fatalf("managed launch message = %q, want safe fallback category without nested secret", classified.Message)
	}
}

var _ WebMCPDiscoveryService = bootstrapSelectionDiscovery{}
var _ sessionSelectionReconnector = bootstrapSelectionDiscovery{}
