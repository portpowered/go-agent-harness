package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// Session instructions intentionally disable dynamic system information. A
// realtime session's instructions are the configured workspace or explicit
// prompt content, while the provider/session runtime continues to own its
// model configuration.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	// A pure replay has no provider session to configure. Preserve its captured
	// outbound sequence; injected replay sessions remain configurable for tests
	// and caller-owned session seams.
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()

	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}

	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's audio, explicit text-seed, and duration behavior while
// carrying the selected or default workspace instructions into provider
// construction.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioPath, maxDuration, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if audioPath != "" {
		opts.AudioOutputRequested = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}

	if audioPath == "" {
		if seed.Present {
			wirePrompt := nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
			output := &sessionTextOutput{writer: out}
			if maxDuration == 0 {
				if plan.inferencer != nil {
					plan.inferencer = &sessionTextSeedInferencer{
						inner:      plan.inferencer,
						wirePrompt: wirePrompt,
						value:      seed.Value,
					}
				}
				return errors.Join(plan.run(ctx, output), output.errorValue())
			}
			durationCtx, err := prepareSessionDurationArtifacts(ctx)
			if err != nil {
				return err
			}
			admission := newSessionDurationAdmission()
			// The seed substitution wrapper must sit INSIDE the admission
			// boundary: the duration runner connects through
			// admittedInferencer, so any wrapper composed outside it never
			// observes the session and the sentinel prompt would leak onto
			// the live wire.
			var admittedInner messages.SessionInferencer
			if plan.inferencer != nil {
				admittedInner = &sessionTextSeedInferencer{
					inner:      plan.inferencer,
					wirePrompt: wirePrompt,
					value:      seed.Value,
				}
			}
			if admittedInner != nil {
				plan.inferencer = &sessionDurationAdmissionInferencer{
					inner:     admittedInner,
					admission: admission,
					closeDone: make(chan struct{}),
				}
			}
			var admittedInferencer *sessionDurationAdmissionInferencer
			if admitted, ok := plan.inferencer.(*sessionDurationAdmissionInferencer); ok {
				admittedInferencer = admitted
			}
			runErr = runSessionDurationPlanWithAdmission(durationCtx, output, plan, maxDuration, realSessionDurationClock{}, admittedInferencer)
			return errors.Join(runErr, output.errorValue())
		}
		if maxDuration == 0 {
			return plan.run(ctx, out)
		}
		durationCtx, err := prepareSessionDurationArtifacts(ctx)
		if err != nil {
			return err
		}
		return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
	}

	if seed.Present {
		plan.loop.Prompt = nextSessionTextWirePrompt()
	}
	audioOut, err := newSessionAudioOutputForPlan(&plan, audioPath, out, nil)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", audioPath, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, closeErr))
		}
	}()

	sessionOut := out
	if audioPath == "-" {
		sessionOut = io.Discard
	}
	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = plan.loop.Prompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped
		if maxDuration == 0 {
			runErr = plan.run(ctx, sessionOut)
		} else {
			durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
			if durationErr != nil {
				return durationErr
			}
			runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
		}
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, outputErr))
		}
		return runErr
	}
	if maxDuration == 0 {
		return plan.run(ctx, sessionOut)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
}

// resolveSessionInstructions performs the host-side prompt selection for the
// legacy realtime service. The reusable text service has the same policy in
// its CLI resolver; keeping this tiny adapter here prevents the old realtime
// graph from importing the extracted session implementation.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	workDir := opts.WorkDir
	if workDir == "" && opts.FilesystemPolicy == nil {
		// Preserve the direct service API's historical workspace behavior. CLI
		// sessions always supply the launch-captured policy explicitly.
		workDir = opts.ConfigDir
	}
	if workDir != "" && opts.FilesystemPolicy == nil {
		// Validate the host-selected workspace before attempting prompt
		// discovery. A missing workspace is a startup/configuration error, not
		// an empty prompt, and must prevent provider/session admission.
		policy, policyErr := cliTools.ResolveFilesystemPolicy(workDir, opts.AllowPaths...)
		if policyErr != nil {
			return "", fmt.Errorf("resolve filesystem scope: %w", policyErr)
		}
		workDir = policy.PrimaryRoot()
	}
	instructions, err := resolveSessionPromptValue(systemPrompt, workDir)
	if err != nil {
		return "", err
	}
	if instructions != "" && workDir != "" {
		loader := skills.NewLoader(workDir, opts.ConfigDir)
		if summary, summaryErr := loader.BuildSummary(); summaryErr == nil && summary != "" {
			instructions += "\n\n---\n\n" + summary
		}
	}
	if instructions != "" && opts.FilesystemPolicy != nil {
		instructions = appendFilesystemScopeInstructions(instructions, opts.FilesystemPolicy)
	}
	return instructions, nil
}

