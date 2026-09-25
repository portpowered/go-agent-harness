// Package production composes browser discovery, the neutral WebMCP broker,
// and Chrome's browser runtime into the request-scoped runtime used by the
// CLI's WebMCP routes. Construction is lazy: no browser endpoint is opened
// until a command invokes the returned runtime.
package production

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// DiscoveryService is the discovery service consumed by the production
// composition. Keeping this interface narrow lets command tests inject a
// discovery fake without importing a browser protocol package or depending
// on a concrete service implementation.
type DiscoveryService interface {
	DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error)
	ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error)
	Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error)
	Selected() (discovery.Selection, bool)
	RefreshSelection(context.Context) (discovery.Selection, error)
}

// Runtime is the composed request-scoped runtime. Close owns every resource
// not owned by Broker.
type Runtime struct {
	Broker    webmcp.Broker
	Discovery DiscoveryService
	Catalog   webmcp.DevToolsCatalog
	Close     func() error
}

// Factory constructs one Runtime for a resolved browser configuration.
type Factory func(config.BrowserConfig) (Runtime, error)

// Options holds the injectable production dependencies. Nil fields resolve
// to the production defaults when the factory runs.
type Options struct {
	Runtime                      webmcp.BrowserRuntime
	Catalog                      webmcp.DevToolsCatalog
	Discovery                    DiscoveryService
	ConfigDir                    string
	ManagedBrowserManager        *chrome.ManagedBrowserManager
	ManagedBrowserManagerFactory func(string) *chrome.ManagedBrowserManager
	HTTPClient                   discovery.HTTPClient
	ActivePortReader             discovery.ActivePortReader
	ProcessEnumerator            discovery.ProcessEnumerator
	IDMapper                     discovery.IDMapper
	TargetIDMapper               discovery.TargetIDMapper
	Clock                        discovery.Clock
	SelectionStore               any
	SelectionStoreFactory        func() any
}

// Option customizes one production factory dependency.
type Option func(*Options)

// WithRuntime injects the raw browser runtime.
func WithRuntime(runtime webmcp.BrowserRuntime) Option {
	return func(options *Options) { options.Runtime = runtime }
}

// WithCatalog injects the DevTools version/target catalog.
func WithCatalog(catalog webmcp.DevToolsCatalog) Option {
	return func(options *Options) { options.Catalog = catalog }
}

// WithDiscovery injects the discovery service.
func WithDiscovery(service DiscoveryService) Option {
	return func(options *Options) { options.Discovery = service }
}

// WithConfigDir keeps managed-browser state and selection persistence on the
// same resolved config directory.
func WithConfigDir(configDir string) Option {
	return func(options *Options) { options.ConfigDir = configDir }
}

// WithManagedBrowserManager injects the lifecycle manager used by
// endpoint-free browser configurations. It is primarily a hermetic test seam;
// production callers can use the factory variant below.
func WithManagedBrowserManager(manager *chrome.ManagedBrowserManager) Option {
	return func(options *Options) { options.ManagedBrowserManager = manager }
}

// WithManagedBrowserManagerFactory defers manager creation until the browser
// configuration has been resolved.
func WithManagedBrowserManagerFactory(factory func(string) *chrome.ManagedBrowserManager) Option {
	return func(options *Options) { options.ManagedBrowserManagerFactory = factory }
}

// WithHTTPClient injects the discovery HTTP client.
func WithHTTPClient(client discovery.HTTPClient) Option {
	return func(options *Options) { options.HTTPClient = client }
}

// WithActivePortReader injects the DevToolsActivePort reader.
func WithActivePortReader(reader discovery.ActivePortReader) Option {
	return func(options *Options) { options.ActivePortReader = reader }
}

// WithProcessEnumerator injects the browser process enumerator.
func WithProcessEnumerator(enumerator discovery.ProcessEnumerator) Option {
	return func(options *Options) { options.ProcessEnumerator = enumerator }
}

// WithIDMapper injects the browser ID mapper.
func WithIDMapper(mapper discovery.IDMapper) Option {
	return func(options *Options) { options.IDMapper = mapper }
}

// WithTargetIDMapper injects the target ID mapper.
func WithTargetIDMapper(mapper discovery.TargetIDMapper) Option {
	return func(options *Options) { options.TargetIDMapper = mapper }
}

// WithClock injects the discovery and broker clock.
func WithClock(clock discovery.Clock) Option {
	return func(options *Options) { options.Clock = clock }
}

// WithSelectionStore injects the selection store. A selectionstore.Store is
// adapted to discovery persistence; any other value is passed through.
func WithSelectionStore(store any) Option {
	return func(options *Options) { options.SelectionStore = store }
}

// WithSelectionStoreFactory defers selection-store creation until command
// execution. This keeps a parsed --config-dir override aligned with the store
// used by the direct command.
func WithSelectionStoreFactory(factory func() any) Option {
	return func(options *Options) { options.SelectionStoreFactory = factory }
}

// DiscoveryInputs derives discovery connection inputs from browser config.
func DiscoveryInputs(browser config.BrowserConfig) discovery.ConnectionInputs {
	return discovery.ConnectionInputs{
		CDPURL:            browser.Connection.CDPURL,
		BrowserWSEndpoint: browser.Connection.WSEndpoint,
		UserDataDir:       browser.Connection.UserDataDir,
		AllowProcessScan:  browser.Connection.AllowProcessScan,
		AllowRemoteCDP:    browser.Connection.AllowRemoteCDP,
	}
}
