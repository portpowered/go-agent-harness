package registry

import (
	"fmt"
	"io"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/misc"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/mouse"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/shell"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/web"
)

// RegistryOptions contains normalized host policy for one registry snapshot.
// It deliberately carries no config-file or CLI types across the service
// boundary.
type RegistryOptions struct {
	Selections []public.ToolSelection
	Exec       public.ExecPolicy
}

func NewToolRegistry() *ToolRegistry {
	return NewToolRegistryWithPolicyAndSkillRoots(RegistryOptions{}, nil, nil, io.Discard)
}

// NewEmptyToolRegistry creates a registry with no tools. Callers that compose
// a participant- or session-specific allowlist can register only the selected
// tools without accidentally inheriting the default registry.
func NewEmptyToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]core.Tool), diagnosticWriter: io.Discard}
}

// NewToolRegistryWithPolicyAndSkillRoots creates the default tool
// registry with request-bound skill roots and diagnostics. The roots are
// ordered directories that directly contain skill subdirectories; no path is
// inferred from process state.
func NewToolRegistryWithPolicyAndSkillRoots(
	options RegistryOptions,
	policy *filesystem.FilesystemPolicy,
	skillRoots []string,
	diagnosticWriter io.Writer,
) *ToolRegistry {
	return newToolRegistry(options, display.DisplayCapability{}, nil, false, policy, true, skillRoots, diagnosticWriter)
}

// NewToolRegistryFromConfigWithDisplayCapability creates the session-specific
// registry after display admission has been resolved. Display-dependent tools
// are omitted together, so the definitions and executor routes cannot drift.
// The ordinary constructor above intentionally retains its direct/batch
// behavior for callers that have not opted into session capability admission.
//
// Gating uses capability.Advertisable(), not capability.Usable(): a display
// that is structurally present but not currently capturable (for example,
// macOS Screen Recording permission has not been granted) still advertises
// show/mouse, so the model can invoke them and receive the actionable,
// invocation-time permission-denied envelope. Only a capability that could
// not prove a display exists at all (headless CI) omits them.
func NewToolRegistryWithDisplayCapability(
	options RegistryOptions,
	capability display.DisplayCapability,
	surface display.DisplaySurface,
) *ToolRegistry {
	return newToolRegistry(options, capability, surface, true, nil, false, nil, nil)
}

// NewToolRegistryFromConfigWithDisplayCapabilityAndPolicy is the session
// capability-aware constructor with the same filesystem boundary as direct
// and one-shot tool registries.
func NewToolRegistryWithDisplayCapabilityAndPolicy(
	options RegistryOptions,
	capability display.DisplayCapability,
	surface display.DisplaySurface,
	policy *filesystem.FilesystemPolicy,
) *ToolRegistry {
	return newToolRegistry(options, capability, surface, true, policy, true, nil, nil)
}

// NewToolRegistryFromConfigWithDisplayCapabilityAndPolicyAndSkillRoots is the
// request-scoped constructor used by the public tools service. It keeps the
// host display boundary, filesystem policy, ordered skill roots, and
// diagnostics in one registry snapshot so definitions and execution cannot
// drift between callers.
func NewToolRegistryWithDisplayCapabilityAndPolicyAndSkillRoots(
	options RegistryOptions,
	capability display.DisplayCapability,
	surface display.DisplaySurface,
	policy *filesystem.FilesystemPolicy,
	skillRoots []string,
	diagnosticWriter io.Writer,
) *ToolRegistry {
	return newToolRegistry(options, capability, surface, true, policy, true, skillRoots, diagnosticWriter)
}

func newToolRegistry(
	options RegistryOptions,
	displayCapability display.DisplayCapability,
	displaySurface display.DisplaySurface,
	gateDisplayTools bool,
	policy *filesystem.FilesystemPolicy,
	policyRequired bool,
	skillRoots []string,
	diagnosticWriter io.Writer,
) *ToolRegistry {
	policy = requireFilesystemPolicy(policy, policyRequired)
	if diagnosticWriter == nil {
		diagnosticWriter = io.Discard
	}
	registry := &ToolRegistry{
		tools:            make(map[string]core.Tool),
		diagnosticWriter: diagnosticWriter,
	}
	enabled := func(id string) bool { return SelectionEnabled(options.Selections, id) }
	registerShellTool(registry, options.Exec, enabled, diagnosticWriter)
	registerFilesystemTools(registry, enabled, policy)
	registerWebTools(registry, enabled)
	registerDisplayTools(registry, enabled, gateDisplayTools, displayCapability, displaySurface)
	registerUtilityTools(registry, enabled, skillRoots)
	return registry
}

