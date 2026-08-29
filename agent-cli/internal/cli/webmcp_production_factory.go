package cli

import (
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"net/http"
)

type WebMCPProductionOptions struct {
	Runtime               webmcp.BrowserRuntime
	Catalog               webmcp.DevToolsCatalog
	Discovery             WebMCPDiscoveryService
	HTTPClient            discovery.HTTPClient
	ActivePortReader      discovery.ActivePortReader
	ProcessEnumerator     discovery.ProcessEnumerator
	IDMapper              discovery.IDMapper
	TargetIDMapper        discovery.TargetIDMapper
	Clock                 discovery.Clock
	SelectionStore        any
	SelectionStoreFactory func() any
}

// WebMCPProductionOption customizes one production factory dependency.
type WebMCPProductionOption func(*WebMCPProductionOptions)

func WithWebMCPProductionRuntime(runtime webmcp.BrowserRuntime) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Runtime = runtime }
}

func WithWebMCPProductionCatalog(catalog webmcp.DevToolsCatalog) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Catalog = catalog }
}

func WithWebMCPProductionDiscovery(service WebMCPDiscoveryService) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Discovery = service }
}

func WithWebMCPProductionHTTPClient(client discovery.HTTPClient) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.HTTPClient = client }
}

func WithWebMCPProductionActivePortReader(reader discovery.ActivePortReader) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.ActivePortReader = reader }
}

func WithWebMCPProductionProcessEnumerator(enumerator discovery.ProcessEnumerator) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.ProcessEnumerator = enumerator }
}

func WithWebMCPProductionIDMapper(mapper discovery.IDMapper) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.IDMapper = mapper }
}

func WithWebMCPProductionTargetIDMapper(mapper discovery.TargetIDMapper) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.TargetIDMapper = mapper }
}

func WithWebMCPProductionClock(clock discovery.Clock) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Clock = clock }
}

func WithWebMCPProductionSelectionStore(store any) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.SelectionStore = store }
}

// WithWebMCPProductionSelectionStoreFactory defers selection-store creation
// until command execution. This keeps a parsed --config-dir override aligned
// with the store used by the direct command.
func WithWebMCPProductionSelectionStoreFactory(factory func() any) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.SelectionStoreFactory = factory }
}

