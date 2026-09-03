package services

import (
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// InteractiveToolClass determines which voice/realtime budget applies to an
// admitted tool call.
type InteractiveToolClass string

const (
	// InteractiveToolClassFastRead is the safe default for read-shaped and
	// unknown calls.
	InteractiveToolClassFastRead InteractiveToolClass = "fast/read"
	// InteractiveToolClassBoundedLongRunning is reserved for operations that
	// are intentionally allowed to outlive the fast/read budget.
	InteractiveToolClassBoundedLongRunning InteractiveToolClass = "bounded-long-running"
)

// InteractiveToolPolicy is an immutable per-session snapshot of interactive
// tool budgets and the class selected for every admitted definition. The
// class map is private and every constructor/clone copies it, so parallel
// sessions cannot mutate one another's timeout state.
type InteractiveToolPolicy struct {
	FastReadTimeout          time.Duration
	LongRunningTimeout       time.Duration
	AcknowledgementThreshold time.Duration

	toolClasses        map[string]InteractiveToolClass
	dynamicLongRunning bool
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
	if settings == (config.InteractiveToolConfig{}) {
		settings = config.DefaultInteractiveToolConfig()
	}
	if err := settings.Validate(); err != nil {
		return InteractiveToolPolicy{}, fmt.Errorf("resolve interactive tool policy: %w", err)
	}

	baseNames := make(map[string]struct{}, len(baseDefinitions))
	for _, definition := range baseDefinitions {
		if name := strings.TrimSpace(definition.Name); name != "" {
			baseNames[name] = struct{}{}
		}
	}
	classes := make(map[string]InteractiveToolClass, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		class := interactiveToolClassForName(name)
		if len(baseNames) > 0 {
			if _, inBase := baseNames[name]; !inBase {
				// A definition beyond the immutable base is a first-class page
				// tool resolved against the live browser catalog.
				class = InteractiveToolClassBoundedLongRunning
			}
		}
		classes[name] = class
	}
	return InteractiveToolPolicy{
		FastReadTimeout:          settings.FastReadTimeout,
		LongRunningTimeout:       settings.LongRunningTimeout,
		AcknowledgementThreshold: settings.AcknowledgementThreshold,
		toolClasses:              classes,
		dynamicLongRunning:       browserDynamic,
	}, nil
}

// ResolveInteractiveToolPolicy derives a policy from one loaded configuration
// snapshot. It does not mutate cfg or definitions.
func ResolveInteractiveToolPolicy(cfg *config.Config, definitions []messages.ToolDefinition) (InteractiveToolPolicy, error) {
	if cfg == nil {
		return NewInteractiveToolPolicy(config.DefaultInteractiveToolConfig(), definitions)
	}
	settings, err := cfg.ResolveInteractiveToolConfig()
	if err != nil {
		return InteractiveToolPolicy{}, fmt.Errorf("resolve interactive tool policy: %w", err)
	}
	return NewInteractiveToolPolicy(settings, definitions)
}

// Clone returns an independent policy snapshot suitable for handing to a
// runtime plan or executor.
func (p InteractiveToolPolicy) Clone() InteractiveToolPolicy {
	clone := p
	clone.toolClasses = make(map[string]InteractiveToolClass, len(p.toolClasses))
	for name, class := range p.toolClasses {
		clone.toolClasses[name] = class
	}
	return clone
}

// ClassForTool returns the resolved class for an admitted tool. A name that
// was not part of the advertised snapshot falls back to the fast/read budget
// - unless the session serves dynamic browser tools, whose mid-session
// registrations are remote interactive operations and keep the bounded
// long-running budget.
func (p InteractiveToolPolicy) ClassForTool(name string) InteractiveToolClass {
	if class, ok := p.toolClasses[name]; ok {
		return class
	}
	if p.dynamicLongRunning {
		return InteractiveToolClassBoundedLongRunning
	}
	return InteractiveToolClassFastRead
}

// TimeoutForTool returns the deadline for one call in this policy snapshot.
func (p InteractiveToolPolicy) TimeoutForTool(name string) time.Duration {
	if p.ClassForTool(name) == InteractiveToolClassBoundedLongRunning {
		return p.LongRunningTimeout
	}
	return p.FastReadTimeout
}

// Validate checks that a policy supplied directly by a service caller still
// satisfies the same bounds as configuration-derived policies.
func (p InteractiveToolPolicy) Validate() error {
	return (config.InteractiveToolConfig{
		FastReadTimeout:          p.FastReadTimeout,
		LongRunningTimeout:       p.LongRunningTimeout,
		AcknowledgementThreshold: p.AcknowledgementThreshold,
	}).Validate()
}

// interactiveToolClassForName is intentionally small and explicit. Every
// other current or future tool remains safe by using fast/read until it is
// deliberately admitted as long-running. The stable WebMCP broker tools are
// admitted: select_tab performs attach+enable+catalog against a real browser
// and invoke runs a real page tool to its terminal status - both routinely
// exceed a local-read deadline on a loaded machine (gate probe 11: composed
// page reads died at the 5s fast/read budget while the browser was healthy).
func interactiveToolClassForName(name string) InteractiveToolClass {
	switch strings.TrimSpace(name) {
	case "exec", "sleep":
		return InteractiveToolClassBoundedLongRunning
	case webmcp.SelectTabToolName, webmcp.InvokeToolName, webmcp.ListToolsToolName,
		webmcp.ListTabsToolName, webmcp.GetContextToolName, webmcp.CancelToolName,
		webmcp.ListCastDevicesToolName, webmcp.CastTabToolName, webmcp.StopCastingToolName:
		return InteractiveToolClassBoundedLongRunning
	default:
		return InteractiveToolClassFastRead
	}
}