func requireFilesystemPolicy(policy *filesystem.FilesystemPolicy, required bool) *filesystem.FilesystemPolicy {
	if !required || policy != nil {
		return policy
	}
	// Policy-aware composition is fail-closed even when a caller omits the
	// optional value. Resolve the ordinary default when possible, but never
	// silently switch back to the legacy unrestricted host filesystem.
	resolved, err := filesystem.ResolveFilesystemPolicy("")
	if err == nil {
		policy = resolved
	}
	if policy == nil {
		policy = &filesystem.FilesystemPolicy{}
	}
	return policy
}

// SelectionEnabled reports whether one tool is enabled by the normalized
// request. An empty selection list enables the complete built-in surface.
func SelectionEnabled(selections []public.ToolSelection, id string) bool {
	if len(selections) == 0 {
		return true
	}
	for _, selection := range selections {
		if selection.ID == id {
			return selection.Enabled
		}
	}
	return true
}

func registerShellTool(registry *ToolRegistry, policy public.ExecPolicy, enabled func(string) bool, diagnosticWriter io.Writer) {
	if !enabled("exec") {
		return
	}
	registerTool(registry, shell.NewExecToolWithPolicyAndDiagnosticWriter("", false, policy, diagnosticWriter))
}

func registerFilesystemTools(registry *ToolRegistry, enabled func(string) bool, policy *filesystem.FilesystemPolicy) {
	registerReadTools(registry, enabled, policy)
	registerWriteTools(registry, enabled, policy)
}

func registerReadTools(registry *ToolRegistry, enabled func(string) bool, policy *filesystem.FilesystemPolicy) {
	if enabled("read_file") {
		if policy != nil {
			registerTool(registry, filesystem.NewReadFileToolWithPolicy(policy))
		} else {
			registerTool(registry, filesystem.NewReadFileTool("", false))
		}
	}
	if enabled(filesystem.ReadImageToolID) {
		if policy != nil {
			registerTool(registry, filesystem.NewReadImageToolWithPolicy(policy))
		} else {
			registerTool(registry, filesystem.NewReadImageTool(nil))
		}
	}
}

func registerWriteTools(registry *ToolRegistry, enabled func(string) bool, policy *filesystem.FilesystemPolicy) {
	registerWriteTool(registry, enabled, policy, "write_file",
		func(p *filesystem.FilesystemPolicy) core.Tool { return filesystem.NewWriteFileToolWithPolicy(p) },
		func() core.Tool { return filesystem.NewWriteFileTool("", false) })
	registerWriteTool(registry, enabled, policy, "edit_file",
		func(p *filesystem.FilesystemPolicy) core.Tool { return filesystem.NewEditFileToolWithPolicy(p) },
		func() core.Tool { return filesystem.NewEditFileTool("", false) })
	registerWriteTool(registry, enabled, policy, "append_file",
		func(p *filesystem.FilesystemPolicy) core.Tool { return filesystem.NewAppendFileToolWithPolicy(p) },
		func() core.Tool { return filesystem.NewAppendFileTool("", false) })
	registerWriteTool(registry, enabled, policy, "list_dir",
		func(p *filesystem.FilesystemPolicy) core.Tool { return filesystem.NewListDirToolWithPolicy(p) },
		func() core.Tool { return filesystem.NewListDirTool("", false) })
}

func registerWriteTool(
	registry *ToolRegistry,
	enabled func(string) bool,
	policy *filesystem.FilesystemPolicy,
	id string,
	withPolicy func(*filesystem.FilesystemPolicy) core.Tool,
	withoutPolicy func() core.Tool,
) {
	if !enabled(id) {
		return
	}
	if policy != nil {
		registerTool(registry, withPolicy(policy))
		return
	}
	registerTool(registry, withoutPolicy())
}

func registerWebTools(registry *ToolRegistry, enabled func(string) bool) {
	if enabled("web_fetch") {
		registerTool(registry, web.NewWebFetchTool(0))
	}
	if enabled("web_search") {
		if searchTool := web.NewWebSearchTool(web.WebSearchToolOptions{DuckDuckGoEnabled: true}); searchTool != nil {
			registerTool(registry, searchTool)
		}
	}
}

func registerDisplayTools(
	registry *ToolRegistry,
	enabled func(string) bool,
	gateDisplayTools bool,
	capability display.DisplayCapability,
	surface display.DisplaySurface,
) {
	if gateDisplayTools && !capability.Advertisable() {
		return
	}
	if enabled("show") {
		registerTool(registry, display.NewScreenToolWithDisplaySurface(surface))
	}
	if enabled("mouse") {
		registerTool(registry, mouse.NewMouseTool())
	}
}

func registerUtilityTools(registry *ToolRegistry, enabled func(string) bool, skillRoots []string) {
	if enabled("load_skill") {
		registerTool(registry, misc.NewLoadSkillToolFromRoots(skillRoots...))
	}
	if enabled("sleep") {
		registerTool(registry, misc.NewSleepTool())
	}
}

func registerTool(registry *ToolRegistry, tool core.Tool) {
	if err := registry.Register(tool); err != nil {
		panic(fmt.Errorf("register default tool %q: %w", tool.Name(), err))
	}
}