// NewProductionWebMCPDoctorFactory composes browser discovery, the neutral
// broker, and Chrome's browser runtime for the actual CLI routes. Construction
// remains lazy: no browser endpoint is opened until a command invokes the
// returned factory runtime.
func NewProductionWebMCPDoctorFactory(options ...WebMCPProductionOption) WebMCPDoctorFactory {
	resolved := WebMCPProductionOptions{}
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}

	return func(browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		if err := browser.Validate(); err != nil {
			return WebMCPDoctorRuntime{}, fmt.Errorf("resolve browser config: %w", err)
		}
		if err := validateDoctorEndpoints(browser); err != nil {
			return WebMCPDoctorRuntime{}, err
		}

		httpClient := resolved.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		activePortReader := resolved.ActivePortReader
		if activePortReader == nil {
			activePortReader = discovery.FileActivePortReader{}
		}
		idMapper := resolved.IDMapper
		if idMapper == nil {
			idMapper = discovery.HashIDMapper{}
		}
		targetIDMapper := resolved.TargetIDMapper
		if targetIDMapper == nil {
			targetIDMapper = discovery.HashTargetIDMapper{}
		}

		runtime := resolved.Runtime
		if runtime == nil {
			runtimeOptions := make([]chrome.Option, 0, 1)
			if client, ok := httpClient.(*http.Client); ok {
				runtimeOptions = append(runtimeOptions, chrome.WithHTTPClient(client))
			}
			runtime = chrome.NewRuntime(runtimeOptions...)
		}
		catalog := resolved.Catalog
		if catalog == nil {
			if catalogRuntime, ok := runtime.(webmcp.DevToolsCatalog); ok {
				catalog = catalogRuntime
			}
		}

		composition := &productionWebMCPComposition{
			browser:        browser,
			inputs:         productionDiscoveryInputs(browser),
			runtime:        runtime,
			catalog:        catalog,
			activePort:     activePortReader,
			idMapper:       idMapper,
			targetIDMapper: targetIDMapper,
			clock:          resolved.Clock,
			coreCandidates: make(map[string]webmcp.BrowserCandidate),
			laneCandidates: make(map[string]discovery.BrowserCandidate),
			endpoints:      make(map[string]discovery.Endpoint),
		}

		var service WebMCPDiscoveryService = resolved.Discovery
		if service == nil {
			selectionStore := resolved.SelectionStore
			if resolved.SelectionStoreFactory != nil {
				selectionStore = resolved.SelectionStoreFactory()
			}
			serviceOptions := discovery.Options{
				HTTPClient:        &productionHTTPClient{delegate: httpClient, owner: composition},
				ActivePortReader:  &productionActivePortReader{delegate: activePortReader, owner: composition},
				TargetLister:      productionTargetLister{owner: composition},
				TargetProbe:       productionTargetProbe{owner: composition},
				IDMapper:          idMapper,
				TargetIDMapper:    targetIDMapper,
				AllowedOrigins:    append([]string(nil), browser.Policy.AllowedOrigins...),
				DeniedOrigins:     append([]string(nil), browser.Policy.DeniedOrigins...),
				Clock:             resolved.Clock,
				ProcessEnumerator: nil,
				SelectionStore:    productionSelectionStore(selectionStore),
			}
			if resolved.ProcessEnumerator != nil {
				serviceOptions.ProcessEnumerator = &productionProcessEnumerator{
					delegate: resolved.ProcessEnumerator,
					owner:    composition,
				}
			}
			persistenceEnabled := browser.Selection.Persist && serviceOptions.SelectionStore != nil
			serviceOptions.PersistenceEnabled = &persistenceEnabled
			service = discovery.New(serviceOptions)
		}
		composition.discovery = service

		brokerOptions := webmcp.BrokerOptions{
			Runtime:           composition,
			Discoverer:        composition,
			ToolRefFactory:    webmcp.StableToolRef,
			CancelOnInterrupt: browser.Policy.CancelOnInterrupt,
			MaxInputBytes:     browser.Limits.MaxInputBytes,
			MaxResultBytes:    browser.Limits.MaxResultBytes,
			InvocationTimeout: browser.Limits.InvocationTimeout,
		}
		if resolved.Clock != nil {
			brokerOptions.Clock = resolved.Clock
		}
		broker := webmcp.NewBroker(brokerOptions)

		var closeService func() error
		if closer, ok := service.(interface{ Close() error }); ok {
			closeService = closer.Close
		}
		return WebMCPDoctorRuntime{
			Broker:    broker,
			Discovery: service,
			Catalog:   &productionWebMCPCatalog{owner: composition},
			Close:     closeService,
		}, nil
	}
}

// configDirForGlobalFlags is kept at the composition boundary so the route
// can give the production factory the same selection path as direct commands.
func configDirForGlobalFlags(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.ConfigDir()
}

// defaultWebMCPDoctorFactory is the single default composition used by
// direct commands and router fallbacks. The selection store is created only
// after flags have been parsed, so --config-dir applies consistently without
// making command construction touch the filesystem.
func defaultWebMCPDoctorFactory(globalFlags *flags.GlobalFlags) WebMCPDoctorFactory {
	return NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionSelectionStoreFactory(func() any {
			return NewFileWebMCPSelectionStore(configDirForGlobalFlags(globalFlags))
		}),
	)
}

func webmcpRuntimeUnavailableError(phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime is unavailable", map[string]any{
		"phase": phase,
	})
}

func webmcpRuntimeFactoryError(err error) error {
	if err == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return err
	}
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime could not be constructed", map[string]any{
		"phase": "runtime_factory",
	})
}

func productionDiscoveryInputs(browser config.BrowserConfig) discovery.ConnectionInputs {
	return discovery.ConnectionInputs{
		CDPURL:            browser.Connection.CDPURL,
		BrowserWSEndpoint: browser.Connection.WSEndpoint,
		UserDataDir:       browser.Connection.UserDataDir,
		AllowProcessScan:  browser.Connection.AllowProcessScan,
		AllowRemoteCDP:    browser.Connection.AllowRemoteCDP,
	}
}
