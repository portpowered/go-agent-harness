package session

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// InstructionLoader is the host-owned I/O boundary used while resolving a
// session's instruction value. The runtime chooses which operation is needed;
// the host supplies the filesystem and skill-summary implementations.
//
// A loader must not mutate the workspace. Stat is used only to preserve the
// explicit prompt path-versus-literal compatibility rule: any stat error
// leaves the supplied value as literal text.
type InstructionLoader interface {
	Stat(string) error
	ReadFile(string) ([]byte, error)
	SkillsSummary() (string, error)
}

// InstructionRequest contains the normalized host values needed for prompt
// selection. WorkspaceDir and FilesystemScopeDescription are values already
// resolved by the host; the runtime never discovers a workspace, config
// directory, environment, or skills root on its own.
type InstructionRequest struct {
	// Prompt accepts the existing session contract: "none" disables
	// instructions, a value that stats as a file is read, and all other
	// non-empty values remain literal text.
	Prompt string
	// WorkspaceDir is the normalized root used for AGENTS.md lookup and is
	// also the context in which the host-owned loader builds its skill summary.
	WorkspaceDir string
	// FilesystemScopeDescription is appended only when the host marks the
	// normalized filesystem policy as present and the selected instructions are
	// non-empty. The separate presence bit preserves an empty description.
	FilesystemScopeDescription string
	FilesystemScopeSet         bool
	Loader                     InstructionLoader
}

// InstructionResult is the selected, scope-aware instruction text. Tool and
// browser policy is composed separately through InstructionService.Compose so
// callers can preserve existing planner boundaries while sharing one policy
// implementation.
type InstructionResult struct {
	Instructions string
}

// BrowserCapabilityState is the neutral browser lifecycle state carried into
// instruction composition. CLI-specific browser packages do not cross the
// reusable session boundary.
type BrowserCapabilityState string

const (
	// BrowserCapabilityConnectedUnselected means a browser endpoint exists but
	// no page target has been selected for the session.
	BrowserCapabilityConnectedUnselected BrowserCapabilityState = "connected_unselected"
)

// InstructionComposition contains the normalized model-facing capability
// surface. Tool definitions are copied from the host's advertised snapshot;
// the service only inspects their names and never executes a tool.
type InstructionComposition struct {
	Instructions           string
	ToolDefinitions        []messages.ToolDefinition
	BrowserCapabilityState BrowserCapabilityState
	BrowserToolsEnabled    bool
	// PageSightToolID lets a host supply the identifier used by its page-sight
	// capability without importing that host's tool package into the runtime.
	// Empty uses the public runtime default, "show_page".
	PageSightToolID string
}

// InstructionService owns session prompt selection and model-facing policy
// composition. Its implementation is private and is constructed through the
// session service's Wire graph.
type InstructionService interface {
	Resolve(context.Context, InstructionRequest) (InstructionResult, error)
	Compose(InstructionComposition) string
}