func resolveSessionPromptValue(value, workDir string) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		if _, err := os.Stat(value); err == nil {
			data, readErr := os.ReadFile(value)
			if readErr != nil {
				return "", fmt.Errorf("read system prompt %s: %w", value, readErr)
			}
			return string(data), nil
		}
		return value, nil
	}
	data, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read AGENTS.md %s: %w", filepath.Join(workDir, "AGENTS.md"), err)
}

func appendFilesystemScopeInstructions(instructions string, policy interface{ ScopeDescription() string }) string {
	scope := "Filesystem scope: " + policy.ScopeDescription() + ". Relative filesystem-tool paths resolve from this workdir."
	if instructions == "" {
		return scope
	}
	return instructions + "\n\n" + scope
}

const sessionToolGroundingPolicy = `Tool-grounding requirements:
- For requests about actual files, commands, web resources, images, or other machine state, use the relevant advertised tool before making factual claims about what exists, happened, or was observed. Use only tools advertised in this session; if no relevant advertised tool exists, say that you cannot inspect the real state instead of guessing.
- Do not claim that an action ran or that state was observed without its corresponding tool result. Wait for the result and base the response on its returned facts.
- Report tool errors, missing resources, permission denials, and non-zero command exits as failures. Never invent output, turn a failure into apparent success, or present memory or assumptions as observations.
- A filesystem refusal envelope means the requested operation was refused and not performed. Explain that it was refused and not performed, preserve the reported operation, path, workdir, reason, and remediation, and never describe it as a successful read or mutation.
- Mention --allow-path as a remedy only for outside-permitted-roots refusals involving a non-sensitive location; protected or sensitive reads cannot be authorized by widening the allowlist.`

const sessionSightGroundingPolicy = `Sight routing requirements:
- For questions about the rendered visual appearance of the selected browser page, use show_page. Its result is the authoritative page sight for broad visual requests and literal visual follow-up questions.
- When a discovered page tool returns structured state that directly answers the request, use that state as authoritative and do not call show_page merely to restate, verify, or translate it. For example, a board-state tool is authoritative for positions, pieces, alignment, and solved status; show_page is still required if the customer asks how the board visually looks on screen.
- Never use host-display sight as a fallback for a browser-page request. If page sight is unavailable or fails, report that page sight is unavailable.
- Use show_screen only when the customer explicitly asks about the computer's physical display; it is a separate capability and does not answer browser-page questions.`

const sessionConnectedUnselectedBrowserGrounding = `WebMCP browser selection:
- A browser endpoint is connected, but no page is selected.
- Before any page work, call webmcp_list_tabs.
- If multiple eligible tabs are returned, first match them against the current requested page by title, paraphrase, purpose, or category; only ask the customer which page to use when multiple tabs still match that current step. The mere presence of unrelated eligible tabs is not ambiguity.
- If the customer explicitly requests work on multiple pages in an order, the first unfinished page is the current requested page. Select and finish it, then list and select the next named page; never ask which page comes first when the customer already supplied the order.
- After the customer chooses, call webmcp_select_tab with the exact browser_id and target_id returned by webmcp_list_tabs.
- Until exact selection succeeds, do not invoke page tools, say that browser access is unavailable, or suggest uploads, links, manual page descriptions, shell commands, or other workarounds.`

const sessionWebMCPAmbiguityPolicy = `WebMCP ambiguity recovery:
- A failed WebMCP result with error.code "ambiguous_browser" or error.code "ambiguous_tab" and details.recovery.action "ask_customer" is a pending customer choice, not permission to retry the same call.
- Ask exactly one concise spoken/text question before any additional browser tool call. For ambiguous_tab, name every candidate in details.candidate_choices with its safe title and origin; if a label is unavailable, name its exact candidate ID. For ambiguous_browser, name every exact ID in details.candidate_browser_ids. Do not claim that a page was selected.
- Until the customer answers, do not repeat webmcp_get_context, webmcp_list_tabs, or webmcp_select_tab, and do not invoke a page tool. Never retry with an omitted, unchanged, title-based, URL-based, or inferred selector, and never request multiple continuations for the same ambiguity result.
- After the customer answers, map the answer to one advertised exact candidate ID. For a page selection, pass the exact browser_id and target_id from that candidate once; for a browser selection, pass its exact browser_id once. Do not substitute by list order or act on an unchosen page.`

