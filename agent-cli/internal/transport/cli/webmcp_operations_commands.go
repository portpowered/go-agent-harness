package cli

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations/xvideo"
	"github.com/spf13/cobra"
)

const (
	flagJSON           = "json"
	flagJSONUsage      = "Write one machine-readable JSON result"
	flagTimeout        = "timeout"
	flagReason         = "reason"
	flagCommandTimeout = "command-timeout"
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

const webmcpInvokeHelp = `Invoke a selected page tool by exact reference or unique name.

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
within %v; cancellation does not claim rollback or safe retry.`

const webmcpCancelHelp = `Cancel a pending browser invocation using the exact ID from the
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
cancel_requested is never terminal confirmation.`

const webmcpXPrepareVideoHelp = "Transfer one MP4 (up to 64 MiB) through the selected X WebMCP router. Requires --file, --text, and --account. Returns a one-use draft token after X finishes processing. Review the draft, then explicitly invoke x_publish_post to publish. Failure or timeout never authorizes automatic publishing; inspect any remaining composer before retrying."

// newDirectCommand builds one direct command with the shared browser,
// command-timeout, and --json flags.
func newDirectCommand(use, short string, args cobra.PositionalArgs, values *webmcpDirectFlags, run func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: args, SilenceUsage: true, RunE: run}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	registerWebMCPDirectCommandTimeoutFlag(cmd, values)
	return cmd
}

func registerDirectJSONFlag(cmd *cobra.Command, values *webmcpDirectFlags) {
	cmd.Flags().BoolVar(&values.json, flagJSON, false, flagJSONUsage)
}

func (c *WebMCPOperationsCommand) browsersCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("browsers", "List discovered WebMCP browsers", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "browsers", webmcp.ErrorEndpointNotFound, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			browserID := ""
			if directBrowserFlagChanged(cmd) {
				browserID = values.browser.Browser
			}
			return directOutcome(operations.Browsers(ctx, broker, browser, browserID))
		})
	})
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) tabsCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("tabs", "List browser tabs available for WebMCP", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "tabs", webmcp.ErrorEndpointNotFound, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			filter := operations.TabFilter{OriginContains: values.originContains, EligibleOnly: values.eligible}
			return directOutcome(operations.Tabs(ctx, broker, browser, filter))
		})
	})
	cmd.Flags().BoolVar(&values.eligible, "eligible", false, "List only targets eligible for WebMCP")
	cmd.Flags().BoolVar(&values.includeZeroToolPages, "include-zero-tool-pages", false, "Include eligible pages with no known tools")
	cmd.Flags().StringVar(&values.originContains, "origin-contains", "", "Filter by an origin substring")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) selectCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("select", "Select an exact browser target for WebMCP", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "select", webmcp.ErrorTargetAttachFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			activate := browser.Selection.ActivateTab
			if directFlagChanged(cmd, "activate") {
				activate = values.activate
			}
			return directOutcome(operations.Select(ctx, broker, operations.SelectRequest{
				Selector:      c.directSelector(cmd, browser),
				Activate:      activate,
				SaveSelection: c.saveDirectSelection,
			}))
		})
	})
	cmd.Flags().BoolVar(&values.activate, "activate", false, "Activate the selected tab after attaching")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) activateCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("activate", "Activate an exact browser target", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "activate", webmcp.ErrorTargetAttachFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return directOutcome(operations.Activate(ctx, broker, c.directSelector(cmd, browser)))
		})
	})
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) contextCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("context", "Show the selected WebMCP page context", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "context", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return directOutcome(operations.DescribeContext(ctx, broker, c.directSelector(cmd, browser), values.refresh))
		})
	})
	cmd.Flags().BoolVar(&values.refresh, "refresh", false, "Refresh browser and catalog metadata")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) toolsCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("tools", "List tools exposed by the selected WebMCP page", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		if values.watch {
			return c.executeDirect(cmd, values, directKindWatch, webmcp.ErrorStaleSelection, c.watchOperation(cmd, values))
		}
		return c.executeDirect(cmd, values, "tools", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return directOutcome(operations.Tools(ctx, broker, c.directSelector(cmd, browser), operations.ToolQuery{
				Refresh:        values.refresh,
				NameContains:   values.nameContains,
				IncludeSchemas: values.includeSchemas,
				FrameID:        values.frameID,
			}))
		})
	})
	cmd.Long = "List tools exposed by the selected WebMCP page. With --watch, observe the target-scoped semantic stream across independent CLI or CDP clients.\n\n" + webmcpWatchHelp
	cmd.Flags().BoolVar(&values.refresh, "refresh", false, "Refresh the selected page catalog")
	cmd.Flags().StringVar(&values.nameContains, "name-contains", "", "Filter tools by a name substring")
	cmd.Flags().StringVar(&values.frameID, "frame-id", "", "Filter tools to one frame identifier")
	cmd.Flags().BoolVar(&values.includeSchemas, "include-schemas", true, "Include complete page input schemas")
	cmd.Flags().BoolVar(&values.watch, "watch", false, "Watch catalog and invocation events instead of listing once")
	cmd.Flags().BoolVar(&values.once, "once", false, "Stop watch mode after the first event")
	cmd.Flags().DurationVar(&values.timeout, flagTimeout, 0, "Bound watch duration (Go duration)")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) invokeCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("invoke [tool-name] [key=value...]", "Invoke a selected page tool by ref or unique name", cobra.ArbitraryArgs, values, func(cmd *cobra.Command, args []string) error {
		invokeCtx, interrupted, stopInterrupt := operations.NewInterruptContext(cmd.Context())
		defer stopInterrupt()
		return c.executeDirectWithParentContext(cmd, invokeCtx, values, "invoke", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return directOutcome(operations.Invoke(ctx, broker, operations.InvokeRequest{
				Selector: c.directSelector(cmd, browser),
				Input: operations.InvocationInput{
					Args:           args,
					ToolRef:        values.toolRef,
					InputJSON:      values.inputJSON,
					ParseArguments: parseKeyValueArgs,
				},
				Reason:      values.reason,
				Timeout:     values.timeout,
				Receipts:    cmd.ErrOrStderr(),
				Interrupted: interrupted,
			}))
		})
	})
	cmd.Long = fmt.Sprintf(webmcpInvokeHelp, operations.InterruptReconciliationTimeout)
	cmd.Flags().StringVar(&values.toolRef, "tool-ref", "", "Exact generation-bound WebMCP tool reference")
	cmd.Flags().StringVar(&values.inputJSON, "input-json", "", "JSON object passed to the page tool")
	cmd.Flags().StringVar(&values.reason, flagReason, "direct CLI invocation", "User-facing reason for the page action")
	cmd.Flags().DurationVar(&values.timeout, flagTimeout, 0, "Bound invocation duration (Go duration)")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) cancelCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("cancel [invocation-id]", "Cancel a pending WebMCP invocation", cobra.MaximumNArgs(1), values, func(cmd *cobra.Command, args []string) error {
		return c.executeDirect(cmd, values, "cancel", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			return directOutcome(operations.Cancel(ctx, broker, operations.CancelRequest{
				Selector:     c.directSelector(cmd, browser),
				InvocationID: values.invocationID,
				Args:         args,
				Reason:       values.reason,
				Timeout:      values.timeout,
			}))
		})
	})
	cmd.Long = webmcpCancelHelp
	cmd.Flags().StringVar(&values.invocationID, "invocation", "", "Exact invocation ID")
	cmd.Flags().StringVar(&values.reason, flagReason, "direct CLI cancellation", "User-facing cancellation reason")
	cmd.Flags().DurationVar(&values.timeout, flagTimeout, 0, "Bound cancellation reconciliation duration (Go duration)")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

