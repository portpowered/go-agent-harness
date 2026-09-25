package cli

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// The production WebMCP composition lives in internal/webmcp/production.
// This file only adapts it to the CLI's doctor/direct-command seams.

// WebMCPProductionOptions holds the injectable production dependencies.
type WebMCPProductionOptions = production.Options

// WebMCPProductionOption customizes one production factory dependency.
type WebMCPProductionOption = production.Option

// WithWebMCPProductionRuntime injects the raw browser runtime.
func WithWebMCPProductionRuntime(runtime webmcp.BrowserRuntime) WebMCPProductionOption {
	return production.WithRuntime(runtime)
}

// WithWebMCPProductionCatalog injects the DevTools catalog.
func WithWebMCPProductionCatalog(catalog webmcp.DevToolsCatalog) WebMCPProductionOption {
	return production.WithCatalog(catalog)
}

// WithWebMCPProductionDiscovery injects the discovery service.
func WithWebMCPProductionDiscovery(service WebMCPDiscoveryService) WebMCPProductionOption {
	return production.WithDiscovery(service)
}

// WithWebMCPProductionConfigDir keeps managed-browser state and selection
// persistence on the same resolved config directory.
func WithWebMCPProductionConfigDir(configDir string) WebMCPProductionOption {
	return production.WithConfigDir(configDir)
}

// WithWebMCPProductionManagedBrowserManager injects the managed-browser
// lifecycle manager.
func WithWebMCPProductionManagedBrowserManager(manager *chrome.ManagedBrowserManager) WebMCPProductionOption {
	return production.WithManagedBrowserManager(manager)
}

// WithWebMCPProductionManagedBrowserManagerFactory defers manager creation
// until the browser configuration has been resolved.
func WithWebMCPProductionManagedBrowserManagerFactory(factory func(string) *chrome.ManagedBrowserManager) WebMCPProductionOption {
	return production.WithManagedBrowserManagerFactory(factory)
}

// WithWebMCPProductionHTTPClient injects the discovery HTTP client.
func WithWebMCPProductionHTTPClient(client discovery.HTTPClient) WebMCPProductionOption {
	return production.WithHTTPClient(client)
}

// WithWebMCPProductionActivePortReader injects the DevToolsActivePort reader.
func WithWebMCPProductionActivePortReader(reader discovery.ActivePortReader) WebMCPProductionOption {
	return production.WithActivePortReader(reader)
}

// WithWebMCPProductionProcessEnumerator injects the process enumerator.
func WithWebMCPProductionProcessEnumerator(enumerator discovery.ProcessEnumerator) WebMCPProductionOption {
	return production.WithProcessEnumerator(enumerator)
}

// WithWebMCPProductionIDMapper injects the browser ID mapper.
func WithWebMCPProductionIDMapper(mapper discovery.IDMapper) WebMCPProductionOption {
	return production.WithIDMapper(mapper)
}

// WithWebMCPProductionTargetIDMapper injects the target ID mapper.
func WithWebMCPProductionTargetIDMapper(mapper discovery.TargetIDMapper) WebMCPProductionOption {
	return production.WithTargetIDMapper(mapper)
}

// WithWebMCPProductionClock injects the discovery and broker clock.
func WithWebMCPProductionClock(clock discovery.Clock) WebMCPProductionOption {
	return production.WithClock(clock)
}

// WithWebMCPProductionSelectionStore injects the selection store.
func WithWebMCPProductionSelectionStore(store any) WebMCPProductionOption {
	return production.WithSelectionStore(store)
}

// WithWebMCPProductionSelectionStoreFactory defers selection-store creation
// until command execution. This keeps a parsed --config-dir override aligned
// with the store used by the direct command.
func WithWebMCPProductionSelectionStoreFactory(factory func() any) WebMCPProductionOption {
	return production.WithSelectionStoreFactory(factory)
}

// NewProductionWebMCPDoctorFactory validates the resolved browser
// configuration at the command boundary and then delegates composition to
// the production package. Construction remains lazy: no browser endpoint is
// opened until a command invokes the returned factory runtime.
func NewProductionWebMCPDoctorFactory(options ...WebMCPProductionOption) WebMCPDoctorFactory {
	build := production.NewFactory(options...)
	return func(browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		if err := browser.Validate(); err != nil {
			return WebMCPDoctorRuntime{}, fmt.Errorf("resolve browser config: %w", err)
		}
		if err := validateDoctorEndpoints(browser); err != nil {
			return WebMCPDoctorRuntime{}, err
		}
		runtime, err := build(browser)
		if err != nil {
			return WebMCPDoctorRuntime{}, err
		}
		return WebMCPDoctorRuntime{
			Broker:    runtime.Broker,
			Discovery: runtime.Discovery,
			Catalog:   runtime.Catalog,
			Close:     runtime.Close,
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
		WithWebMCPProductionConfigDir(configDirForGlobalFlags(globalFlags)),
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
	return production.DiscoveryInputs(browser)
}

func productionDiscoveryError(err error) error {
	return normalize.DiscoveryError(err)
}
