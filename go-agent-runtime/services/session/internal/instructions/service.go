// Package instructions owns session prompt selection and model-facing policy
// composition behind the public session contract.
package instructions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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

// Service is intentionally stateless. All host-specific I/O enters through a
// request-owned loader and every composition input is a value.
type Service struct{}

func New() *Service { return &Service{} }

var _ session.InstructionService = (*Service)(nil)

// Resolve selects the prompt, adds the optional host-provided skill summary,
// and appends the normalized filesystem scope. Model-facing tool and browser
// policy is deliberately composed by Compose so direct planner callers and
// resolved host requests share one composition boundary.
func (s *Service) Resolve(ctx context.Context, request session.InstructionRequest) (session.InstructionResult, error) {
	if ctx == nil {
		return session.InstructionResult{}, errors.New("instruction resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return session.InstructionResult{}, err
	}
	instructions, err := resolvePrompt(ctx, request.Prompt, request.WorkspaceDir, request.Loader)
	if err != nil {
		return session.InstructionResult{}, err
	}
	if instructions != "" && request.WorkspaceDir != "" && request.Loader != nil {
		if err := ctx.Err(); err != nil {
			return session.InstructionResult{}, err
		}
		if summary, summaryErr := request.Loader.SkillsSummary(); summaryErr == nil && summary != "" {
			instructions += "\n\n---\n\n" + summary
		}
	}
	if instructions != "" && request.FilesystemScopeSet {
		scope := "Filesystem scope: " + request.FilesystemScopeDescription + ". Relative filesystem-tool paths resolve from this workdir."
		instructions += "\n\n" + scope
	}
	return session.InstructionResult{Instructions: instructions}, nil
}

func resolvePrompt(ctx context.Context, value, workspaceDir string, loader session.InstructionLoader) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		return resolveExplicitPrompt(ctx, value, loader)
	}
	return resolveWorkspacePrompt(ctx, workspaceDir, loader)
}

func resolveExplicitPrompt(ctx context.Context, value string, loader session.InstructionLoader) (string, error) {
	// A nil loader is useful for literal-only embedded callers. It also
	// prevents the reusable runtime from reaching into host filesystem
	// state when a host has not opted into prompt-file resolution.
	if loader == nil {
		return value, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := loader.Stat(value); err == nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		data, err := loader.ReadFile(value)
		if err != nil {
			return "", fmt.Errorf("read system prompt %s: %w", value, err)
		}
		return string(data), nil
	}
	return value, nil
}

func resolveWorkspacePrompt(ctx context.Context, workspaceDir string, loader session.InstructionLoader) (string, error) {
	if loader == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
	data, err := loader.ReadFile(agentsPath)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read AGENTS.md %s: %w", agentsPath, err)
}

// Compose appends the provider-neutral grounding policies in their historical
// order. The no-tools and empty-instruction paths are deliberately exact
// identity operations.
func (s *Service) Compose(request session.InstructionComposition) string {
	instructions := request.Instructions
	if instructions == "" {
		return instructions
	}
	if len(request.ToolDefinitions) == 0 {
		return instructions
	}
	blocks := []string{instructions}
	blocks = appendConnectedUnselectedPolicy(blocks, instructions, request.BrowserCapabilityState)
	blocks = appendMissingPolicy(blocks, instructions, toolGroundingPolicy)
	if request.BrowserToolsEnabled {
		blocks = appendBrowserPolicies(blocks, instructions)
		blocks = appendSightPolicy(blocks, instructions, request.ToolDefinitions, request.PageSightToolID)
	}
	return joinPolicyBlocks(blocks)
}

func appendConnectedUnselectedPolicy(blocks []string, instructions string, state session.BrowserCapabilityState) []string {
	if state == session.BrowserCapabilityConnectedUnselected && !strings.Contains(instructions, connectedUnselectedBrowserGrounding) {
		return append(blocks, connectedUnselectedBrowserGrounding)
	}
	return blocks
}

func appendBrowserPolicies(blocks []string, instructions string) []string {
	blocks = appendMissingPolicy(blocks, instructions, tabSelectionCalibration)
	return appendMissingPolicy(blocks, instructions, webMCPAmbiguityPolicy)
}

func appendSightPolicy(blocks []string, instructions string, definitions []messages.ToolDefinition, pageSightID string) []string {
	if pageSightID == "" {
		pageSightID = defaultPageSightToolID
	}
	if hasTool(definitions, pageSightID) {
		return appendMissingPolicy(blocks, instructions, sightGroundingPolicy)
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

func hasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}
