package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/doctor"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/spf13/cobra"
)

// The direct command results are owned by internal/webmcp/operations; these
// names keep the CLI's public result vocabulary.
type (
	WebMCPDirectBrowser           = operations.Browser
	WebMCPDirectBrowsersData      = operations.BrowsersData
	WebMCPDirectTab               = operations.Tab
	WebMCPDirectTabsData          = operations.TabsData
	WebMCPDirectContext           = operations.Context
	WebMCPDirectFrame             = operations.Frame
	WebMCPDirectTool              = operations.Tool
	WebMCPDirectToolsData         = operations.ToolsData
	WebMCPDirectInvocation        = operations.Invocation
	WebMCPDirectCancelData        = operations.CancelData
	WebMCPDirectEvent             = operations.Event
	WebMCPDirectWatchData         = operations.WatchData
	WebMCPDirectInvocationReceipt = operations.Receipt
)

// directKindWatch names the watch kind, which has a terminal result even
// when it is canceled before a runtime is constructed.
const directKindWatch = "watch"

type webmcpDirectFlags struct {
	browser flags.BrowserFlags
	json    bool

	eligible             bool
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

// AddCommands attaches the direct WebMCP commands.
func (c *WebMCPOperationsCommand) AddCommands(parent *cobra.Command) {
	if c == nil || parent == nil {
		return
	}
	commands := []*cobra.Command{
		c.browsersCommand(),
		c.tabsCommand(),
		c.selectCommand(),
		c.activateCommand(),
		c.contextCommand(),
		c.toolsCommand(),
		c.invokeCommand(),
		c.xPrepareVideoCommand(),
		c.cancelCommand(),
		c.watchCommand(),
	}
	// Direct operations render their own output. Cobra must not render errors again,
	// otherwise human output is duplicated and JSON mode is contaminated with
	// a second, non-JSON stderr line.
	for _, command := range commands {
		command.SilenceErrors = true
	}
	parent.AddCommand(commands...)
}

func newWebMCPDirectFlags() *webmcpDirectFlags {
	return &webmcpDirectFlags{}
}

type webmcpDirectOperation = direct.Operation

// directOutcome adapts a typed operation result to the untyped command
// result; a failed operation has no data.
func directOutcome[T any](value T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return value, nil
}

// directSelector describes the command's target request for the operations
// package.
func (c *WebMCPOperationsCommand) directSelector(cmd *cobra.Command, browser config.BrowserConfig) operations.Selector {
	return operations.Selector{
		Browser:            browser,
		BrowserFlagChanged: directBrowserFlagChanged(cmd),
		TabFlagChanged:     directFlagChanged(cmd, "tab", "browser-tab"),
		LoadSelection:      c.loadDirectSelection,
	}
}

func (c *WebMCPOperationsCommand) executeDirect(cmd *cobra.Command, values *webmcpDirectFlags, kind string, fallback webmcp.ErrorCode, operation webmcpDirectOperation) error {
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
	if values == nil {
		values = newWebMCPDirectFlags()
	}
	execution := operations.Execution{
		CommandTimeout: values.commandTimeout,
		Timeout:        values.timeout,
		Watch:          kind == directKindWatch,
		Once:           values.once,
	}
	data, operationErr := operations.Execute(ctx, execution, func(commandCtx context.Context) (any, error) {
		return c.runDirect(commandCtx, cmd, values, operation)
	})
	var writeErr error
	if values.json {
		writeErr = writeWebMCPDirectJSON(cmd.OutOrStdout(), data, operationErr, fallback)
	} else {
		writeErr = writeWebMCPDirectHuman(cmd.OutOrStdout(), kind, data, operationErr, fallback)
	}
	if writeErr != nil {
		return errors.Join(operationErr, writeErr)
	}
	return operationErr
}

func (c *WebMCPOperationsCommand) runDirect(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, operation webmcpDirectOperation) (any, error) {
	browser, err := c.resolveDirectBrowserConfig(cmd, values)
	if err != nil {
		return nil, err
	}
	factory := c.factory
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(c.globalFlags)
	}
	return direct.Run(ctx, factory, browser, operation)
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
	if err := doctor.CheckEndpointPolicy(resolved); err != nil {
		return config.BrowserConfig{}, err
	}
	return resolved, nil
}
