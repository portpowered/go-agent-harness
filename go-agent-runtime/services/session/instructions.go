package session

import sessioninstructions "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"

// The session package keeps these aliases for source compatibility with
// existing hosts. The canonical contract and policy owner live in the public
// sessioninstructions service; this package must not define a second copy.
type InstructionLoader = sessioninstructions.InstructionLoader
type ContextAwareInstructionLoader = sessioninstructions.ContextAwareInstructionLoader
type InstructionRequest = sessioninstructions.InstructionRequest
type InstructionResult = sessioninstructions.InstructionResult
type BrowserCapabilityState = sessioninstructions.BrowserCapabilityState
type InstructionComposition = sessioninstructions.InstructionComposition
type InstructionService = sessioninstructions.Service
type ErrorKind = sessioninstructions.ErrorKind
type ResolutionPhase = sessioninstructions.ResolutionPhase
type ResolutionError = sessioninstructions.ResolutionError

const (
	MaxInstructionBytes                = sessioninstructions.MaxInstructionBytes
	MaxSkillSummaryBytes               = sessioninstructions.MaxSkillSummaryBytes
	MaxFilesystemScopeDescriptionBytes = sessioninstructions.MaxFilesystemScopeDescriptionBytes
	MaxWorkspacePathBytes              = sessioninstructions.MaxWorkspacePathBytes
	MaxToolDefinitions                 = sessioninstructions.MaxToolDefinitions
	MaxToolNameBytes                   = sessioninstructions.MaxToolNameBytes

	ErrContextRequired         = sessioninstructions.ErrContextRequired
	ErrLoaderRequired          = sessioninstructions.ErrLoaderRequired
	ErrInstructionTooLarge     = sessioninstructions.ErrInstructionTooLarge
	ErrSkillSummaryTooLarge    = sessioninstructions.ErrSkillSummaryTooLarge
	ErrFilesystemScopeTooLarge = sessioninstructions.ErrFilesystemScopeTooLarge
	ErrWorkspacePathTooLong    = sessioninstructions.ErrWorkspacePathTooLong
	ErrMalformedInstruction    = sessioninstructions.ErrMalformedInstruction

	PhaseValidation    = sessioninstructions.PhaseValidation
	PhasePromptStat    = sessioninstructions.PhasePromptStat
	PhasePromptRead    = sessioninstructions.PhasePromptRead
	PhaseWorkspaceRead = sessioninstructions.PhaseWorkspaceRead
	PhaseSkillsSummary = sessioninstructions.PhaseSkillsSummary
	PhaseScope         = sessioninstructions.PhaseScope

	BrowserCapabilityConnectedUnselected = sessioninstructions.BrowserCapabilityConnectedUnselected
)
