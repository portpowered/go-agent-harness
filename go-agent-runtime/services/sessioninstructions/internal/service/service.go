// Package service contains the private session instruction policy
// implementation. It has no host imports and receives all state through the
// public sessioninstructions contract.
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
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
)

const (
	defaultPageSightToolID = "show_page"

	toolGroundingPolicy = `Tool-grounding requirements:
- For requests about actual files, commands, web resources, images, or other machine state, use the relevant advertised tool before making factual claims about what exists, happened, or was observed. Use only tools advertised in this session; if no relevant advertised tool exists, say that you cannot inspect the real state instead of guessing.
- Do not claim that an action ran or that state was observed without its corresponding tool result. Wait for the result and base the response on its returned facts.
- Report tool errors, missing resources, permission denials, and non-zero command exits as failures. Never invent output, turn a failure into apparent success, or present memory or assumptions as observations.
- A filesystem refusal envelope means the requested operation was refused and not performed. Explain that it was refused and not performed, preserve the reported operation, path, workdir, reason, and remediation, and never describe it as a successful read or mutation.
- Mention --allow-path as a remedy only for outside-permitted-roots refusals involving a non-sensitive location; protected or sensitive reads cannot be authorized by widening the allowlist.`

	sightGroundingPolicy = `Sight routing requirements:
- For questions about the rendered visual appearance of the selected browser page, use show_page. Its result is the authoritative page sight for broad visual requests and literal visual follow-up questions.
- When a discovered page tool returns structured state that directly answers the request, use that state as authoritative and do not call show_page merely to restate, verify, or translate it. For example, a board-state tool is authoritative for positions, pieces, alignment, and solved status; show_page is still required if the customer asks how the board visually looks on screen.
- Never use host-display sight as a fallback for a browser-page request. If page sight is unavailable or fails, report that page sight is unavailable.
- Use show_screen only when the customer explicitly asks about the computer's physical display; it is a separate capability and does not answer browser-page questions.`

	connectedUnselectedBrowserGrounding = `WebMCP browser selection:
- A browser endpoint is connected, but no page is selected.
- Before any page work, call webmcp_list_tabs.
- If multiple eligible tabs are returned, first match them against the current requested page by title, paraphrase, purpose, or category; only ask the customer which page to use when multiple tabs still match that current step. The mere presence of unrelated eligible tabs is not ambiguity.
- If the customer explicitly requests work on multiple pages in an order, the first unfinished page is the current requested page. Select and finish it, then list and select the next named page; never ask which page comes first when the customer already supplied the order.
- After the customer chooses, call webmcp_select_tab with the exact browser_id and target_id returned by webmcp_list_tabs.
- Until exact selection succeeds, do not invoke page tools, say that browser access is unavailable, or suggest uploads, links, manual page descriptions, shell commands, or other workarounds.`

	webMCPAmbiguityPolicy = `WebMCP ambiguity recovery:
- A failed WebMCP result with error.code "ambiguous_browser" or error.code "ambiguous_tab" and details.recovery.action "ask_customer" is a pending customer choice, not permission to retry the same call.
- Ask exactly one concise spoken/text question before any additional browser tool call. For ambiguous_tab, name every candidate in details.candidate_choices with its safe title and origin; if a label is unavailable, name its exact candidate ID. For ambiguous_browser, name every exact ID in details.candidate_browser_ids. Do not claim that a page was selected.
- Until the customer answers, do not repeat webmcp_get_context, webmcp_list_tabs, or webmcp_select_tab, and do not invoke a page tool. Never retry with an omitted, unchanged, title-based, URL-based, or inferred selector, and never request multiple continuations for the same ambiguity result.
- After the customer answers, map the answer to one advertised exact candidate ID. For a page selection, pass the exact browser_id and target_id from that candidate once; for a browser selection, pass its exact browser_id once. Do not substitute by list order or act on an unchosen page.`

	tabSelectionCalibration = `WebMCP tab selection calibration:
- Distinguish tab selection from navigation. When the customer asks to switch to or select an already-open tab or page, call webmcp_list_tabs (or use the most recently returned tab catalog) before answering. When the customer asks to open a new tab, call webmcp_open_tab. When the customer asks to change, redirect, or navigate the currently selected tab to an absolute website URL, call webmcp_navigate_tab directly; do not open or select another tab first. This preserves the selected target and any active cast of that target.
- A customer request to switch or select an already-open browser tab or page is real page work, even when a page is already selected. Selection ambiguity is never a reason to say that switching tabs, or browsing generally, is unavailable -- either resolve the one clear match or ask the customer; do not deny the capability.
- Treat a listed tab as an eligible match for the request when it matches by exact title, by an obvious paraphrase of that title, or by the page's stated purpose or category (for example, a request for "the document editor" matches a writing app and not a game). Do not require the customer's wording to be a literal, word-for-word match of the tab's title.
- For an explicit ordered request spanning multiple pages, resolve only the first unfinished page when deciding the current match. Complete that page, then list and select the next named page in the customer's order; do not treat the ordered set itself as selection ambiguity.
- Exactly one eligible tab: call webmcp_select_tab with its exact browser_id and target_id immediately. Do not ask a clarifying or confirmation question first, and do not list eligible tabs the customer did not ask about. Confirm only after the switch succeeds, for example "Okay, you're on <title> now."
- Two or more tabs matching the same current step: ask exactly one concise question naming every matching candidate by its title before calling webmcp_select_tab. Do not guess and do not select by list order.`
)

