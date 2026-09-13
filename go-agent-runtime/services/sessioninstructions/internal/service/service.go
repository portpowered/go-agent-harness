package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessioninstructions "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
)

// Contract aliases keep the private implementation signatures tied to the
// host-neutral public contract without exposing construction or policy there.
type (
	Service                = sessioninstructions.Service
	InstructionLoader      = sessioninstructions.InstructionLoader
	InstructionRequest     = sessioninstructions.InstructionRequest
	InstructionResult      = sessioninstructions.InstructionResult
	InstructionComposition = sessioninstructions.InstructionComposition
	BrowserCapabilityState = sessioninstructions.BrowserCapabilityState
	ResolutionPhase        = sessioninstructions.ResolutionPhase
	ResolutionError        = sessioninstructions.ResolutionError
)

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

// Service is stateless. Host-specific I/O enters only through a request-owned
// loader and composition receives a value snapshot.
type instructionService struct{}

func New() sessioninstructions.Service { return &instructionService{} }

var _ Service = (*instructionService)(nil)

// Resolve selects the explicit prompt, workspace AGENTS.md, or none; then it
// appends skills and scope in their established order. Every loader operation
// is attributed to a phase and checked for cancellation.
func (s *instructionService) Resolve(ctx context.Context, request InstructionRequest) (InstructionResult, error) {
	if err := validateRequest(ctx, request); err != nil {
		return InstructionResult{}, err
	}
	instructions, err := resolvePrompt(ctx, request.Prompt, request.WorkspaceDir, request.Loader)
	if err != nil {
		return InstructionResult{}, err
	}
	instructions, err = appendSkillsSummary(ctx, instructions, request.WorkspaceDir, request.Loader)
	if err != nil {
		return InstructionResult{}, err
	}
	instructions, err = appendFilesystemScope(instructions, request)
	if err != nil {
		return InstructionResult{}, err
	}
	return InstructionResult{Instructions: instructions}, nil
}