// sessionWebMCPTabSelectionCalibration is the proactive counterpart to
// sessionWebMCPAmbiguityPolicy above. The ambiguity-recovery policy only
// takes effect after a WebMCP tool call has already failed with an ambiguous
// result, so it never fires when the model never attempts a tool call in the
// first place -- the model then has nothing telling it a tab switch is
// something it can act on, and it wrongly reports the capability as absent.
// This block is unconditional on browser capability state (unlike
// sessionConnectedUnselectedBrowserGrounding, which only applies before any
// selection has ever succeeded) so a later "switch tabs" request, made after
// a page is already selected, still carries proactive selection guidance.
const sessionWebMCPTabSelectionCalibration = `WebMCP tab selection calibration:
- Distinguish tab selection from navigation. When the customer asks to switch to or select an already-open tab or page, call webmcp_list_tabs (or use the most recently returned tab catalog) before answering. When the customer asks to open a new tab, call webmcp_open_tab. When the customer asks to change, redirect, or navigate the currently selected tab to an absolute website URL, call webmcp_navigate_tab directly; do not open or select another tab first. This preserves the selected target and any active cast of that target.
- A customer request to switch or select an already-open browser tab or page is real page work, even when a page is already selected. Selection ambiguity is never a reason to say that switching tabs, or browsing generally, is unavailable -- either resolve the one clear match or ask the customer; do not deny the capability.
- Treat a listed tab as an eligible match for the request when it matches by exact title, by an obvious paraphrase of that title, or by the page's stated purpose or category (for example, a request for "the document editor" matches a writing app and not a game). Do not require the customer's wording to be a literal, word-for-word match of the tab's title.
- For an explicit ordered request spanning multiple pages, resolve only the first unfinished page when deciding the current match. Complete that page, then list and select the next named page in the customer's order; do not treat the ordered set itself as selection ambiguity.
- Exactly one eligible tab: call webmcp_select_tab with its exact browser_id and target_id immediately. Do not ask a clarifying or confirmation question first, and do not list eligible tabs the customer did not ask about. Confirm only after the switch succeeds, for example "Okay, you're on <title> now."
- Two or more tabs matching the same current step: ask exactly one concise question naming every matching candidate by its title before calling webmcp_select_tab. Do not guess and do not select by list order.`

// composeSessionInstructions preserves the selected customer instructions and
// adds the provider-neutral grounding contract exactly once for tool-enabled
// sessions. Browser-enabled sessions additionally receive the tab-selection
// calibration contract (act immediately on one clear match, ask only when
// genuinely ambiguous, never deny the capability) and the ambiguity recovery
// contract, which makes a retryable WebMCP result a customer-input boundary
// rather than an invitation to repeat a selector-free call. The calibration
// contract is unconditional on browser capability state so it still applies
// once a page has already been selected and the customer asks to switch; the
// recovery contract only ever matters after a tool call has already been
// attempted and failed. The no-tools path remains byte-for-byte unchanged,
// and callers that already supplied any of these policies do not receive a
// duplicate copy.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	if instructions == "" || len(opts.ToolDefinitions) == 0 {
		return instructions
	}
	blocks := []string{instructions}
	if opts.BrowserCapabilityState == webmcp.BrowserCapabilityConnectedUnselected && !strings.Contains(instructions, sessionConnectedUnselectedBrowserGrounding) {
		blocks = append(blocks, sessionConnectedUnselectedBrowserGrounding)
	}
	if !strings.Contains(instructions, sessionToolGroundingPolicy) {
		blocks = append(blocks, sessionToolGroundingPolicy)
	}
	if opts.BrowserToolsEnabled && !strings.Contains(instructions, sessionWebMCPTabSelectionCalibration) {
		blocks = append(blocks, sessionWebMCPTabSelectionCalibration)
	}
	if opts.BrowserToolsEnabled && !strings.Contains(instructions, sessionWebMCPAmbiguityPolicy) {
		blocks = append(blocks, sessionWebMCPAmbiguityPolicy)
	}
	if opts.BrowserToolsEnabled && sessionHasTool(opts.ToolDefinitions, cliTools.PageSightToolID) && !strings.Contains(instructions, sessionSightGroundingPolicy) {
		blocks = append(blocks, sessionSightGroundingPolicy)
	}
	filtered := blocks[:0]
	for _, block := range blocks {
		if block != "" {
			filtered = append(filtered, block)
		}
	}
	return strings.Join(filtered, "\n\n")
}
