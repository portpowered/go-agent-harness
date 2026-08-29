package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
)

const (
	// WebMCPSelectionFileName is deliberately separate from config.yaml. A
	// selection is ephemeral browser state, not configuration, and the file
	// contains only opaque IDs and redacted origin metadata.
	WebMCPSelectionFileName = "webmcp-selection.json"
	WebMCPSelectionVersion  = 1

	webmcpDirectWatchStatusEnded    = "ended"
	webmcpDirectWatchStatusCanceled = "canceled"
	webmcpDirectWatchStatusOnce     = "one_event"
	webmcpDirectWatchStatusFailed   = "failed"

	webmcpDirectInvocationReceiptVersion  = "webmcp.invoke-receipt.v1"
	webmcpDirectInvocationReceiptMaxBytes = 1024
)

const webmcpWatchHelp = `Watch the selected target's semantic WebMCP stream.

The following activity is target-observable and is emitted when it is caused by
another CLI process or CDP client attached to the same target:

  toolsAdded/toolsRemoved -> catalog_changed
  toolInvoked             -> invocation_created
  toolResponded           -> invocation_terminal

The watcher also reports generation_changed when the target navigation boundary
changes. selected and session_closed are watcher-local lifecycle events, and
broker admission, approval, and cancellation-request history remains
process-local; no cross-process visibility is promised for those classes.

The tools --watch form uses this same observation and output contract. Target
transport or bounded-delivery loss is reported as a failed session_closed event
instead of a normal complete stream.`

// WebMCPDirectBrowser is the safe browser listing shape. Endpoint addresses
// are redacted before they are copied into this result.
type WebMCPDirectBrowser struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Product      string `json:"product"`
	Protocol     string `json:"protocol"`
	Scope        string `json:"scope"`
	Endpoint     string `json:"endpoint,omitempty"`
	HarnessOwned bool   `json:"harness_owned"`
}

type WebMCPDirectBrowsersData struct {
	Browsers []WebMCPDirectBrowser `json:"browsers"`
}

// WebMCPDirectTab contains only bounded display metadata and normalized
// origin information. URL query/fragment data is never returned.
type WebMCPDirectTab struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Type              string `json:"type"`
	Title             string `json:"title"`
	Origin            string `json:"origin"`
	Eligible          bool   `json:"eligible"`
	EligibilityReason string `json:"eligibility_reason,omitempty"`
	Attached          bool   `json:"attached"`
	Selected          bool   `json:"selected"`
	Generation        uint64 `json:"generation,omitempty"`
	ToolCount         *int   `json:"tool_count,omitempty"`
}

type WebMCPDirectTabsData struct {
	Tabs []WebMCPDirectTab `json:"tabs"`
}

type WebMCPDirectContext struct {
	BrowserID         string `json:"browser_id"`
	TargetID          string `json:"target_id"`
	Title             string `json:"title"`
	URL               string `json:"url,omitempty"`
	Origin            string `json:"origin"`
	Generation        uint64 `json:"generation"`
	Connected         bool   `json:"connected"`
	Ready             bool   `json:"ready"`
	CatalogReady      bool   `json:"catalog_ready"`
	CatalogGeneration uint64 `json:"catalog_generation"`
	ToolCount         int    `json:"tool_count"`
}

type WebMCPDirectFrame struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