// Service is stateless. Host-specific I/O enters only through a request-owned
// loader and composition receives a value snapshot.
type Service struct{}

func New() *Service { return &Service{} }

var _ sessioninstructions.InstructionService = (*Service)(nil)

// Resolve selects the explicit prompt, workspace AGENTS.md, or none; then it
// appends skills and scope in their established order. Every loader operation
// is attributed to a phase and checked for cancellation.
func (s *Service) Resolve(ctx context.Context, request sessioninstructions.InstructionRequest) (sessioninstructions.InstructionResult, error) {
	if err := validateRequest(ctx, request); err != nil {
		return sessioninstructions.InstructionResult{}, err
	}
	instructions, err := resolvePrompt(ctx, request.Prompt, request.WorkspaceDir, request.Loader)
	if err != nil {
		return sessioninstructions.InstructionResult{}, err
	}
	instructions, err = appendSkillsSummary(ctx, instructions, request.WorkspaceDir, request.Loader)
	if err != nil {
		return sessioninstructions.InstructionResult{}, err
	}
	instructions, err = appendFilesystemScope(instructions, request)
	if err != nil {
		return sessioninstructions.InstructionResult{}, err
	}
	return sessioninstructions.InstructionResult{Instructions: instructions}, nil
}

func appendSkillsSummary(ctx context.Context, instructions, workspaceDir string, loader sessioninstructions.InstructionLoader) (string, error) {
	if instructions == "" || workspaceDir == "" || loader == nil {
		return instructions, nil
	}
	if err := checkContext(ctx, sessioninstructions.PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	summary, summaryErr := skillsSummary(ctx, loader)
	if err := checkContext(ctx, sessioninstructions.PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	if summaryErr != nil {
		return "", resolutionError(sessioninstructions.PhaseSkillsSummary, workspaceDir, fmt.Errorf("load skills summary: %w", summaryErr))
	}
	if err := checkContext(ctx, sessioninstructions.PhaseSkillsSummary, workspaceDir); err != nil {
		return "", err
	}
	if err := validateText(summary, sessioninstructions.MaxSkillSummaryBytes, sessioninstructions.ErrSkillSummaryTooLarge); err != nil {
		return "", resolutionError(sessioninstructions.PhaseSkillsSummary, workspaceDir, err)
	}
	if summary == "" {
		return instructions, nil
	}
	instructions += "\n\n---\n\n" + summary
	if err := validateText(instructions, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge); err != nil {
		return "", resolutionError(sessioninstructions.PhaseSkillsSummary, workspaceDir, err)
	}
	return instructions, nil
}

func appendFilesystemScope(instructions string, request sessioninstructions.InstructionRequest) (string, error) {
	if instructions == "" || !request.FilesystemScopeSet {
		return instructions, nil
	}
	scope := "Filesystem scope: " + request.FilesystemScopeDescription + ". Relative filesystem-tool paths resolve from this workdir."
	instructions += "\n\n" + scope
	if err := validateText(instructions, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge); err != nil {
		return "", resolutionError(sessioninstructions.PhaseScope, "", err)
	}
	return instructions, nil
}

func validateRequest(ctx context.Context, request sessioninstructions.InstructionRequest) error {
	if ctx == nil {
		return resolutionError(sessioninstructions.PhaseValidation, "", sessioninstructions.ErrContextRequired)
	}
	if err := ctx.Err(); err != nil {
		return resolutionError(sessioninstructions.PhaseValidation, "", err)
	}
	if err := validateText(request.Prompt, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge); err != nil {
		return resolutionError(sessioninstructions.PhaseValidation, "prompt", err)
	}
	if err := validateText(request.WorkspaceDir, sessioninstructions.MaxWorkspacePathBytes, sessioninstructions.ErrWorkspacePathTooLong); err != nil {
		return resolutionError(sessioninstructions.PhaseValidation, "workspace", err)
	}
	if request.FilesystemScopeSet {
		if err := validateText(request.FilesystemScopeDescription, sessioninstructions.MaxFilesystemScopeDescriptionBytes, sessioninstructions.ErrFilesystemScopeTooLarge); err != nil {
			return resolutionError(sessioninstructions.PhaseValidation, "scope", err)
		}
	}
	if request.Prompt == "" && request.WorkspaceDir != "" && request.Loader == nil {
		return resolutionError(sessioninstructions.PhaseValidation, request.WorkspaceDir, sessioninstructions.ErrLoaderRequired)
	}
	return nil
}

func resolvePrompt(ctx context.Context, value, workspaceDir string, loader sessioninstructions.InstructionLoader) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		return resolveExplicitPrompt(ctx, value, loader)
	}
	return resolveWorkspacePrompt(ctx, workspaceDir, loader)
}