func (c *WebMCPOperationsCommand) watchCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := newDirectCommand("watch", "Watch WebMCP broker events", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, directKindWatch, webmcp.ErrorEndpointUnreachable, c.watchOperation(cmd, values))
	})
	cmd.Long = webmcpWatchHelp
	cmd.Flags().DurationVar(&values.timeout, flagTimeout, 0, "Bound watch duration (Go duration)")
	cmd.Flags().BoolVar(&values.once, "once", false, "Stop after the first event")
	registerDirectJSONFlag(cmd, values)
	return cmd
}

// watchOperation is shared by watch and tools --watch, which promise the
// same observation and output contract.
func (c *WebMCPOperationsCommand) watchOperation(cmd *cobra.Command, values *webmcpDirectFlags) webmcpDirectOperation {
	return func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
		return directOutcome(operations.Watch(ctx, broker, operations.WatchRequest{
			Selector: c.directSelector(cmd, browser),
			Timeout:  values.timeout,
			Once:     values.once,
		}))
	}
}

func (c *WebMCPOperationsCommand) xPrepareVideoCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	request := xvideo.Request{}
	cmd := newDirectCommand("x-prepare-video", "Transfer a local MP4 and prepare an X video draft (never publishes)", cobra.NoArgs, values, func(cmd *cobra.Command, _ []string) error {
		return c.executeDirect(cmd, values, "x-prepare-video", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
			prepared := request
			prepared.Selector = c.directSelector(cmd, browser)
			prepared.Reason = values.reason
			prepared.Recovery = cmd.ErrOrStderr()
			return directOutcome(xvideo.Prepare(ctx, broker, prepared))
		})
	})
	cmd.Long = webmcpXPrepareVideoHelp
	values.commandTimeout = xvideo.PreparationTimeout
	cmd.Flags().Lookup(flagCommandTimeout).DefValue = xvideo.PreparationTimeout.String()
	cmd.Flags().StringVar(&request.File, "file", "", "Local MP4 path (read-only, up to 64 MiB)")
	cmd.Flags().StringVar(&request.Caption, "text", "", "Exact caption")
	cmd.Flags().StringVar(&request.Account, "account", "", "Expected signed-in @handle")
	cmd.Flags().StringVar(&values.reason, flagReason, "prepare user-requested X video draft", "User-facing reason")
	registerDirectJSONFlag(cmd, values)
	return cmd
}
