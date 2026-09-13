package wire

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// interactiveToolPolicyService is the one host adapter for the interactive
// policy. It translates a loaded CLI config and the CLI's WebMCP vocabulary
// into the host-neutral runtime request, then carries the resulting immutable
// snapshot beside the request-scoped tool surface.
type interactiveToolPolicyService struct {
	inner   serviceTools.Service
	factory runtimeTools.InteractiveToolPolicyFactory
}

func newInteractiveToolPolicyService(inner serviceTools.Service) serviceTools.Service {
	if inner == nil {
		return nil
	}
	return &interactiveToolPolicyService{
		inner:   inner,
		factory: runtimeToolsWire.NewInteractiveToolPolicy(),
	}
}

func (s *interactiveToolPolicyService) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	if s == nil || s.inner == nil {
		return serviceTools.Capabilities{}, fmt.Errorf("interactive tool policy service is not configured")
	}

	settings, err := resolveInteractiveToolPolicySettings(cfg)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	request := runtimeTools.InteractiveToolPolicyRequest{
		Settings:                 settings,
		ExplicitLongRunningNames: browserLongRunningToolNames(),
		DynamicLongRunning:       browserToolsAreDynamic(cfg),
	}
	// Validate before asking the underlying capability service to construct a
	// browser, registry, or any other request-scoped resource.
	if _, err := s.factory.Resolve(request); err != nil {
		return serviceTools.Capabilities{}, fmt.Errorf("resolve interactive tool policy: %w", err)
	}

	capabilities, err := s.inner.Resolve(cfg)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	request.Definitions = cloneToolDefinitions(capabilities.Definitions)
	request.BaseDefinitions = cloneToolDefinitions(capabilities.Definitions)
	policy, err := s.factory.Resolve(request)
	if err != nil {
		return serviceTools.Capabilities{}, fmt.Errorf("resolve interactive tool policy: %w", err)
	}
	capabilities.InteractiveToolPolicy = policy.Clone()
	return capabilities, nil
}

func resolveInteractiveToolPolicySettings(cfg *config.Config) (runtimeTools.InteractiveToolPolicySettings, error) {
	resolved := config.DefaultInteractiveToolConfig()
	if cfg != nil {
		var err error
		resolved, err = cfg.ResolveInteractiveToolConfig()
		if err != nil {
			return runtimeTools.InteractiveToolPolicySettings{}, fmt.Errorf("resolve interactive tool policy: %w", err)
		}
	}
	return runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          resolved.FastReadTimeout,
		LongRunningTimeout:       resolved.LongRunningTimeout,
		AcknowledgementThreshold: resolved.AcknowledgementThreshold,
	}, nil
}

func browserToolsAreDynamic(cfg *config.Config) bool {
	return cfg != nil && cfg.Browser.BrowserBackendEnabled()
}

func browserLongRunningToolNames() []string {
	return []string{
		webmcp.SelectTabToolName,
		webmcp.InvokeToolName,
		webmcp.ListToolsToolName,
		webmcp.ListTabsToolName,
		webmcp.GetContextToolName,
		webmcp.CancelToolName,
		webmcp.ListCastDevicesToolName,
		webmcp.CastTabToolName,
		webmcp.StopCastingToolName,
	}
}

func cloneToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return append([]messages.ToolDefinition(nil), definitions...)
}