func resolveExplicitPrompt(ctx context.Context, value string, loader sessioninstructions.InstructionLoader) (string, error) {
	// A nil loader is useful for literal-only embedded callers. It prevents the
	// runtime from reaching into host filesystem state when the host has not
	// opted into prompt-file resolution.
	if loader == nil {
		return validateAndReturn(value, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge, sessioninstructions.PhaseValidation, "prompt")
	}
	if err := checkContext(ctx, sessioninstructions.PhasePromptStat, value); err != nil {
		return "", err
	}
	statErr := loaderStat(ctx, loader, value)
	if err := checkContext(ctx, sessioninstructions.PhasePromptStat, value); err != nil {
		return "", err
	}
	if statErr != nil {
		if errors.Is(statErr, context.Canceled) || errors.Is(statErr, context.DeadlineExceeded) {
			return "", resolutionError(sessioninstructions.PhasePromptStat, value, statErr)
		}
		// Preserve the established literal fallback for every ordinary stat
		// failure, including a missing explicit path.
		return validateAndReturn(value, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge, sessioninstructions.PhasePromptStat, value)
	}
	if err := checkContext(ctx, sessioninstructions.PhasePromptRead, value); err != nil {
		return "", err
	}
	data, readErr := loaderReadFile(ctx, loader, value)
	if err := checkContext(ctx, sessioninstructions.PhasePromptRead, value); err != nil {
		return "", err
	}
	if readErr != nil {
		return "", resolutionError(sessioninstructions.PhasePromptRead, value, fmt.Errorf("read system prompt %s: %w", value, readErr))
	}
	if err := checkContext(ctx, sessioninstructions.PhasePromptRead, value); err != nil {
		return "", err
	}
	text, err := boundedText(data, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge)
	if err != nil {
		return "", resolutionError(sessioninstructions.PhasePromptRead, value, err)
	}
	return text, nil
}

