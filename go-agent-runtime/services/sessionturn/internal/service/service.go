// Package service assembles the private session-turn implementations behind
// the public sessionturn contract.
package service

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/toolexec"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const errNoPolicyFactory sessionturn.Error = "interactive tool policy factory is not configured"

// Service implements sessionturn.Service. It is stateless; every value is
// request scoped.
type Service struct {
	policies tools.InteractiveToolPolicyFactory
}

var _ sessionturn.Service = (*Service)(nil)

// New assembles the service from the interactive policy factory.
func New(policies tools.InteractiveToolPolicyFactory) *Service {
	return &Service{policies: policies}
}

// NewToolExecutor creates the session-owned executor.
func (s *Service) NewToolExecutor(request sessionturn.ToolExecutorRequest) messages.ToolExecutor {
	return toolexec.New(request)
}

// ResolveInteractivePolicy resolves one immutable policy snapshot. Nil
// settings select the runtime defaults.
func (s *Service) ResolveInteractivePolicy(request sessionturn.InteractivePolicyRequest) (tools.InteractiveToolPolicy, error) {
	if s.policies == nil {
		return nil, errNoPolicyFactory
	}
	settings := tools.InteractiveToolPolicySettings{
		FastReadTimeout:          tools.DefaultInteractiveFastReadTimeout,
		LongRunningTimeout:       tools.DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: tools.DefaultInteractiveAcknowledgementThreshold,
	}
	if request.Settings != nil {
		settings = *request.Settings
	}
	return s.policies.Resolve(tools.InteractiveToolPolicyRequest{
		Settings:                 settings,
		Definitions:              request.Definitions,
		BaseDefinitions:          request.BaseDefinitions,
		ExplicitLongRunningNames: browserLongRunningToolNames(),
		DynamicLongRunning:       request.DynamicLongRunning,
	})
}

// browserLongRunningToolNames are remote interactive broker operations whose
// one call spans a browser round trip.
func browserLongRunningToolNames() []string {
	return []string{
		tools.SelectTabToolName,
		tools.InvokeToolName,
		tools.ListToolsToolName,
		tools.ListTabsToolName,
		tools.GetContextToolName,
		tools.CancelToolName,
		tools.ListCastDevicesToolName,
		tools.CastTabToolName,
		tools.StopCastingToolName,
	}
}
