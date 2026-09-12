// Package sessioninstructions defines the host-neutral session instruction
// contract. Filesystem access and skill discovery stay behind InstructionLoader;
// the runtime never discovers host state on its own.
package sessioninstructions

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// MaxInstructionBytes bounds every individual instruction document and the
	// final resolved instruction value accepted by the runtime.
	MaxInstructionBytes = 1 << 20
	// MaxSkillSummaryBytes bounds the host-provided summary before it is
	// appended to the selected instructions.
	MaxSkillSummaryBytes = 256 << 10
	// MaxFilesystemScopeDescriptionBytes bounds the normalized scope text
	// supplied by a host.
	MaxFilesystemScopeDescriptionBytes = 64 << 10
	// MaxWorkspacePathBytes bounds the workspace path used to form AGENTS.md.
	MaxWorkspacePathBytes = 4 << 10
	// MaxToolDefinitions bounds the capability snapshot copied for composition.
	MaxToolDefinitions = 4096
	// MaxToolNameBytes bounds identifiers used while selecting page-sight policy.
	MaxToolNameBytes = 256
)

// ErrorKind is a comparable, immutable error identity suitable for errors.Is.
// Constants are used instead of mutable package-level error variables so the
// public contract remains free of shared state.
type ErrorKind string

func (e ErrorKind) Error() string { return string(e) }

const (
	// ErrContextRequired indicates a nil resolution context.
	ErrContextRequired ErrorKind = "instruction resolution context is required"
	// ErrLoaderRequired indicates that a workspace-backed resolution had no
	// host-owned loader capability.
	ErrLoaderRequired ErrorKind = "instruction loader is required"
	// ErrInstructionTooLarge indicates an instruction value exceeded its bound.
	ErrInstructionTooLarge ErrorKind = "instruction value exceeds the configured bound"
	// ErrSkillSummaryTooLarge indicates an oversized skill summary.
	ErrSkillSummaryTooLarge ErrorKind = "skill summary exceeds the configured bound"
	// ErrFilesystemScopeTooLarge indicates an oversized normalized scope.
	ErrFilesystemScopeTooLarge ErrorKind = "filesystem scope exceeds the configured bound"
	// ErrWorkspacePathTooLong indicates an unsafe workspace path length.
	ErrWorkspacePathTooLong ErrorKind = "workspace path exceeds the configured bound"
	// ErrMalformedInstruction indicates invalid UTF-8 or an embedded NUL byte.
	ErrMalformedInstruction ErrorKind = "instruction value is malformed"
)

// ResolutionPhase identifies the operation that produced a resolution error.
type ResolutionPhase string

const (
	PhaseValidation    ResolutionPhase = "validation"
	PhasePromptStat    ResolutionPhase = "prompt-stat"
	PhasePromptRead    ResolutionPhase = "prompt-read"
	PhaseWorkspaceRead ResolutionPhase = "workspace-read"
	PhaseSkillsSummary ResolutionPhase = "skills-summary"
	PhaseScope         ResolutionPhase = "filesystem-scope"
)

// ResolutionError preserves the causal phase and optional path while keeping
// the underlying loader or context error available to errors.Is/errors.As.
type ResolutionError struct {
	Phase ResolutionPhase
	Path  string
	Err   error
}

func (e *ResolutionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Path != "" {
		return fmt.Sprintf("instruction resolution %s %s: %v", e.Phase, e.Path, e.Err)
	}
	return fmt.Sprintf("instruction resolution %s: %v", e.Phase, e.Err)
}

func (e *ResolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InstructionLoader is the host-owned I/O boundary used while resolving a
// session's instruction value. Stat is used only to preserve the explicit
// prompt path-versus-literal compatibility rule: any non-context stat error
// leaves the supplied value as literal text.
type InstructionLoader interface {
	Stat(string) error
	ReadFile(string) ([]byte, error)
	SkillsSummary() (string, error)
}

// ContextAwareInstructionLoader is an optional stronger loader seam. A
// service uses these methods when present and checks cancellation around every
// legacy loader call when only InstructionLoader is implemented.
type ContextAwareInstructionLoader interface {
	StatContext(context.Context, string) error
	ReadFileContext(context.Context, string) ([]byte, error)
	SkillsSummaryContext(context.Context) (string, error)
}

// InstructionRequest contains normalized host values needed for prompt
// selection. WorkspaceDir and FilesystemScopeDescription are values already
// resolved by the host; the runtime never discovers a workspace, config
// directory, environment, or skills root on its own.
type InstructionRequest struct {
	// Prompt accepts the session contract: "none" disables instructions, a
	// value that stats as a file is read, and all other non-empty values remain
	// literal text.
	Prompt string
	// WorkspaceDir is the normalized root used for AGENTS.md lookup and the
	// context in which the host-owned loader builds its skill summary.
	WorkspaceDir string
	// FilesystemScopeDescription is appended only when the host marks the
	// normalized filesystem policy as present and selected instructions are
	// non-empty.
	FilesystemScopeDescription string
	FilesystemScopeSet         bool
	Loader                     InstructionLoader
}

// InstructionResult is the selected, scope-aware instruction text. Tool and
// browser policy is composed separately through InstructionService.Compose.
type InstructionResult struct {
	Instructions string
}

// BrowserCapabilityState is the neutral browser lifecycle state carried into
// instruction composition. Host browser packages do not cross this boundary.
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
	// capability. Empty uses the runtime default, "show_page".
	PageSightToolID string
}

// Service owns prompt selection and model-facing policy composition. Its
// implementation is private and is constructed through the sessioninstructions
// Wire graph.
type Service interface {
	Resolve(context.Context, InstructionRequest) (InstructionResult, error)
	Compose(InstructionComposition) string
}

// InstructionService is the compatibility name for Service.
type InstructionService = Service