func resolveWorkspacePrompt(ctx context.Context, workspaceDir string, loader sessioninstructions.InstructionLoader) (string, error) {
	if workspaceDir == "" {
		return "", nil
	}
	if loader == nil {
		return "", resolutionError(sessioninstructions.PhaseWorkspaceRead, workspaceDir, sessioninstructions.ErrLoaderRequired)
	}
	if err := checkContext(ctx, sessioninstructions.PhaseWorkspaceRead, workspaceDir); err != nil {
		return "", err
	}
	agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
	data, readErr := loaderReadFile(ctx, loader, agentsPath)
	if err := checkContext(ctx, sessioninstructions.PhaseWorkspaceRead, agentsPath); err != nil {
		return "", err
	}
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return "", resolutionError(sessioninstructions.PhaseWorkspaceRead, agentsPath, readErr)
		}
		if errors.Is(readErr, fs.ErrNotExist) {
			return "", nil
		}
		return "", resolutionError(sessioninstructions.PhaseWorkspaceRead, agentsPath, fmt.Errorf("read AGENTS.md %s: %w", agentsPath, readErr))
	}
	if err := checkContext(ctx, sessioninstructions.PhaseWorkspaceRead, agentsPath); err != nil {
		return "", err
	}
	text, err := boundedText(data, sessioninstructions.MaxInstructionBytes, sessioninstructions.ErrInstructionTooLarge)
	if err != nil {
		return "", resolutionError(sessioninstructions.PhaseWorkspaceRead, agentsPath, err)
	}
	return text, nil
}

func loaderStat(ctx context.Context, loader sessioninstructions.InstructionLoader, path string) error {
	if contextual, ok := loader.(interface {
		StatContext(context.Context, string) error
	}); ok {
		return contextual.StatContext(ctx, path)
	}
	return loader.Stat(path)
}

func loaderReadFile(ctx context.Context, loader sessioninstructions.InstructionLoader, path string) ([]byte, error) {
	if contextual, ok := loader.(interface {
		ReadFileContext(context.Context, string) ([]byte, error)
	}); ok {
		return contextual.ReadFileContext(ctx, path)
	}
	return loader.ReadFile(path)
}

func skillsSummary(ctx context.Context, loader sessioninstructions.InstructionLoader) (string, error) {
	if contextual, ok := loader.(interface {
		SkillsSummaryContext(context.Context) (string, error)
	}); ok {
		return contextual.SkillsSummaryContext(ctx)
	}
	return loader.SkillsSummary()
}

func checkContext(ctx context.Context, phase sessioninstructions.ResolutionPhase, path string) error {
	if ctx == nil {
		return resolutionError(phase, path, sessioninstructions.ErrContextRequired)
	}
	if err := ctx.Err(); err != nil {
		return resolutionError(phase, path, err)
	}
	return nil
}

func validateAndReturn(value string, limit int, tooLarge error, phase sessioninstructions.ResolutionPhase, path string) (string, error) {
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
		return sessioninstructions.ErrMalformedInstruction
	}
	return nil
}

func resolutionError(phase sessioninstructions.ResolutionPhase, path string, err error) error {
	return &sessioninstructions.ResolutionError{Phase: phase, Path: path, Err: err}
}

// Compose appends provider-neutral grounding policies in their historical
// order. Empty instructions and no-tool requests are exact identity paths.
func (s *Service) Compose(request sessioninstructions.InstructionComposition) string {
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

func appendConnectedUnselectedPolicy(blocks []string, instructions string, state sessioninstructions.BrowserCapabilityState) []string {
	if state == sessioninstructions.BrowserCapabilityConnectedUnselected && !strings.Contains(instructions, connectedUnselectedBrowserGrounding) {
		return append(blocks, connectedUnselectedBrowserGrounding)
	}
	return blocks
}

func appendBrowserPolicies(blocks []string, instructions string) []string {
	blocks = appendMissingPolicy(blocks, instructions, tabSelectionCalibration)
	return appendMissingPolicy(blocks, instructions, webMCPAmbiguityPolicy)
}

func appendSightPolicy(blocks []string, instructions string, definitions []messages.ToolDefinition, pageSightID string) []string {
	if len(pageSightID) == 0 || len(pageSightID) > sessioninstructions.MaxToolNameBytes || !utf8.ValidString(pageSightID) || strings.IndexByte(pageSightID, 0) >= 0 {
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
	if len(definitions) > sessioninstructions.MaxToolDefinitions {
		definitions = definitions[:sessioninstructions.MaxToolDefinitions]
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
		if len(definition.Name) <= sessioninstructions.MaxToolNameBytes && definition.Name == name {
			return true
		}
	}
	return false
}
