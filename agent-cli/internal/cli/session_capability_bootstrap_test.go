package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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

type bootstrapSelectionDiscovery struct {
	sessionBrokerDiscovery
	selected discovery.Selection
}

func (d bootstrapSelectionDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	return d.selected, nil
}

var _ WebMCPDiscoveryService = bootstrapSelectionDiscovery{}
var _ sessionSelectionReconnector = bootstrapSelectionDiscovery{}
