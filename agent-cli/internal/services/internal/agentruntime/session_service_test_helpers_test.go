package agentruntime_test

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// newInjectedSessionService keeps moved CLI tests on the same explicit graph
// as production. The old tests supplied only a clock to NewSessionService;
// that no longer constructs a runtime, so this test-only adapter composes the
// runtime once and preserves each test's inferencer/device seams.
func newInjectedSessionService(deps servicewire.SessionDependencies) serviceSession.SessionService {
	if deps.Clock == nil {
		deps.Clock = platformclock.Real{}
	}
	if deps.Runtime == nil {
		factory := servicewire.NewSessionRuntimeFactory()
		deps.Runtime = servicewire.NewSessionRuntime(
			deps.Clock,
			deps.ToolService,
			factory,
			deps.RuntimeFactory,
			deps.SessionInferencer,
			deps.ToolExecutor,
			deps.DeviceRegistry,
			deps.RuntimeObserver,
			deps.MetricSampler,
			deps.Logger,
		)
	}
	return servicewire.NewSessionService(deps)
}

func browserTestToolService(closeCount *int) serviceTools.Service {
	return serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			Definitions:            []messages.ToolDefinition{{Name: "browser_test"}},
			BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected,
			Initialize:             func(context.Context) error { return nil },
			Close: func() error {
				if closeCount != nil {
					(*closeCount)++
				}
				return nil
			},
		}, nil
	})
}
