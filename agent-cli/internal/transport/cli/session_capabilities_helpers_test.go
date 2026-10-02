package cli

import (
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// NewSessionToolCapabilitiesFactoryWithDisplayProbe resolves display
// admission through displayProbe. Options such as
// servicewire.WithDisplayProbeTimeout tune how long admission waits for it.
func NewSessionToolCapabilitiesFactoryWithDisplayProbe(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displayProbe cliTools.DisplayCapabilityProbe, options ...servicewire.ToolCapabilitiesOption) SessionToolCapabilitiesFactory {
	surface := cliTools.NewHostDisplaySurface()
	if displayProbe == nil {
		displayProbe = surface
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, surface, displayProbe, options...)
}
