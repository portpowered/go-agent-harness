// Package bootstrap owns the one setup operation shared by every broker tool
// in a browser-enabled session: reconcile the configured or persisted
// selection, recover ordinary drift to a connected-but-unselected browser, and
// fail closed with a classified error for everything else.
package bootstrap

import (
	"context"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// bootstrapPhase labels every reconnect and failure issued by session setup.
const bootstrapPhase = "session_bootstrap"

// Reconnector is intentionally narrower than the production discovery
// service. It keeps older injected discovery fakes source- and
// behavior-compatible while allowing the production session to use the strict
// persisted-identity reconnect path.
type Reconnector interface {
	Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error)
}

// PersistedSelectionLoader is the optional persisted-record probe.
type PersistedSelectionLoader interface {
	LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error)
}

// StateFunc receives browser capability state transitions. It may be nil.
type StateFunc func(webmcp.BrowserCapabilityState)

// New creates the session setup operation. Endpoint verification happens even
// when there is no persisted selection, while a present record always takes
// the strict browser/target/origin/continuity reconnect path.
func New(browser config.BrowserConfig, service production.DiscoveryService, broker webmcp.Broker, setState StateFunc) func(context.Context) error {
	b := &bootstrapper{browser: browser, broker: broker, setState: setState}
	if reconnector, ok := service.(Reconnector); ok {
		b.reconnector = reconnector
	}
	if loader, ok := service.(PersistedSelectionLoader); ok {
		b.loader = loader
	}
	return b.run
}

type bootstrapper struct {
	browser     config.BrowserConfig
	broker      webmcp.Broker
	reconnector Reconnector
	loader      PersistedSelectionLoader
	setState    StateFunc
}

// selectors is the normalized operator selection for one bootstrap.
type selectors struct {
	browserID string
	targetID  string
	origin    string
	activate  bool
}

func (s selectors) explicit() bool {
	return s.browserID != "" || s.targetID != "" || s.origin != ""
}

func (b *bootstrapper) run(ctx context.Context) error { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	selected, err := b.selectors()
	if err != nil {
		return err
	}
	if handled, err := b.selectConfigured(ctx, selected); handled {
		return err
	}
	if !selected.explicit() {
		if handled, err := b.selectDefault(ctx, selected); handled {
			return err
		}
	}
	if err := verifyEndpoint(ctx, b.browser, b.broker); err != nil {
		return err
	}
	b.mark(webmcp.BrowserCapabilityConnectedUnselected)
	return nil
}

// selectors trims the configured selection and accepts the tabs listing's
// "browserID/targetID" reference the same way the direct commands do.
func (b *bootstrapper) selectors() (selectors, error) {
	selection := b.browser.Selection
	result := selectors{
		browserID: strings.TrimSpace(selection.Browser),
		targetID:  strings.TrimSpace(selection.Tab),
		origin:    strings.TrimSpace(selection.Origin),
		activate:  selection.ActivateTab || b.browser.UsesManagedBrowser(),
	}
	refBrowserID, refTargetID, composite := normalize.SplitCompositeTargetRef(result.targetID)
	if !composite {
		return result, nil
	}
	if result.browserID != "" && result.browserID != refBrowserID {
		return selectors{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the target reference names a different browser than the explicit browser selector", map[string]any{
			"browser_id":          direct.NormalizeOpaqueID(result.browserID),
			"target_id":           direct.NormalizeOpaqueID(refTargetID),
			"selected_generation": uint64(0),
			"reason":              "selector_browser_mismatch",
		})
	}
	result.browserID = refBrowserID
	result.targetID = refTargetID
	return result, nil
}

func (b *bootstrapper) mark(state webmcp.BrowserCapabilityState) {
	if b.setState != nil {
		b.setState(state)
	}
}

func (b *bootstrapper) reconnect(ctx context.Context, options discovery.ReconnectOptions) (discovery.Selection, error) {
	options.Reason = bootstrapPhase
	return b.reconnector.Reconnect(ctx, production.DiscoveryInputs(b.browser), options)
}

func (b *bootstrapper) adopt(ctx context.Context, selected discovery.Selection, activate bool) error {
	err := adoptSelection(ctx, b.broker, selected, activate)
	if err == nil {
		b.mark(webmcp.BrowserCapabilitySelected)
	}
	return err
}
