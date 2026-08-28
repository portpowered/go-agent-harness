package cli

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionBrowserBrokerFactory constructs the broker for one resolved browser
// configuration. It is called only after browser capability activation has
// been resolved, which keeps disabled sessions free of browser construction
// side effects and gives tests a neutral fake-broker seam.
type SessionBrowserBrokerFactory func(config.BrowserConfig) (webmcp.Broker, error)

// NewSessionToolCapabilitiesFactory returns the production session capability
// resolver. Static tools remain filtered by the resolved config. When browser
// tools are enabled, the resolver constructs the real broker tool set and
// preflights its namespace against the static surface before returning either
// definitions or an executor.
//
// A non-registry static executor is retained for disabled sessions for
// compatibility with injected callers. Such an executor has no inspectable
// definition namespace, so enabled browser composition advertises the broker
// surface and does not guess at hidden static names.
func NewSessionToolCapabilitiesFactory(
	staticExecutor messages.ToolExecutor,
	brokerFactory SessionBrowserBrokerFactory,
) SessionToolCapabilitiesFactory {
	if brokerFactory == nil {
		brokerFactory = NewSessionBrowserBroker
	}

	return func(cfg *config.Config) (SessionToolCapabilities, error) {
		if cfg != nil {
			if err := cfg.ValidateBrowser(); err != nil {
				return SessionToolCapabilities{}, fmt.Errorf("resolve browser config: %w", err)
			}
		}
		_, isRegistryExecutor := staticExecutor.(*cliTools.RegistryExecutor)
		var (
			resolvedStaticExecutor messages.ToolExecutor
			staticDefinitions      []messages.ToolDefinition
		)
		if isRegistryExecutor || staticExecutor == nil {
			registry := cliTools.NewToolRegistryFromConfig(cfg)
			resolvedStaticExecutor = cliTools.NewRegistryExecutor(registry)
			staticDefinitions = registry.ToAgentLoopDefs()
		} else {
			resolvedStaticExecutor = staticExecutor
		}

		if cfg == nil || !cfg.Browser.BrowserBackendEnabled() {
			return SessionToolCapabilities{
				Executor:    resolvedStaticExecutor,
				Definitions: staticDefinitions,
			}, nil
		}

		// The broker definitions are stable and side-effect free. Check their
		// namespace before asking the injected factory to allocate or dial any
		// browser resource.
		stableBrokerDefinitions := webmcpTools.NewBrokerToolSet(nil).Definitions()
		if err := cliTools.ValidateToolDefinitionNamespaces(staticDefinitions, stableBrokerDefinitions); err != nil {
			return SessionToolCapabilities{}, fmt.Errorf("compose session tools: %w", err)
		}

		broker, err := brokerFactory(cfg.Browser)
		if err != nil {
			return closeFailedBroker(broker, fmt.Errorf("construct WebMCP broker: %w", err))
		}
		if broker == nil {
			return SessionToolCapabilities{}, errors.New("construct WebMCP broker: broker factory returned nil")
		}

		brokerSet := webmcpTools.NewBrokerToolSet(broker)
		surface, err := cliTools.ComposeToolSurface(
			resolvedStaticExecutor,
			staticDefinitions,
			brokerSet.Executor(),
			brokerSet.Definitions(),
		)
		if err != nil {
			return closeFailedBroker(broker, fmt.Errorf("compose session tools: %w", err))
		}
		capabilityCoordinator := services.NewSessionCapabilityCoordinator(broker.Close)
		return SessionToolCapabilities{
			Executor:    surface.Executor,
			Definitions: surface.Definitions,
			Close:       capabilityCoordinator.Close,
		}, nil
	}
}

// NewSessionBrowserBroker creates the browser-neutral real StatefulBroker.
// Discovery and protocol adapters are intentionally not guessed here; Lane B
// and Lane D can be supplied through SessionBrowserBrokerFactory when those
// production seams are available. The broker itself remains useful for
// deterministic composition and reports classified operation failures until
// an adapter is installed.
func NewSessionBrowserBroker(browser config.BrowserConfig) (webmcp.Broker, error) {
	if err := browser.Validate(); err != nil {
		return nil, err
	}
	return webmcp.NewBroker(webmcp.BrokerOptions{
		CancelOnInterrupt: browser.Policy.CancelOnInterrupt,
		MaxInputBytes:     browser.Limits.MaxInputBytes,
		MaxResultBytes:    browser.Limits.MaxResultBytes,
		InvocationTimeout: browser.Limits.InvocationTimeout,
	}), nil
}

func closeFailedBroker(broker webmcp.Broker, primary error) (SessionToolCapabilities, error) {
	if broker == nil {
		return SessionToolCapabilities{}, primary
	}
	if closeErr := services.NewSessionCapabilityCoordinator(broker.Close).Close(); closeErr != nil {
		return SessionToolCapabilities{}, errors.Join(primary, fmt.Errorf("close WebMCP broker: %w", closeErr))
	}
	return SessionToolCapabilities{}, primary
}