func appendSkillsSummary(ctx context.Context, instructions, workspaceDir string, loader InstructionLoader) (string, error) {
	if instructions == "" || workspaceDir == "" || loader == nil {
		return instructions, nil
	}
	if err := checkContext(ctx, PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	summary, summaryErr := skillsSummary(ctx, loader)
	if err := checkContext(ctx, PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	if summaryErr != nil {
		return "", resolutionError(PhaseSkillsSummary, workspaceDir, fmt.Errorf("load skills summary: %w", summaryErr))
	}
	if err := checkContext(ctx, PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	if err := validateText(summary, MaxSkillSummaryBytes, ErrSkillSummaryTooLarge); err != nil {
		return "", resolutionError(PhaseSkillsSummary, workspaceDir, err)
	}
	if summary == "" {
		return instructions, nil
	}
	instructions += "\n\n---\n\n" + summary
	if err := validateText(instructions, MaxInstructionBytes, ErrInstructionTooLarge); err != nil {
		return "", resolutionError(PhaseSkillsSummary, workspaceDir, err)
	}
	return instructions, nil
}

func appendFilesystemScope(instructions string, request InstructionRequest) (string, error) {
	if instructions == "" || !request.FilesystemScopeSet {
		return instructions, nil
	}
	scope := "Filesystem scope: " + request.FilesystemScopeDescription + ". Relative filesystem-tool paths resolve from this workdir."
	instructions += "\n\n" + scope
	if err := validateText(instructions, MaxInstructionBytes, ErrInstructionTooLarge); err != nil {
		return "", resolutionError(PhaseScope, "", err)
	}
	return instructions, nil
}

func validateRequest(ctx context.Context, request InstructionRequest) error {
	if ctx == nil {
		return resolutionError(PhaseValidation, "", ErrContextRequired)
	}
	if err := ctx.Err(); err != nil {
		return resolutionError(PhaseValidation, "", err)
	}
	if err := validateText(request.Prompt, MaxInstructionBytes, ErrInstructionTooLarge); err != nil {
		return resolutionError(PhaseValidation, "prompt", err)
	}
	if err := validateText(request.WorkspaceDir, MaxWorkspacePathBytes, ErrWorkspacePathTooLong); err != nil {
		return resolutionError(PhaseValidation, "workspace", err)
	}
	if request.FilesystemScopeSet {
		if err := validateText(request.FilesystemScopeDescription, MaxFilesystemScopeDescriptionBytes, ErrFilesystemScopeTooLarge); err != nil {
			return resolutionError(PhaseValidation, "scope", err)
		}
	}
	if request.Prompt == "" && request.WorkspaceDir != "" && request.Loader == nil {
		return resolutionError(PhaseValidation, request.WorkspaceDir, ErrLoaderRequired)
	}
	return nil
}

func resolvePrompt(ctx context.Context, value, workspaceDir string, loader InstructionLoader) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		return resolveExplicitPrompt(ctx, value, loader)
	}
	return resolveWorkspacePrompt(ctx, workspaceDir, loader)
}

func resolveExplicitPrompt(ctx context.Context, value string, loader InstructionLoader) (string, error) {
	// A nil loader is useful for literal-only embedded callers. It prevents the
	// runtime from reaching into host filesystem state when the host has not
	// opted into prompt-file resolution.
	if loader == nil {
		return validateAndReturn(value, MaxInstructionBytes, ErrInstructionTooLarge, PhaseValidation, "prompt")
	}
	if err := checkContext(ctx, PhasePromptStat, value); err != nil {
		return "", err
	}
	statErr := loaderStat(ctx, loader, value)
	if err := checkContext(ctx, PhasePromptStat, value); err != nil {
		return "", err
	}
	if statErr != nil {
		if errors.Is(statErr, context.Canceled) || errors.Is(statErr, context.DeadlineExceeded) {
			return "", resolutionError(PhasePromptStat, value, statErr)
		}
		// Preserve the established literal fallback for every ordinary stat
		// failure, including a missing explicit path.
		return validateAndReturn(value, MaxInstructionBytes, ErrInstructionTooLarge, PhasePromptStat, value)
	}
	if err := checkContext(ctx, PhasePromptRead, value); err != nil {
		return "", err
	}
	data, readErr := loaderReadFile(ctx, loader, value)
	if err := checkContext(ctx, PhasePromptRead, value); err != nil {
		return "", err
	}
	if readErr != nil {
		return "", resolutionError(PhasePromptRead, value, fmt.Errorf("read system prompt %s: %w", value, readErr))
	}
	if err := checkContext(ctx, PhasePromptRead, value); err != nil {
		return "", err
	}
	text, err := boundedText(data, MaxInstructionBytes, ErrInstructionTooLarge)
	if err != nil {
		return "", resolutionError(PhasePromptRead, value, err)
	}
	return text, nil
}

func resolveWorkspacePrompt(ctx context.Context, workspaceDir string, loader InstructionLoader) (string, error) {
	if workspaceDir == "" {
		return "", nil
	}
	if loader == nil {
		return "", resolutionError(PhaseWorkspaceRead, workspaceDir, ErrLoaderRequired)
	}
	if err := checkContext(ctx, PhaseWorkspaceRead, workspaceDir); err != nil {
		return "", err
	}
	agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
	data, readErr := loaderReadFile(ctx, loader, agentsPath)
	if err := checkContext(ctx, PhaseWorkspaceRead, agentsPath); err != nil {
		return "", err
	}
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return "", resolutionError(PhaseWorkspaceRead, agentsPath, readErr)
		}
		if errors.Is(readErr, fs.ErrNotExist) {
			return "", nil
		}
		return "", resolutionError(PhaseWorkspaceRead, agentsPath, fmt.Errorf("read AGENTS.md %s: %w", agentsPath, readErr))
	}
	if err := checkContext(ctx, PhaseWorkspaceRead, agentsPath); err != nil {
		return "", err
	}
	text, err := boundedText(data, MaxInstructionBytes, ErrInstructionTooLarge)
	if err != nil {
		return "", resolutionError(PhaseWorkspaceRead, agentsPath, err)
	}
	return text, nil
}

func loaderStat(ctx context.Context, loader InstructionLoader, path string) error {
	if contextual, ok := loader.(interface {
		StatContext(context.Context, string) error
	}); ok {
		return contextual.StatContext(ctx, path)
	}
	return loader.Stat(path)
}

func loaderReadFile(ctx context.Context, loader InstructionLoader, path string) ([]byte, error) {
	if contextual, ok := loader.(interface {
		ReadFileContext(context.Context, string) ([]byte, error)
	}); ok {
		return contextual.ReadFileContext(ctx, path)
	}
	return loader.ReadFile(path)
}

func skillsSummary(ctx context.Context, loader InstructionLoader) (string, error) {
	if contextual, ok := loader.(interface {
		SkillsSummaryContext(context.Context) (string, error)
	}); ok {
		return contextual.SkillsSummaryContext(ctx)
	}
	return loader.SkillsSummary()
}

