package agentruntime

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// InteractiveToolClass is retained as a CLI compatibility alias while the
// reusable tools service owns the runtime contract.
type InteractiveToolClass = runtimeTools.InteractiveToolClass

const (
	InteractiveToolClassFastRead           = runtimeTools.InteractiveToolClassFastRead
	InteractiveToolClassBoundedLongRunning = runtimeTools.InteractiveToolClassBoundedLongRunning
)

// InteractiveToolPolicy preserves the CLI's value-shaped fields and methods.
// Resolution, classification, validation, and snapshot cloning are delegated
// to the reusable tools service.
type InteractiveToolPolicy struct {
	FastReadTimeout          time.Duration
	LongRunningTimeout       time.Duration
	AcknowledgementThreshold time.Duration

	runtimePolicy runtimeTools.InteractiveToolPolicy
}

// NewInteractiveToolPolicy resolves a session-local policy from operator
// configuration and the exact definition snapshot that will be advertised to
// the provider. Names not present in that snapshot use the fast/read fallback.
func NewInteractiveToolPolicy(settings config.InteractiveToolConfig, definitions []messages.ToolDefinition) (InteractiveToolPolicy, error) {
	return NewInteractiveToolPolicyForSession(settings, definitions, nil, false)
}

// NewInteractiveToolPolicyForSession additionally classifies browser work.
// Definitions beyond the immutable base surface are dynamic page tools:
// remote interactive operations whose one call spans a CDP round trip (and,
// on a cold broker, dial+attach+enable+catalog before the page even runs), so
// they are admitted to the bounded long-running budget instead of the
// fast/read deadline designed for local reads. When browserDynamic is set,
// names outside the snapshot (page tools registered mid-session) inherit the
// same bounded budget instead of the fast/read fallback.
func NewInteractiveToolPolicyForSession(
	settings config.InteractiveToolConfig,
	definitions []messages.ToolDefinition,
	baseDefinitions []messages.ToolDefinition,
	browserDynamic bool,
) (InteractiveToolPolicy, error) {
	factory := runtimeToolsWire.NewInteractiveToolPolicy()
	resolved, err := factory.Resolve(runtimeTools.InteractiveToolPolicyRequest{
		Settings:                 interactiveToolPolicySettings(settings),
		Definitions:              definitions,
		BaseDefinitions:          baseDefinitions,
		ExplicitLongRunningNames: browserLongRunningToolNames(),
		DynamicLongRunning:       browserDynamic,
	})
	if err != nil {
		return InteractiveToolPolicy{}, err
	}
	return newInteractiveToolPolicyCompatibilityValue(resolved), nil
}

// ResolveInteractiveToolPolicy derives a policy from one loaded configuration
// snapshot. It does not mutate cfg or definitions.
func ResolveInteractiveToolPolicy(cfg *config.Config, definitions []messages.ToolDefinition) (InteractiveToolPolicy, error) {
	if cfg == nil {
		return NewInteractiveToolPolicy(config.DefaultInteractiveToolConfig(), definitions)
	}
	return NewInteractiveToolPolicy(cfg.Tools.Interactive, definitions)
}

// Clone returns an independent policy snapshot suitable for handing to a
// runtime plan or executor.
func (p InteractiveToolPolicy) Clone() InteractiveToolPolicy {
	clone := p
	if p.runtimePolicy != nil {
		clone.runtimePolicy = p.runtimePolicy.Clone()
	}
	return clone
}

// ClassForTool returns the resolved class for an admitted tool. A name that
// was not part of the advertised snapshot falls back to the fast/read budget
// - unless the session serves dynamic browser tools, whose mid-session
// registrations are remote interactive operations and keep the bounded
// long-running budget.
func (p InteractiveToolPolicy) ClassForTool(name string) InteractiveToolClass {
	if p.runtimePolicy != nil {
		return p.runtimePolicy.ClassForTool(name)
	}
	return InteractiveToolClassFastRead
}

// TimeoutForTool returns the deadline for one call in this policy snapshot.
func (p InteractiveToolPolicy) TimeoutForTool(name string) time.Duration {
	if p.runtimePolicy != nil {
		return p.runtimePolicy.TimeoutForTool(name)
	}
	if p.ClassForTool(name) == InteractiveToolClassBoundedLongRunning {
		return p.LongRunningTimeout
	}
	return p.FastReadTimeout
}

// Validate checks that a policy supplied directly by a service caller still
// satisfies the same bounds as configuration-derived policies.
func (p InteractiveToolPolicy) Validate() error {
	if p.runtimePolicy != nil {
		return p.runtimePolicy.Validate()
	}
	return runtimeToolsWire.NewInteractiveToolPolicy().ValidateSettings(runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          p.FastReadTimeout,
		LongRunningTimeout:       p.LongRunningTimeout,
		AcknowledgementThreshold: p.AcknowledgementThreshold,
	})
}

func interactiveToolPolicySettings(settings config.InteractiveToolConfig) runtimeTools.InteractiveToolPolicySettings {
	return runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          settings.FastReadTimeout,
		LongRunningTimeout:       settings.LongRunningTimeout,
		AcknowledgementThreshold: settings.AcknowledgementThreshold,
	}
}

func newInteractiveToolPolicyCompatibilityValue(policy runtimeTools.InteractiveToolPolicy) InteractiveToolPolicy {
	settings := policy.Settings()
	return InteractiveToolPolicy{
		FastReadTimeout:          settings.FastReadTimeout,
		LongRunningTimeout:       settings.LongRunningTimeout,
		AcknowledgementThreshold: settings.AcknowledgementThreshold,
		runtimePolicy:            policy,
	}
}

// browserLongRunningToolNames is the CLI's explicit value conversion. The
// runtime policy package receives names only and never imports WebMCP.
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
