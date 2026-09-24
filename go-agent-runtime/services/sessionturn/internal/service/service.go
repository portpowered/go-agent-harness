// Package service assembles the private session-turn implementations behind
// the public sessionturn contract.
package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/publication"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/sessionwrap"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/toolexec"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/turns"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const errNoPolicyFactory sessionturn.Error = "interactive tool policy factory is not configured"

// Service implements sessionturn.Service. Its only state is the text-seed
// wire prompt allocator; every other value is request scoped.
type Service struct {
	instructions session.InstructionService
	policies     tools.InteractiveToolPolicyFactory
	staging      tools.ImageStaging
	wirePrompts  sessionwrap.WirePrompts
}

var _ sessionturn.Service = (*Service)(nil)

// New assembles the service from its peer runtime capabilities.
func New(instructions session.InstructionService, policies tools.InteractiveToolPolicyFactory, staging tools.ImageStaging) *Service {
	return &Service{instructions: instructions, policies: policies, staging: staging}
}

// NewTurns creates an idle persistent turn session.
func (s *Service) NewTurns(opts sessionturn.TurnsOptions) sessionturn.Turns {
	return turns.New(opts)
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

// StartPublication starts a publisher, or returns an inert publication when
// the session has no browser watch or refresh.
func (s *Service) StartPublication(ctx context.Context, request sessionturn.PublicationRequest) sessionturn.Publication {
	if request.Watch == nil || request.Refresh == nil {
		return publication.Inert{}
	}
	publisher := publication.New(request)
	publisher.Start(ctx)
	return publisher
}

// MergeToolDefinitions keeps the base surface and adds non-colliding names.
func (s *Service) MergeToolDefinitions(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return publication.Merge(base, definitions)
}

// ToolDefinitionDigest identifies a canonical surface.
func (s *Service) ToolDefinitionDigest(definitions []messages.ToolDefinition) (string, error) {
	return publication.Digest(definitions)
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