func checkContext(ctx context.Context, phase ResolutionPhase, path string) error {
	if ctx == nil {
		return resolutionError(phase, path, ErrContextRequired)
	}
	if err := ctx.Err(); err != nil {
		return resolutionError(phase, path, err)
	}
	return nil
}

func validateAndReturn(value string, limit int, tooLarge error, phase ResolutionPhase, path string) (string, error) {
	if err := validateText(value, limit, tooLarge); err != nil {
		return "", resolutionError(phase, path, err)
	}
	return value, nil
}

func boundedText(data []byte, limit int, tooLarge error) (string, error) {
	// Converting a copied slice to a string prevents a loader from retaining a
	// mutable backing buffer through the returned instruction value.
	copyData := append([]byte(nil), data...)
	text := string(copyData)
	if err := validateText(text, limit, tooLarge); err != nil {
		return "", err
	}
	return text, nil
}

func validateText(value string, limit int, tooLarge error) error {
	if len(value) > limit {
		return errors.Join(tooLarge, fmt.Errorf("got %d bytes, limit is %d", len(value), limit))
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return ErrMalformedInstruction
	}
	return nil
}

func resolutionError(phase ResolutionPhase, path string, err error) error {
	return &ResolutionError{Phase: phase, Path: path, Err: err}
}

// Compose appends provider-neutral grounding policies in their historical
// order. Empty instructions and no-tool requests are exact identity paths.
func (s *instructionService) Compose(request InstructionComposition) string {
	instructions := request.Instructions
	if instructions == "" {
		return instructions
	}
	definitions := cloneToolDefinitions(request.ToolDefinitions)
	if len(definitions) == 0 {
		return instructions
	}
	blocks := []string{instructions}
	blocks = appendConnectedUnselectedPolicy(blocks, instructions, request.BrowserCapabilityState)
	blocks = appendMissingPolicy(blocks, instructions, toolGroundingPolicy)
	if request.BrowserToolsEnabled {
		blocks = appendBrowserPolicies(blocks, instructions)
		blocks = appendSightPolicy(blocks, instructions, definitions, request.PageSightToolID)
	}
	return joinPolicyBlocks(blocks)
}

func appendConnectedUnselectedPolicy(blocks []string, instructions string, state BrowserCapabilityState) []string {
	if state == BrowserCapabilityConnectedUnselected && !strings.Contains(instructions, connectedUnselectedBrowserGrounding) {
		return append(blocks, connectedUnselectedBrowserGrounding)
	}
	return blocks
}

func appendBrowserPolicies(blocks []string, instructions string) []string {
	blocks = appendMissingPolicy(blocks, instructions, tabSelectionCalibration)
	return appendMissingPolicy(blocks, instructions, webMCPAmbiguityPolicy)
}

func appendSightPolicy(blocks []string, instructions string, definitions []messages.ToolDefinition, pageSightID string) []string {
	if len(pageSightID) == 0 || len(pageSightID) > MaxToolNameBytes || !utf8.ValidString(pageSightID) || strings.IndexByte(pageSightID, 0) >= 0 {
		pageSightID = defaultPageSightToolID
	}
	if hasTool(definitions, pageSightID) {
		policy := strings.ReplaceAll(sightGroundingPolicy, defaultPageSightToolID, pageSightID)
		return appendMissingPolicy(blocks, instructions, policy)
	}
	return blocks
}

func appendMissingPolicy(blocks []string, instructions, policy string) []string {
	if strings.Contains(instructions, policy) {
		return blocks
	}
	return append(blocks, policy)
}

func joinPolicyBlocks(blocks []string) string {
	filtered := blocks[:0]
	for _, block := range blocks {
		if block != "" {
			filtered = append(filtered, block)
		}
	}
	return strings.Join(filtered, "\n\n")
}

func cloneToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	if len(definitions) > MaxToolDefinitions {
		definitions = definitions[:MaxToolDefinitions]
	}
	cloned := make([]messages.ToolDefinition, len(definitions))
	for index, definition := range definitions {
		cloned[index] = definition
		cloned[index].Parameters = append([]messages.ToolParameter(nil), definition.Parameters...)
		cloned[index].ParameterSchema = append(json.RawMessage(nil), definition.ParameterSchema...)
	}
	return cloned
}

func hasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if len(definition.Name) <= MaxToolNameBytes && definition.Name == name {
			return true
		}
	}
	return false
}
