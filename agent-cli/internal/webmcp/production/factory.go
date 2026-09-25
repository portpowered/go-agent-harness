package production

import (
	"net/http"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

// NewFactory composes browser discovery, the neutral broker, and Chrome's
// browser runtime. The browser configuration handed to the returned factory
// must already be validated by the caller; construction stays lazy and opens
// no browser endpoint.
func NewFactory(options ...Option) Factory {
	resolved := Options{}
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	return func(browser config.BrowserConfig) (Runtime, error) {
		return build(resolved, browser), nil
	}
}

// dependencies are the per-invocation defaults resolved from Options.
type dependencies struct {
	httpClient     discovery.HTTPClient
	activePort     discovery.ActivePortReader
	idMapper       discovery.IDMapper
	targetIDMapper discovery.TargetIDMapper
}

func resolveDependencies(resolved Options) dependencies {
	deps := dependencies{
		httpClient:     resolved.HTTPClient,
		activePort:     resolved.ActivePortReader,
		idMapper:       resolved.IDMapper,
		targetIDMapper: resolved.TargetIDMapper,
	}
	if deps.httpClient == nil {
		deps.httpClient = http.DefaultClient
	}
	if deps.activePort == nil {
		deps.activePort = discovery.FileActivePortReader{}
	}
	if deps.idMapper == nil {
		deps.idMapper = discovery.HashIDMapper{}
	}
	if deps.targetIDMapper == nil {
		deps.targetIDMapper = discovery.HashTargetIDMapper{}
	}
	return deps
}

func build(resolved Options, browser config.BrowserConfig) Runtime {
	deps := resolveDependencies(resolved)
	runtime := resolveRuntime(resolved.Runtime, deps.httpClient)
	var managedHTTPClient *http.Client
	if client, ok := deps.httpClient.(*http.Client); ok {
		managedHTTPClient = client
	}
	owner := &composition{
		browser:        browser,
		configDir:      resolved.ConfigDir,
		inputs:         DiscoveryInputs(browser),
		runtime:        runtime,
		catalog:        resolveCatalog(resolved.Catalog, runtime),
		managedManager: resolveManagedManager(resolved, browser),
		httpClient:     managedHTTPClient,
		activePort:     deps.activePort,
		idMapper:       deps.idMapper,
		targetIDMapper: deps.targetIDMapper,
		clock:          resolved.Clock,
		coreCandidates: make(map[string]webmcp.BrowserCandidate),
		laneCandidates: make(map[string]discovery.BrowserCandidate),
		endpoints:      make(map[string]discovery.Endpoint),
	}
	service := resolved.Discovery
	if service == nil {
		service = newDiscoveryService(resolved, browser, deps, owner)
	}
	owner.discovery = service
	runtimeDiscovery := service
	if owner.managedEnabled() {
		runtimeDiscovery = &managedDiscoveryService{owner: owner, delegate: service}
	}
	return Runtime{
		Broker:    newBroker(resolved, browser, owner),
		Discovery: runtimeDiscovery,
		Catalog:   &catalog{owner: owner},
		Close:     owner.Close,
	}
}

func resolveRuntime(runtime webmcp.BrowserRuntime, httpClient discovery.HTTPClient) webmcp.BrowserRuntime {
	if runtime != nil {
		return runtime
	}
	runtimeOptions := make([]chrome.Option, 0, 1)
	if client, ok := httpClient.(*http.Client); ok {
		runtimeOptions = append(runtimeOptions, chrome.WithHTTPClient(client))
	}
	return chrome.NewRuntime(runtimeOptions...)
}

func resolveCatalog(catalog webmcp.DevToolsCatalog, runtime webmcp.BrowserRuntime) webmcp.DevToolsCatalog {
	if catalog != nil {
		return catalog
	}
	if catalogRuntime, ok := runtime.(webmcp.DevToolsCatalog); ok {
		return catalogRuntime
	}
	return nil
}

func resolveManagedManager(resolved Options, browser config.BrowserConfig) *chrome.ManagedBrowserManager {
	manager := resolved.ManagedBrowserManager
	if manager != nil || !browser.UsesManagedBrowser() || !browser.BrowserBackendEnabled() {
		return manager
	}
	if resolved.ManagedBrowserManagerFactory != nil {
		return resolved.ManagedBrowserManagerFactory(resolved.ConfigDir)
	}
	return chrome.NewManagedBrowserManager(chrome.ManagedBrowserManagerOptions{
		ConfigDir: resolved.ConfigDir,
	})
}

func newDiscoveryService(resolved Options, browser config.BrowserConfig, deps dependencies, owner *composition) DiscoveryService {
	selectionStore := resolved.SelectionStore
	if resolved.SelectionStoreFactory != nil {
		selectionStore = resolved.SelectionStoreFactory()
	}
	serviceOptions := discovery.Options{
		HTTPClient:        &versionRecordingClient{delegate: deps.httpClient, owner: owner},
		ActivePortReader:  &activePortRecorder{delegate: deps.activePort, owner: owner},
		TargetLister:      targetLister{owner: owner},
		TargetProbe:       targetProbe{owner: owner},
		IDMapper:          deps.idMapper,
		TargetIDMapper:    deps.targetIDMapper,
		AllowedOrigins:    append([]string(nil), browser.Policy.AllowedOrigins...),
		DeniedOrigins:     append([]string(nil), browser.Policy.DeniedOrigins...),
		Clock:             resolved.Clock,
		ProcessEnumerator: nil,
		SelectionStore:    selectionstore.ForDiscovery(selectionStore),
	}
	if resolved.ProcessEnumerator != nil {
		serviceOptions.ProcessEnumerator = &processRecorder{
			delegate: resolved.ProcessEnumerator,
			owner:    owner,
		}
	}
	persistenceEnabled := browser.Selection.Persist && serviceOptions.SelectionStore != nil
	serviceOptions.PersistenceEnabled = &persistenceEnabled
	return discovery.New(serviceOptions)
}

func newBroker(resolved Options, browser config.BrowserConfig, owner *composition) webmcp.Broker {
	brokerOptions := webmcp.BrokerOptions{
		Runtime:           owner,
		Discoverer:        owner,
		ToolRefFactory:    webmcp.StableToolRef,
		CancelOnInterrupt: browser.Policy.CancelOnInterrupt,
		MaxInputBytes:     browser.Limits.MaxInputBytes,
		MaxResultBytes:    browser.Limits.MaxResultBytes,
		InvocationTimeout: browser.Limits.InvocationTimeout,
	}
	if resolved.Clock != nil {
		brokerOptions.Clock = resolved.Clock
	}
	return webmcp.NewBroker(brokerOptions)
}
