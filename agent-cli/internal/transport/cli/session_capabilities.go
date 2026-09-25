package cli

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/sessionbroker"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

type SessionBrowserBrokerFactory func(config.BrowserConfig) (webmcp.Broker, error)

type SessionDisplayCapability = cliTools.DisplayCapability

const sessionDisplayCapabilityProbeTimeout = 3 * time.Second

func NewSessionToolCapabilitiesFactory(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory) SessionToolCapabilitiesFactory {
	return NewSessionToolCapabilitiesFactoryWithDisplaySurface(staticExecutor, brokerFactory, nil)
}

// NewSessionToolCapabilitiesFactoryFromService adapts the injected service
// contract to the command's existing lifecycle value without constructing a
// registry or browser in the transport.
func NewSessionToolCapabilitiesFactoryFromService(resolver serviceTools.Service) SessionToolCapabilitiesFactory {
	return sessionbroker.FactoryFromService(resolver)
}

func NewSessionToolCapabilitiesFactoryWithDisplaySurface(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displaySurface cliTools.DisplaySurface) SessionToolCapabilitiesFactory {
	if displaySurface == nil {
		displaySurface = cliTools.NewHostDisplaySurface()
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, displaySurface, displaySurface)
}

func NewSessionToolCapabilitiesFactoryWithDisplayProbe(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displayProbe cliTools.DisplayCapabilityProbe) SessionToolCapabilitiesFactory {
	surface := cliTools.NewHostDisplaySurface()
	if displayProbe == nil {
		displayProbe = surface
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, surface, displayProbe)
}

func newSessionToolCapabilitiesFactory(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe) SessionToolCapabilitiesFactory {
	browserFactory := func(browser config.BrowserConfig, configDir string) (serviceTools.BrowserCapability, error) {
		var broker webmcp.Broker
		var err error
		if brokerFactory != nil {
			broker, err = brokerFactory(browser)
		} else {
			broker, err = newSessionBrowserBrokerWithConfigDir(browser, configDir)
		}
		return sessionbroker.ServiceCapability(broker), err
	}
	resolver := servicewire.NewToolCapabilitiesService(staticExecutor, browserFactory, displaySurface, displayProbe, runtimeToolsWire.NewService())
	return NewSessionToolCapabilitiesFactoryFromService(resolver)
}

// NewSessionBrowserCapability adapts the low-level browser runtime capability.
func NewSessionBrowserCapability(broker webmcp.Broker) serviceTools.BrowserCapability {
	return sessionbroker.ServiceCapability(broker)
}

// newSessionBrowserBrokerWithDoctorFactory wraps one factory runtime in the
// session capability broker; see sessionbroker.NewFromFactory.
func newSessionBrowserBrokerWithDoctorFactory(browser config.BrowserConfig, factory WebMCPDoctorFactory) (webmcp.Broker, error) {
	return sessionbroker.NewFromFactory(browser, factory)
}
