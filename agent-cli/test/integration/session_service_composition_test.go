package integration

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
)

// Tests compose the same runtime and use-case services as the application graph.
func newTestSessionService(deps sessionservicewire.SessionDependencies) agentsession.SessionService {
	deps.Runtime = sessionservicewire.NewSessionRuntime(deps.Clock, deps.ToolService, sessionservicewire.NewSessionRuntimeFactory(), deps.RuntimeFactory, deps.SessionInferencer, deps.ToolExecutor, deps.DeviceRegistry, deps.RuntimeObserver, deps.MetricSampler, deps.Logger)
	return sessionservicewire.NewSessionService(deps)
}
