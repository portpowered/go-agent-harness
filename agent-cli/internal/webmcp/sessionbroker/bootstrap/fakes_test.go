package bootstrap

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/webmcptest"
)

// baseBroker is the shared scripted broker, which records every forwarded
// call by name.
type baseBroker = webmcptest.Broker

// capabilityBroker adds activation-aware selection and tab opening.
type capabilityBroker struct {
	baseBroker
	selectOpts  webmcp.SelectOptions
	openRequest webmcp.OpenTabRequest
	openErr     error
	openCalls   int
}

func (b *capabilityBroker) SelectWithOptions(_ context.Context, _ webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.Record("select")
	b.selectOpts = options
	return b.Page, b.SelectErr
}

func (b *capabilityBroker) OpenTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	b.openCalls++
	b.openRequest = request
	return b.Page, b.openErr
}

type creatingBootstrapBroker struct {
	*capabilityBroker
	createCalls   int
	createRequest webmcp.OpenTabRequest
	createErr     error
}

func (b *creatingBootstrapBroker) CreateTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	b.createCalls++
	b.createRequest = request
	return webmcp.Target{BrowserID: "managed-browser", ID: "managed-tab-new", Type: "page", URL: request.URL}, b.createErr
}

// plainDiscovery satisfies production.DiscoveryService without the optional
// reconnect or persisted-selection extensions.
type plainDiscovery struct{}

func (plainDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return nil, nil
}

func (plainDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	return discovery.TargetSnapshot{}, nil
}

func (plainDiscovery) Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (plainDiscovery) Selected() (discovery.Selection, bool) { return discovery.Selection{}, false }

func (plainDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

// reconnectDiscovery records every reconnect and answers with one outcome.
type reconnectDiscovery struct {
	plainDiscovery
	selected discovery.Selection
	err      error
	options  []discovery.ReconnectOptions
}

func (d *reconnectDiscovery) Reconnect(_ context.Context, _ discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	d.options = append(d.options, options...)
	return d.selected, d.err
}

// loadingDiscovery adds the persisted-record probe.
type loadingDiscovery struct {
	reconnectDiscovery
	present bool
	loadErr error
}

func (d *loadingDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{BrowserID: "managed-old", TargetID: "youtube-old"}, d.present, d.loadErr
}

// loaderOnlyDiscovery exposes the persisted-record probe without Reconnect.
type loaderOnlyDiscovery struct {
	plainDiscovery
}

func (loaderOnlyDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{BrowserID: "browser", TargetID: "tab"}, true, nil
}

var (
	_ production.DiscoveryService = (*reconnectDiscovery)(nil)
	_ Reconnector                 = (*reconnectDiscovery)(nil)
	_ PersistedSelectionLoader    = (*loadingDiscovery)(nil)
	_ webmcp.BrokerTabCreator     = (*creatingBootstrapBroker)(nil)
)
