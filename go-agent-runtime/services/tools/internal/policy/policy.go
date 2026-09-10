package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// Factory owns interactive policy construction. It is exposed to host code
// only through the tools.InteractiveToolPolicyFactory contract and Wire.
type Factory struct{}

func New() *Factory {
	return &Factory{}
}

// Resolve creates a value snapshot from one advertised definition surface.
// The input slices are read during construction and are never retained.
func (f *Factory) Resolve(request public.InteractiveToolPolicyRequest) (public.InteractiveToolPolicy, error) {
	settings := request.Settings
	if settings == (public.InteractiveToolPolicySettings{}) {
		settings = defaultSettings()
	}
	if err := f.ValidateSettings(settings); err != nil {
		return nil, fmt.Errorf("resolve interactive tool policy: %w", err)
	}

	return &snapshot{
		settings:           settings,
		classes:            classesForDefinitions(request.Definitions, request.BaseDefinitions, request.ExplicitLongRunningNames),
		dynamicLongRunning: request.DynamicLongRunning,
	}, nil
}

func defaultSettings() public.InteractiveToolPolicySettings {
	return public.InteractiveToolPolicySettings{
		FastReadTimeout:          public.DefaultInteractiveFastReadTimeout,
		LongRunningTimeout:       public.DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: public.DefaultInteractiveAcknowledgementThreshold,
	}
}

func (f *Factory) ValidateSettings(settings public.InteractiveToolPolicySettings) error {
	if settings.FastReadTimeout <= 0 || settings.FastReadTimeout >= public.InteractiveFastReadTimeoutLimit {
		return fmt.Errorf("tools.interactive.fast_read_timeout must be positive and less than 10s; got %s", settings.FastReadTimeout)
	}
	if settings.LongRunningTimeout <= 0 || settings.LongRunningTimeout >= public.InteractiveLongRunningTimeoutLimit {
		return fmt.Errorf("tools.interactive.long_running_timeout must be positive and less than 30s; got %s", settings.LongRunningTimeout)
	}
	if settings.AcknowledgementThreshold <= 0 || settings.AcknowledgementThreshold > public.InteractiveAcknowledgementThresholdLimit {
		return fmt.Errorf("tools.interactive.acknowledgement_threshold must be positive and no greater than 2s; got %s", settings.AcknowledgementThreshold)
	}
	if settings.LongRunningTimeout <= settings.AcknowledgementThreshold {
		return fmt.Errorf("tools.interactive.long_running_timeout must exceed tools.interactive.acknowledgement_threshold; got %s and %s", settings.LongRunningTimeout, settings.AcknowledgementThreshold)
	}
	return nil
}

func classForName(name string, longRunningNames map[string]struct{}) public.InteractiveToolClass {
	if _, ok := longRunningNames[strings.TrimSpace(name)]; ok {
		return public.InteractiveToolClassBoundedLongRunning
	}
	return public.InteractiveToolClassFastRead
}

func classesForDefinitions(definitions, baseDefinitions []messages.ToolDefinition, explicitLongRunningNames []string) map[string]public.InteractiveToolClass {
	baseNames := normalizedNames(baseDefinitions)
	longRunningNames := longRunningNamesFor(explicitLongRunningNames)
	classes := make(map[string]public.InteractiveToolClass, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		class := classForName(name, longRunningNames)
		if len(baseNames) > 0 && !containsName(baseNames, name) {
			// A definition beyond the immutable base is a remote or
			// otherwise dynamically resolved operation. It receives the
			// bounded budget even when its name is not in the static list.
			class = public.InteractiveToolClassBoundedLongRunning
		}
		classes[name] = class
	}
	return classes
}

func normalizedNames(definitions []messages.ToolDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if name := strings.TrimSpace(definition.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func longRunningNamesFor(explicitNames []string) map[string]struct{} {
	names := map[string]struct{}{"exec": {}, "sleep": {}}
	for _, name := range explicitNames {
		if name = strings.TrimSpace(name); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func containsName(names map[string]struct{}, name string) bool {
	_, ok := names[name]
	return ok
}

type snapshot struct {
	settings           public.InteractiveToolPolicySettings
	classes            map[string]public.InteractiveToolClass
	dynamicLongRunning bool
}

func (p *snapshot) Settings() public.InteractiveToolPolicySettings {
	return p.settings
}

// ClassForTool preserves exact lookup for an advertised name. Constructor
// normalization trims definitions, but a caller's lookup string is not
// silently rewritten.
func (p *snapshot) ClassForTool(name string) public.InteractiveToolClass {
	if class, ok := p.classes[name]; ok {
		return class
	}
	if p.dynamicLongRunning {
		return public.InteractiveToolClassBoundedLongRunning
	}
	return public.InteractiveToolClassFastRead
}

func (p *snapshot) TimeoutForTool(name string) time.Duration {
	if p.ClassForTool(name) == public.InteractiveToolClassBoundedLongRunning {
		return p.settings.LongRunningTimeout
	}
	return p.settings.FastReadTimeout
}

func (p *snapshot) Clone() public.InteractiveToolPolicy {
	clone := *p
	clone.classes = make(map[string]public.InteractiveToolClass, len(p.classes))
	for name, class := range p.classes {
		clone.classes[name] = class
	}
	return &clone
}

func (p *snapshot) Validate() error {
	return New().ValidateSettings(p.settings)
}

var _ public.InteractiveToolPolicyFactory = (*Factory)(nil)
var _ public.InteractiveToolPolicy = (*snapshot)(nil)