type WebMCPDirectTool struct {
	Ref         string            `json:"ref"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	InputSchema json.RawMessage   `json:"input_schema,omitempty"`
	Annotations map[string]any    `json:"annotations"`
	Frame       WebMCPDirectFrame `json:"frame"`
	Generation  uint64            `json:"generation"`
}

type WebMCPDirectToolsData struct {
	BrowserID  string             `json:"browser_id"`
	TargetID   string             `json:"target_id"`
	Generation uint64             `json:"generation"`
	Tools      []WebMCPDirectTool `json:"tools"`
}

type WebMCPDirectInvocation struct {
	InvocationID string          `json:"invocation_id"`
	ToolRef      string          `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

type WebMCPDirectCancelData struct {
	InvocationID string `json:"invocation_id"`
	Status       string `json:"status"`
	Phase        string `json:"phase,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
}

type WebMCPDirectEvent struct {
	Version      string `json:"version"`
	Type         string `json:"type"`
	Sequence     uint64 `json:"sequence"`
	BrowserID    string `json:"browser_id,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	ToolRef      string `json:"tool_ref,omitempty"`
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type WebMCPDirectWatchData struct {
	Status string              `json:"status"`
	Events []WebMCPDirectEvent `json:"events"`
}

type webmcpDirectFlags struct {
	browser flags.BrowserFlags
	json    bool

	eligible             bool
	eligibleOnly         bool
	includeZeroToolPages bool
	originContains       string
	activate             bool
	refresh              bool
	watch                bool
	once                 bool
	nameContains         string
	includeSchemas       bool
	frameID              string
	toolRef              string
	inputJSON            string
	reason               string
	invocationID         string
	timeout              time.Duration
	commandTimeout       time.Duration
}

// WebMCPOperationsCommand owns the direct operation constructors and the
// request-scoped factory. Each invocation gets a fresh broker/runtime and
// closes it before the command returns.
type WebMCPOperationsCommand struct {
	globalFlags    *flags.GlobalFlags
	factory        WebMCPDoctorFactory
	SelectionStore WebMCPSelectionStore
}

// WebMCPOperationsFactory is the descriptive name for the injected runtime
// seam used by direct commands. It aliases the doctor seam so one composition
// root can share ownership and fake setup across the complete command group.
type WebMCPOperationsFactory = WebMCPDoctorFactory

// NewWebMCPOperationsCommand constructs the direct operation group.
func NewWebMCPOperationsCommand(globalFlags *flags.GlobalFlags, factories ...WebMCPDoctorFactory) *WebMCPOperationsCommand {
	factory := defaultWebMCPDoctorFactory(globalFlags)
	if len(factories) > 0 && factories[0] != nil {
		factory = factories[0]
	}
	return &WebMCPOperationsCommand{globalFlags: globalFlags, factory: factory}
}

// AddCommands attaches exactly the nine non-doctor direct WebMCP commands.
func (c *WebMCPOperationsCommand) AddCommands(parent *cobra.Command) {
	if c == nil || parent == nil {
		return
	}
	parent.AddCommand(
		c.browsersCommand(),
		c.tabsCommand(),
		c.selectCommand(),
		c.activateCommand(),
		c.contextCommand(),
		c.toolsCommand(),
		c.invokeCommand(),
		c.cancelCommand(),
		c.watchCommand(),
	)
}

func newWebMCPDirectFlags() *webmcpDirectFlags {
	return &webmcpDirectFlags{}
}

func (c *WebMCPOperationsCommand) browsersCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "browsers",
		Short:        "List discovered WebMCP browsers",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirect(cmd, values, "browsers", webmcp.ErrorEndpointNotFound, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				browserID := ""
				if directBrowserFlagChanged(cmd) {
					browserID = values.browser.Browser
				}
				candidates, err := discoverDirectBrowsers(ctx, broker, browser, browserID)
				if err != nil {
					return nil, err
				}
				rows := make([]WebMCPDirectBrowser, 0, len(candidates))
				for _, candidate := range candidates {
					endpoint := doctorEndpointForCandidate(candidate)
					rows = append(rows, WebMCPDirectBrowser{
						ID:           string(candidate.ID),
						Source:       string(candidate.Source),
						Product:      boundedDoctorText(candidate.Product, 160),
						Protocol:     boundedDoctorText(candidate.Protocol, 80),
						Scope:        endpoint.Scope,
						Endpoint:     endpoint.Address,
						HarnessOwned: candidate.HarnessOwned,
					})
				}
				return WebMCPDirectBrowsersData{Browsers: rows}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) tabsCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "tabs",
		Short:        "List browser tabs available for WebMCP",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirect(cmd, values, "tabs", webmcp.ErrorEndpointNotFound, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				browserID := browser.Selection.Browser
				candidates, err := discoverDirectBrowsers(ctx, broker, browser, browserID)
				if err != nil {
					return nil, err
				}
				selected, _ := selectedDirectContext(ctx, broker, false)
				var catalog webmcp.ToolCatalogSnapshot
				catalogKnown := false
				if selected.Key.BrowserID != "" && selected.Key.TargetID != "" {
					catalog, err = broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
					catalogKnown = err == nil
				}
				rows := make([]WebMCPDirectTab, 0)
				for _, candidate := range candidates {
					targets, listErr := broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
					if listErr != nil {
						return nil, listErr
					}
					for _, target := range targets {
						if target.BrowserID == "" {
							target.BrowserID = candidate.ID
						}
						if values.originContains != "" && !strings.Contains(safeOrigin(target.Origin), values.originContains) {
							continue
						}
						if values.eligible || values.eligibleOnly {
							if !target.Eligible {
								continue
							}
						}
						row := directTabFromTarget(target)
						if selected.Key.BrowserID == target.BrowserID && selected.Key.TargetID == target.ID {
							row.Selected = true
							row.Attached = selected.Connected
							row.Generation = selected.Generation
							if catalogKnown {
								count := len(catalog.Tools)
								row.ToolCount = &count
							}
						}
						rows = append(rows, row)
					}
				}
				sort.SliceStable(rows, func(i, j int) bool {
					if rows[i].BrowserID != rows[j].BrowserID {
						return rows[i].BrowserID < rows[j].BrowserID
					}
					return rows[i].TargetID < rows[j].TargetID
				})
				return WebMCPDirectTabsData{Tabs: rows}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.eligible, "eligible", false, "List only targets eligible for WebMCP")
	cmd.Flags().BoolVar(&values.eligibleOnly, "eligible-only", false, "List only targets eligible for WebMCP")
	cmd.Flags().BoolVar(&values.includeZeroToolPages, "include-zero-tool-pages", false, "Include eligible pages with no known tools")
	cmd.Flags().StringVar(&values.originContains, "origin-contains", "", "Filter by an origin substring")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) selectCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "select",
		Short:        "Select an exact browser target for WebMCP",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirect(cmd, values, "select", webmcp.ErrorTargetAttachFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				return c.selectDirectTarget(ctx, cmd, values, broker, browser)
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.activate, "activate", false, "Activate the selected tab after attaching")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) activateCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "activate",
		Short:        "Activate an exact browser target",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirect(cmd, values, "activate", webmcp.ErrorTargetAttachFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				candidate, target, _, err := c.resolveDirectTarget(ctx, cmd, values, broker, browser)
				if err != nil {
					return nil, err
				}
				selector := webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}
				if activator, ok := broker.(interface {
					Activate(context.Context, webmcp.TargetSelector) error
				}); ok {
					if err := activator.Activate(ctx, selector); err != nil {
						return nil, err
					}
					return directContextData(webmcp.PageContext{Key: webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}, Title: target.Title, URL: target.URL, Origin: target.Origin, Connected: target.Attached}), nil
				}
				selectorWithOptions, ok := broker.(interface {
					SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
				})
				if !ok {
					return nil, webmcpRuntimeUnavailableError("target_activation")
				}
				page, err := selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: true})
				if err != nil {
					return nil, err
				}
				return directContextData(page), nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) contextCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "context",
		Short:        "Show the selected WebMCP page context",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirect(cmd, values, "context", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				page, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser)
				if err != nil {
					return nil, err
				}
				return c.contextWithCatalog(ctx, broker, page, values.refresh)
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.refresh, "refresh", false, "Refresh browser and catalog metadata")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) toolsCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "tools",
		Short:        "List tools exposed by the selected WebMCP page",
		Long:         "List tools exposed by the selected WebMCP page. With --watch, observe the target-scoped semantic stream across independent CLI or CDP clients.\n\n" + webmcpWatchHelp,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if values.watch {
				return c.executeDirect(cmd, values, "watch", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
					watchCtx := ctx
					if values.timeout > 0 {
						var cancel context.CancelFunc
						watchCtx, cancel = context.WithTimeout(ctx, values.timeout)
						defer cancel()
					}
					// Subscribe before selection so tools --watch observes the same
					// selected and initial catalog events as webmcp watch.
					stream := broker.Watch(watchCtx)
					if _, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser); err != nil {
						return nil, err
					}
					return runDirectWatchStream(watchCtx, stream, values.once)
				})
			}
			return c.executeDirect(cmd, values, "tools", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				page, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser)
				if err != nil {
					return nil, err
				}
				snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{
					Refresh:        values.refresh,
					NameContains:   values.nameContains,
					IncludeSchemas: values.includeSchemas,
					FrameID:        webmcp.FrameID(values.frameID),
				})
				if err != nil {
					return nil, err
				}
				return directToolsData(page, snapshot, values.includeSchemas), nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().BoolVar(&values.refresh, "refresh", false, "Refresh the selected page catalog")
	cmd.Flags().StringVar(&values.nameContains, "name-contains", "", "Filter tools by a name substring")
	cmd.Flags().StringVar(&values.frameID, "frame-id", "", "Filter tools to one frame identifier")
	cmd.Flags().BoolVar(&values.includeSchemas, "include-schemas", true, "Include complete page input schemas")
	cmd.Flags().BoolVar(&values.watch, "watch", false, "Watch catalog and invocation events instead of listing once")
	cmd.Flags().BoolVar(&values.once, "once", false, "Stop watch mode after the first event")
	cmd.Flags().DurationVar(&values.timeout, "timeout", 0, "Bound watch duration (Go duration)")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) invokeCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:   "invoke [tool-name] [key=value...]",
		Short: "Invoke a selected page tool by ref or unique name",
		Long: fmt.Sprintf(`Invoke a selected page tool by exact reference or unique name.

After the browser accepts the invocation, this command writes one bounded,
machine-readable dispatch receipt to stderr before waiting for the terminal
result. The receipt has only version, invocation_id, tool_ref, and state, for
example:
  {"version":"webmcp.invoke-receipt.v1","invocation_id":"...","tool_ref":"...","state":"dispatched"}

The receipt is copyable in human mode and is the documented machine-readable
handoff in --json mode. Stdout remains one final command result. To cancel
from another process, keep the exact browser selection persisted and pass the
receipt invocation_id to agent webmcp cancel --invocation <id> --json.
The first SIGINT during a dispatched wait requests cancellation with an
independent reconciliation context and emits the final cancellation result
within %v; cancellation does not claim rollback or safe retry.`, webmcpDirectInterruptReconciliationTimeout),
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			invokeCtx, interrupted, stopInterrupt := newWebMCPDirectInterruptContext(cmd.Context())
			defer stopInterrupt()
			return c.executeDirectWithParentContext(cmd, invokeCtx, values, "invoke", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				selected, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser)
				if err != nil {
					if interrupted() {
						return nil, directInvocationCanceledBeforeDispatch("")
					}
					return nil, err
				}
				toolRef, input, err := resolveDirectInvocation(args, values, broker, ctx)
				if err != nil {
					if interrupted() {
						return nil, directInvocationCanceledBeforeDispatch("")
					}
					return nil, err
				}
				invokeCtx := ctx
				if values.timeout > 0 {
					var cancel context.CancelFunc
					invokeCtx, cancel = context.WithTimeout(ctx, values.timeout)
					defer cancel()
				}
				result, err := broker.Invoke(invokeCtx, webmcp.InvokeRequest{
					ToolRef: toolRef,
					Input:   input,
					Reason:  values.reason,
				})
				if err != nil {
					if interrupted() {
						return nil, directInvocationCanceledBeforeDispatch(toolRef)
					}
					return nil, err
				}
				receiptID, err := writeWebMCPDirectInvocationReceipt(cmd.ErrOrStderr(), result, toolRef)
				if err != nil {
					return nil, err
				}
				if interrupted() {
					return nil, reconcileDirectInvocationInterrupt(broker, result, selected.Key, receiptID, toolRef)
				}
				dispatchedResult := result
				result, err = waitDirectInvocation(invokeCtx, broker, result)
				if err != nil {
					if interrupted() {
						return nil, reconcileDirectInvocationInterrupt(broker, dispatchedResult, selected.Key, receiptID, toolRef)
					}
					return nil, err
				}
				if result.ErrorCode != "" || directInvocationFailed(result.State) {
					return nil, directInvocationResultError(result, toolRef)
				}
				output := result.Output
				if len(bytes.TrimSpace(output)) == 0 {
					output = json.RawMessage("null")
				}
				status := string(result.State)
				if status == "" {
					status = string(webmcp.InvocationDispatched)
				}
				return WebMCPDirectInvocation{InvocationID: string(receiptID), ToolRef: string(toolRef), Status: status, Output: append(json.RawMessage(nil), output...)}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().StringVar(&values.toolRef, "tool-ref", "", "Exact generation-bound WebMCP tool reference")
	cmd.Flags().StringVar(&values.inputJSON, "input-json", "", "JSON object passed to the page tool")
	cmd.Flags().StringVar(&values.reason, "reason", "direct CLI invocation", "User-facing reason for the page action")
	cmd.Flags().DurationVar(&values.timeout, "timeout", 0, "Bound invocation duration (Go duration)")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) cancelCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:   "cancel [invocation-id]",
		Short: "Cancel a pending WebMCP invocation",
		Long: `Cancel a pending browser invocation using the exact ID from the
invoke dispatch receipt. A fresh process rehydrates only the exact persisted
browser and target selection (or an explicitly supplied --browser and --tab)
and asks that target to cancel the supplied ID; it never searches for or
falls back to another target.

Two-process flow:
  agent webmcp select --browser <browser-id> --tab <target-id> --persist-selection --json
  agent webmcp invoke --tool-ref <tool-ref> --input-json '{}' --json
  agent webmcp cancel --invocation <receipt-invocation-id> --json

The receipt is on stderr and the one final cancel result is on stdout. The
result is successful only after the exact target reports terminal Canceled.
Completed or Error is a non-retryable completed-anyway result. A missing
terminal is a non-retryable cancellation_unconfirmed result with
side_effect_unknown; lifecycle loss retains its distinct classification. A
declarative autosubmit page may therefore remain uncertain after dispatch;
cancel_requested is never terminal confirmation.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.executeDirect(cmd, values, "cancel", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				invocationID := values.invocationID
				if invocationID == "" && len(args) == 1 {
					invocationID = args[0]
				}
				if invocationID == "" {
					return nil, directInvalidInputError("an invocation ID is required (use --invocation or a positional ID)", "/invocation_id")
				}
				if values.invocationID != "" && len(args) == 1 {
					return nil, directInvalidInputError("invocation ID must be supplied by --invocation or positionally, not both", "/invocation_id")
				}
				candidate, target, stored, err := c.resolveDirectTarget(ctx, cmd, values, broker, browser)
				if err != nil {
					return nil, err
				}
				if !directCancelHasExactTarget(cmd, browser, stored) {
					return nil, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "direct cancellation requires an exact persisted or explicitly selected browser target", map[string]any{
						"browser_id": browser.Selection.Browser,
						"target_id":  browser.Selection.Tab,
						"reason":     "exact_selection_required",
					})
				}
				selector := webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}
				if _, err := selectDirectTarget(ctx, broker, selector, false); err != nil {
					return nil, err
				}
				cancelCtx := ctx
				var cancelContext context.CancelFunc
				if values.timeout > 0 {
					cancelCtx, cancelContext = context.WithTimeout(ctx, values.timeout)
					defer cancelContext()
				}
				request := webmcp.CancelRequest{InvocationID: webmcp.InvocationID(invocationID), Reason: values.reason}
				if directCanceller, ok := broker.(webmcp.DirectCanceller); ok {
					err = directCanceller.CancelDirect(cancelCtx, webmcp.DirectCancelRequest{
						Target:       selector,
						InvocationID: request.InvocationID,
						Reason:       request.Reason,
					})
				} else {
					err = broker.Cancel(cancelCtx, request)
				}
				if err != nil {
					return nil, err
				}
				return WebMCPDirectCancelData{
					InvocationID: invocationID,
					Status:       "canceled",
					Phase:        "terminal",
					Outcome:      "confirmed_canceled",
				}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().StringVar(&values.invocationID, "invocation", "", "Exact invocation ID")
	cmd.Flags().StringVar(&values.reason, "reason", "direct CLI cancellation", "User-facing cancellation reason")
	cmd.Flags().DurationVar(&values.timeout, "timeout", 0, "Bound cancellation reconciliation duration (Go duration)")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) watchCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "watch",
		Short:        "Watch WebMCP broker events",
		Long:         webmcpWatchHelp,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirectWithContext(cmd, values, "watch", webmcp.ErrorEndpointUnreachable, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				watchCtx := ctx
				var cancelWatch context.CancelFunc
				if values.timeout > 0 {
					watchCtx, cancelWatch = context.WithTimeout(ctx, values.timeout)
					defer cancelWatch()
				}
				stream := broker.Watch(watchCtx)
				// The target session must outlive the bounded watch stream. Passing
				// watchCtx to selection would make it the chromedp target-context
				// parent; when the watch deadline fires, the target would detach
				// before broker cleanup could issue its explicit detach. Selection
				// uses the command lifetime, while only event consumption is timed.
				if _, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser); err != nil {
					return nil, err
				}
				return runDirectWatchStream(watchCtx, stream, values.once)
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	cmd.Flags().DurationVar(&values.timeout, "timeout", 0, "Bound watch duration (Go duration)")
	cmd.Flags().BoolVar(&values.once, "once", false, "Stop after the first event")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

type webmcpDirectOperation func(context.Context, webmcp.Broker, config.BrowserConfig) (any, error)

func (c *WebMCPOperationsCommand) executeDirect(cmd *cobra.Command, values *webmcpDirectFlags, kind string, fallback webmcp.ErrorCode, operation webmcpDirectOperation) error {
	return c.executeDirectWithContext(cmd, values, kind, fallback, operation)
}

func (c *WebMCPOperationsCommand) executeDirectWithContext(cmd *cobra.Command, values *webmcpDirectFlags, kind string, fallback webmcp.ErrorCode, operation webmcpDirectOperation) error {
	if cmd == nil {
		return errors.New("WebMCP command is required")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return c.executeDirectWithParentContext(cmd, ctx, values, kind, fallback, operation)
}

func (c *WebMCPOperationsCommand) executeDirectWithParentContext(cmd *cobra.Command, ctx context.Context, values *webmcpDirectFlags, kind string, fallback webmcp.ErrorCode, operation webmcpDirectOperation) error {
	if cmd == nil {
		return errors.New("WebMCP command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandTimeout := directCommandTimeout(values)
	var data any
	var operationErr error
	if values != nil && values.commandTimeout < 0 {
		operationErr = directInvalidInputError("--command-timeout must not be negative", "/command_timeout")
	} else if values != nil && values.timeout < 0 {
		operationErr = directInvalidInputError("--timeout must be positive", "/timeout")
	} else {
		commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		if kind == "watch" && commandCtx.Err() != nil {
			// A canceled watch has a terminal data result and does not need to
			// construct a runtime just to observe that its stream is canceled.
			// This also keeps a legacy, non-cooperative factory off the critical
			// path after an interrupt-before-setup.
			data, operationErr = runDirectWatchStream(commandCtx, nil, values != nil && values.once)
		} else {
			data, operationErr = c.runDirect(commandCtx, cmd, values, operation)
		}
		cancel()
	}
	operationErr = preferDirectBrowserDisconnected(operationErr)
	var writeErr error
	if values != nil && values.json {
		writeErr = writeWebMCPDirectJSON(cmd.OutOrStdout(), data, operationErr, fallback)
	} else {
		writeErr = writeWebMCPDirectHuman(cmd.OutOrStdout(), kind, data, operationErr, fallback)
	}
	if writeErr != nil {
		return errors.Join(operationErr, writeErr)
	}
	return operationErr
}

func (c *WebMCPOperationsCommand) runDirect(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, operation webmcpDirectOperation) (data any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if values == nil {
		values = newWebMCPDirectFlags()
	}
	browser, resolveErr := c.resolveDirectBrowserConfig(cmd, values)
	if resolveErr != nil {
		return nil, resolveErr
	}
	factory := c.factory
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(c.globalFlags)
	}
	runtime, factoryErr := constructWebMCPDoctorRuntime(ctx, factory, browser)
	if factoryErr != nil {
		return nil, preferDirectBrowserDisconnected(errors.Join(directRuntimeFactoryFailure(factoryErr), closeWebMCPDoctorRuntimeBounded(runtime)))
	}
	if runtime.Broker == nil {
		return nil, preferDirectBrowserDisconnected(errors.Join(webmcpRuntimeUnavailableError("runtime_factory"), closeWebMCPDoctorRuntimeBounded(runtime)))
	}
	data, err = runWebMCPDirectOperation(ctx, operation, runtime.Broker, browser)
	return data, preferDirectBrowserDisconnected(errors.Join(err, closeWebMCPDoctorRuntimeBounded(runtime)))
}

func (c *WebMCPOperationsCommand) resolveDirectBrowserConfig(cmd *cobra.Command, values *webmcpDirectFlags) (config.BrowserConfig, error) {
	loaded, err := resolveSessionBrowserConfig(c.globalFlags, nil, nil)
	if err != nil {
		return config.BrowserConfig{}, err
	}
	if loaded == nil {
		return config.BrowserConfig{}, errors.New("browser configuration loader returned nil config")
	}
	resolved, err := loaded.Browser.ApplyBrowserOverrides(directBrowserOverrides(cmd, &values.browser))
	if err != nil {
		return config.BrowserConfig{}, fmt.Errorf("resolve WebMCP command flags: %w", err)
	}
	if err := validateDoctorEndpoints(resolved); err != nil {
		return config.BrowserConfig{}, err
	}
	endpoint := doctorEndpointFor(resolved)
	if endpoint.Scope == "non_loopback" && !resolved.Connection.AllowRemoteCDP {
		return config.BrowserConfig{}, webmcp.NewClassifiedError(webmcp.ErrorRemoteEndpointDenied, "remote browser endpoints require explicit permission", map[string]any{
			"endpoint_kind": endpointKindFor(resolved),
			"network_class": "non_loopback",
			"required_flag": "browser-allow-remote-cdp",
		})
	}
	return resolved, nil
}

func registerWebMCPDirectBrowserFlags(cmd *cobra.Command, values *flags.BrowserFlags) {
	if cmd == nil || values == nil {
		return
	}
	stringAliases := func(target *string, usage string, names ...string) {
		for _, name := range names {
			cmd.Flags().StringVar(target, name, "", usage)
		}
	}
	boolAliases := func(target *bool, usage string, names ...string) {
		for _, name := range names {
			bindStrictBrowserBool(cmd.Flags(), target, name, usage)
		}
	}
	stringAliases(&values.CDPURL, "Browser DevTools HTTP endpoint", "cdp-url", "browser-cdp-url")
	stringAliases(&values.WSEndpoint, "Browser DevTools WebSocket endpoint", "ws-endpoint", "browser-ws-endpoint")
	stringAliases(&values.UserDataDir, "Browser profile directory used for DevTools discovery", "user-data-dir", "browser-user-data-dir")
	boolAliases(&values.AllowProcessScan, "Allow process-based browser endpoint discovery", "allow-process-scan", "browser-allow-process-scan")
	boolAliases(&values.AllowRemoteCDP, "Allow non-loopback DevTools endpoints", "allow-remote-cdp", "browser-allow-remote-cdp")
	stringAliases(&values.Browser, "Exact normalized browser ID", "browser", "browser-browser")
	stringAliases(&values.Tab, "Exact browser target ID", "tab", "browser-tab")
	stringAliases(&values.Origin, "Exact browser page origin filter", "origin", "browser-origin")
	stringAliases(&values.AutoSelect, "Browser target auto-selection: off, single, or persisted", "auto-select", "browser-auto-select")
	boolAliases(&values.ActivateTab, "Activate the selected browser tab", "activate-tab", "browser-activate-tab")
	boolAliases(&values.PersistSelection, "Persist the selected browser ID and target metadata", "persist-selection", "browser-persist-selection")
	for _, name := range []string{"allowed-origin", "browser-allowed-origin"} {
		cmd.Flags().StringArrayVar(&values.AllowedOrigins, name, nil, "Allow an exact browser page origin (repeatable)")
	}
	for _, name := range []string{"denied-origin", "browser-denied-origin"} {
		cmd.Flags().StringArrayVar(&values.DeniedOrigins, name, nil, "Deny an exact browser page origin (repeatable)")
	}
	stringAliases(&values.Approval, "Browser page approval policy: always, writes, or never", "approval", "browser-approval")
	stringAliases(&values.CancelOnInterrupt, "Browser invocation cancellation policy: never, read-only, or always", "cancel-on-interrupt", "browser-cancel-on-interrupt")
	cmd.Flags().DurationVar(&values.InvocationTimeout, "invocation-timeout", 0, "Maximum browser invocation duration (Go duration)")
	cmd.Flags().DurationVar(&values.InvocationTimeout, "browser-invocation-timeout", 0, "Maximum browser invocation duration (Go duration)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxInputBytes, name: "max-input-bytes"}, "max-input-bytes", "Maximum browser input_json bytes (decimal integer)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxInputBytes, name: "browser-max-input-bytes"}, "browser-max-input-bytes", "Maximum browser input_json bytes (decimal integer)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxResultBytes, name: "max-result-bytes"}, "max-result-bytes", "Maximum browser result bytes (decimal integer)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxResultBytes, name: "browser-max-result-bytes"}, "browser-max-result-bytes", "Maximum browser result bytes (decimal integer)")
	boolAliases(&values.SerializePerTarget, "Serialize browser page calls per target", "serialize-per-target", "browser-serialize-per-target")
}

func directBrowserOverrides(cmd *cobra.Command, values *flags.BrowserFlags) config.BrowserOverrides {
	var overrides config.BrowserOverrides
	if cmd == nil || values == nil {
		return overrides
	}
	changed := func(names ...string) bool { return directFlagChanged(cmd, names...) }
	if changed("cdp-url", "browser-cdp-url") {
		overrides.CDPURL = &values.CDPURL
	}
	if changed("ws-endpoint", "browser-ws-endpoint") {
		overrides.WSEndpoint = &values.WSEndpoint
	}
	if changed("user-data-dir", "browser-user-data-dir") {
		overrides.UserDataDir = &values.UserDataDir
	}
	if changed("allow-process-scan", "browser-allow-process-scan") {
		overrides.AllowProcessScan = &values.AllowProcessScan
	}
	if changed("allow-remote-cdp", "browser-allow-remote-cdp") {
		overrides.AllowRemoteCDP = &values.AllowRemoteCDP
	}
	if changed("browser", "browser-browser") {
		overrides.Browser = &values.Browser
	}
	if changed("tab", "browser-tab") {
		overrides.Tab = &values.Tab
	}
	if changed("origin", "browser-origin") {
		overrides.Origin = &values.Origin
	}
	if changed("auto-select", "browser-auto-select") {
		overrides.AutoSelect = &values.AutoSelect
	}
	if changed("activate-tab", "browser-activate-tab") {
		overrides.ActivateTab = &values.ActivateTab
	}
	if changed("persist-selection", "browser-persist-selection") {
		overrides.PersistSelection = &values.PersistSelection
	}
	if changed("allowed-origin", "browser-allowed-origin") {
		overrides.AllowedOrigins = &values.AllowedOrigins
	}
	if changed("denied-origin", "browser-denied-origin") {
		overrides.DeniedOrigins = &values.DeniedOrigins
	}
	if changed("approval", "browser-approval") {
		overrides.Approval = &values.Approval
	}
	if changed("cancel-on-interrupt", "browser-cancel-on-interrupt") {
		overrides.CancelOnInterrupt = &values.CancelOnInterrupt
	}
	if changed("invocation-timeout", "browser-invocation-timeout") {
		overrides.InvocationTimeout = &values.InvocationTimeout
	}
	if changed("max-input-bytes", "browser-max-input-bytes") {
		overrides.MaxInputBytes = &values.MaxInputBytes
	}
	if changed("max-result-bytes", "browser-max-result-bytes") {
		overrides.MaxResultBytes = &values.MaxResultBytes
	}
	if changed("serialize-per-target", "browser-serialize-per-target") {
		overrides.SerializePerTarget = &values.SerializePerTarget
	}
	return overrides
}

func (c *WebMCPOperationsCommand) resolveDirectTarget(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, broker webmcp.Broker, browser config.BrowserConfig) (webmcp.BrowserCandidate, webmcp.Target, *WebMCPSelection, error) {
	browserID := browser.Selection.Browser
	targetID := browser.Selection.Tab
	stored := (*WebMCPSelection)(nil)

	// Explicit command selectors take precedence over both config and the
	// persisted record. When no target selector is supplied, an existing
	// persisted record is used as an exact opaque ID, never as a hint for a
	// different target.
	if targetID == "" && !directFlagChanged(cmd, "tab", "browser-tab") && !directBrowserFlagChanged(cmd) {
		selection, err := c.loadDirectSelection()
		if err != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "persisted browser selection could not be read", map[string]any{
				"reason": "persisted_selection_invalid",
			})
		}
		if selection.BrowserID != "" || selection.TargetID != "" {
			if selection.BrowserID == "" || selection.TargetID == "" {
				return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "persisted browser selection is incomplete", map[string]any{
					"reason": "persisted_selection_incomplete",
				})
			}
			storedCopy := selection
			stored = &storedCopy
			if browserID == "" {
				browserID = selection.BrowserID
			}
			targetID = selection.TargetID
		}
	}

	candidates, err := discoverDirectCandidates(ctx, broker, browser)
	if err != nil {
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "browser_not_found", err)
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
	}
	if browserID == "" {
		ids := directBrowserCandidateIDs(candidates)
		if len(ids) != 1 {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousBrowser, "multiple browsers matched; an exact browser ID is required", map[string]any{
				"candidate_browser_ids": ids,
			})
		}
		browserID = string(candidates[0].ID)
	}
	var candidate webmcp.BrowserCandidate
	for _, possible := range candidates {
		if string(possible.ID) == browserID {
			candidate = possible
			break
		}
	}
	if candidate.ID == "" {
		if stored != nil {
			if reason, replacement := directReplacementReason(candidates, browser, *stored); replacement {
				return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, reason, nil)
			}
		}
		err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           targetID,
			"selected_generation": persistedSelectionGeneration(stored),
			"reason":              "browser_not_found",
		})
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "browser_not_found", err)
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
	}
	if stored != nil {
		if stored.EndpointID != "" && stored.EndpointID != string(candidate.ID) {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "endpoint_changed", nil)
		}
		if stored.BrowserInstanceID != candidate.BrowserInstanceID && (stored.BrowserInstanceID != "" || candidate.BrowserInstanceID != "") {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "browser_instance_changed", nil)
		}
	}

	targets, err := broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "target_list_failed", err)
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
	}
	for index := range targets {
		if targets[index].BrowserID == "" {
			targets[index].BrowserID = candidate.ID
		}
	}

	var target *webmcp.Target
	if targetID != "" {
		for index := range targets {
			if string(targets[index].ID) == targetID {
				selected := targets[index]
				target = &selected
				break
			}
		}
		if target == nil {
			err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
				"browser_id":          browserID,
				"target_id":           targetID,
				"selected_generation": uint64(0),
				"reason":              "target_not_found",
			})
			if stored != nil {
				return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "target_not_found", err)
			}
			return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
		}
	} else {
		matches := directEligibleTargetMatches(targets, browser)
		switch {
		case len(matches) == 0:
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, directNoEligibleTabError(browserID, browser, len(targets), "")
		case len(matches) > 1:
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
				"browser_id":           normalizeDirectOpaqueID(browserID),
				"candidate_target_ids": directTargetCandidateIDs(matches),
			})
		case browser.Selection.AutoSelect == config.BrowserAutoSelectPersisted:
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "persisted browser target selection is not current", map[string]any{
				"browser_id":          browserID,
				"target_id":           "",
				"selected_generation": uint64(0),
				"reason":              "persisted_selection_missing",
			})
		case browser.Selection.AutoSelect == config.BrowserAutoSelectOff || browser.Selection.AutoSelect == "":
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "no browser target is selected; provide an exact target ID or enable auto-selection", map[string]any{
				"browser_id":          browserID,
				"target_id":           "",
				"selected_generation": uint64(0),
				"reason":              "selection_required",
			})
		default:
			selected := matches[0]
			target = &selected
		}
	}

	if target == nil {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, directNoEligibleTabError(browserID, browser, len(targets), "")
	}
	if stored != nil {
		if stored.Origin != "" && safeOrigin(stored.Origin) != safeOrigin(target.Origin) {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "origin_changed", nil)
		}
		if stored.ContinuityMarker != "" && target.ContinuityMarker != "" && stored.ContinuityMarker != target.ContinuityMarker {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "continuity_changed", nil)
		}
		if stored.Generation != 0 && target.Generation != 0 && stored.Generation != target.Generation {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionErrorAtGeneration(browserID, targetID, stored.Generation, "generation_changed", nil)
		}
	}
	if err := directTargetPolicyError(*target, browser); err != nil {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, err
	}
	if target.Type != "" && !strings.EqualFold(target.Type, "page") {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, directNoEligibleTabError(browserID, browser, len(targets), "not_page")
	}
	if !target.Eligible {
		if strings.EqualFold(target.EligibilityReason, "unsupported_webmcp") {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "the selected target does not provide WebMCP", map[string]any{
				"browser_id":          browserID,
				"target_id":           targetID,
				"required_capability": "webmcp",
			})
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, directNoEligibleTabError(browserID, browser, len(targets), boundedDirectReason(target.EligibilityReason))
	}
	if browser.Selection.Origin != "" && safeOrigin(target.Origin) != safeOrigin(browser.Selection.Origin) {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, directNoEligibleTabError(browserID, browser, len(targets), "origin_mismatch")
	}
	return candidate, *target, stored, nil
}

func directCancelHasExactTarget(cmd *cobra.Command, browser config.BrowserConfig, stored *WebMCPSelection) bool {
	if stored != nil {
		return true
	}
	if browser.Selection.Tab != "" {
		return true
	}
	return directFlagChanged(cmd, "tab", "browser-tab")
}

func boundedDirectReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "ineligible"
	}
	if len(value) > 80 {
		return value[:80]
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "ineligible"
		}
	}
	return value
}

func stalePersistedSelectionErrorAtGeneration(browserID, targetID string, generation uint64, reason string, cause error) error {
	if phase, disconnected := persistedBrowserLossPhase(reason, cause); disconnected {
		details := map[string]any{
			"browser_id":         browserID,
			"target_id":          targetID,
			"phase":              phase,
			"reconnect_required": true,
		}
		err := webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), details)
		err.Cause = cause
		return err
	}
	details := map[string]any{
		"browser_id":          browserID,
		"target_id":           targetID,
		"selected_generation": generation,
		"reason":              reason,
	}
	err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, stalePersistedSelectionMessage(reason), details)
	err.Cause = cause
	return err
}

func stalePersistedSelectionMessage(reason string) string {
	switch reason {
	case "browser_instance_changed", "endpoint_changed":
		return "the selected browser was replaced; rediscover and explicitly select a browser target"
	default:
		return "the persisted browser target selection is no longer current"
	}
}

func persistedSelectionGeneration(selection *WebMCPSelection) uint64 {
	if selection == nil {
		return 0
	}
	return selection.Generation
}

func persistedBrowserLossPhase(reason string, cause error) (string, bool) {
	if reason == "target_not_found" {
		return "", false
	}
	fallback := "targets"
	if reason == "browser_not_found" {
		fallback = "discovery"
	}
	return browserLossPhase(cause, fallback)
}

func browserLossPhase(err error, fallback string) (string, bool) {
	if err == nil {
		return "", false
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		switch classified.Code {
		case webmcp.ErrorBrowserDisconnected, webmcp.ErrorEndpointNotFound, webmcp.ErrorEndpointUnreachable:
			if phase, ok := safePersistedPhase(classified.Details["phase"]); ok {
				return phase, true
			}
			return fallback, true
		case webmcp.ErrorStaleSelection:
			if classified.Details != nil && classified.Details["reason"] == "browser_not_found" {
				return fallback, true
			}
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, nested := range joined.Unwrap() {
			if phase, found := browserLossPhase(nested, fallback); found {
				return phase, true
			}
		}
	}
	if phase, found := browserLossPhase(errors.Unwrap(err), fallback); found {
		return phase, true
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "connection refused") || strings.Contains(message, "connection reset") || strings.Contains(message, "connection lost") || strings.Contains(message, "browser disconnected") || strings.Contains(message, "endpoint not found") {
		return fallback, true
	}
	return "", false
}

func safePersistedPhase(value any) (string, bool) {
	phase, ok := value.(string)
	if !ok {
		return "", false
	}
	phase = strings.TrimSpace(phase)
	if phase == "" || len(phase) > 32 {
		return "", false
	}
	for _, character := range phase {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "", false
		}
	}
	return phase, true
}

func directTargetPolicyError(target webmcp.Target, browser config.BrowserConfig) error {
	origin := safeOrigin(target.Origin)
	if deniedOrigin(origin, browser.Policy) {
		return webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is denied by policy", map[string]any{
			"origin_digest": originDigest(origin),
			"policy":        "denied_origins",
		})
	}
	if len(browser.Policy.AllowedOrigins) > 0 && !allowedOrigin(origin, browser.Policy.AllowedOrigins) {
		return webmcp.NewClassifiedError(webmcp.ErrorOriginDenied, "the selected page origin is not in the allowed policy", map[string]any{
			"origin_digest": originDigest(origin),
			"policy":        "allowed_origins",
		})
	}
	return nil
}
