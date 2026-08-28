package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
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
)

// ErrWebMCPOperationsRequiresLaneBOrD identifies the production discovery
// and browser protocol seams that are intentionally not guessed by the CLI
// composition root. Direct commands surface it as an unavailable operation.
var ErrWebMCPOperationsRequiresLaneBOrD = ErrWebMCPDoctorRequiresLaneBOrD

// WebMCPSelection is the small persisted record shared by separate direct
// command invocations. It is intentionally limited to normalized IDs and a
// redacted origin; endpoint credentials and websocket paths never cross this
// boundary.
type WebMCPSelection struct {
	Version    int       `json:"version"`
	EndpointID string    `json:"endpoint_id"`
	BrowserID  string    `json:"browser_id"`
	TargetID   string    `json:"target_id"`
	Origin     string    `json:"origin"`
	SelectedAt time.Time `json:"selected_at"`
}

// WebMCPSelectionStore persists and loads one opaque browser selection.
// Implementations may be injected by embedders and command tests.
type WebMCPSelectionStore interface {
	Load() (WebMCPSelection, error)
	Save(WebMCPSelection) error
}

// FileWebMCPSelectionStore is the default user-only selection store.
type FileWebMCPSelectionStore struct {
	Path string
}

// NewFileWebMCPSelectionStore constructs a selection store below configDir.
// An empty configDir follows the same ~/.agent-cli default as ConfigStorage.
func NewFileWebMCPSelectionStore(configDir string) *FileWebMCPSelectionStore {
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, config.ConfigDirName)
		}
	}
	return &FileWebMCPSelectionStore{Path: filepath.Join(configDir, WebMCPSelectionFileName)}
}

func (s *FileWebMCPSelectionStore) Load() (WebMCPSelection, error) {
	if s == nil || s.Path == "" {
		return WebMCPSelection{}, errors.New("WebMCP selection path is unavailable")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return WebMCPSelection{}, nil
	}
	if err != nil {
		return WebMCPSelection{}, fmt.Errorf("read WebMCP selection: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var selection WebMCPSelection
	if err := decoder.Decode(&selection); err != nil {
		return WebMCPSelection{}, fmt.Errorf("decode WebMCP selection: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return WebMCPSelection{}, errors.New("decode WebMCP selection: more than one JSON value")
		}
		return WebMCPSelection{}, fmt.Errorf("decode WebMCP selection: %w", err)
	}
	if err := validateWebMCPSelection(selection); err != nil {
		return WebMCPSelection{}, err
	}
	if selection.Origin != "" {
		selection.Origin = safeOrigin(selection.Origin)
	}
	return selection, nil
}

func (s *FileWebMCPSelectionStore) Save(selection WebMCPSelection) error {
	if s == nil || s.Path == "" {
		return errors.New("WebMCP selection path is unavailable")
	}
	if selection.Version == 0 {
		selection.Version = WebMCPSelectionVersion
	}
	if selection.SelectedAt.IsZero() {
		selection.SelectedAt = time.Now().UTC()
	}
	if selection.Origin != "" {
		selection.Origin = safeOrigin(selection.Origin)
	}
	if err := validateWebMCPSelection(selection); err != nil {
		return err
	}
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create WebMCP selection directory: %w", err)
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode WebMCP selection: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".webmcp-selection-*")
	if err != nil {
		return fmt.Errorf("create WebMCP selection temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect WebMCP selection temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write WebMCP selection: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close WebMCP selection temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace WebMCP selection: %w", err)
	}
	return nil
}

func validateWebMCPSelection(selection WebMCPSelection) error {
	if selection.Version != WebMCPSelectionVersion {
		return fmt.Errorf("WebMCP selection version %d is unsupported", selection.Version)
	}
	if selection.BrowserID == "" || selection.TargetID == "" {
		return errors.New("WebMCP selection requires browser_id and target_id")
	}
	if selection.Origin != "" {
		selectionOrigin := safeOrigin(selection.Origin)
		if selectionOrigin == "" {
			return errors.New("WebMCP selection origin is invalid")
		}
	}
	return nil
}

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
	factory := unavailableWebMCPDoctorFactory
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
					return nil, directRequiresLaneError("target activation")
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
	cmd.Flags().BoolVar(&values.refresh, "refresh", false, "Refresh browser and catalog metadata")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) toolsCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "tools",
		Short:        "List tools exposed by the selected WebMCP page",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if values.watch {
				return c.executeDirect(cmd, values, "watch", webmcp.ErrorStaleSelection, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
					if _, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser); err != nil {
						return nil, err
					}
					watchCtx := ctx
					if values.timeout > 0 {
						var cancel context.CancelFunc
						watchCtx, cancel = context.WithTimeout(ctx, values.timeout)
						defer cancel()
					}
					return runDirectWatch(watchCtx, broker, values.once)
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
		Use:          "invoke [tool-name] [key=value...]",
		Short:        "Invoke a selected page tool by ref or unique name",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.executeDirectWithContext(cmd, values, "invoke", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
				_, err := c.ensureDirectSelection(ctx, cmd, values, broker, browser)
				if err != nil {
					return nil, err
				}
				toolRef, input, err := resolveDirectInvocation(args, values, broker, ctx)
				if err != nil {
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
				return WebMCPDirectInvocation{InvocationID: string(result.InvocationID), ToolRef: string(toolRef), Status: status, Output: append(json.RawMessage(nil), output...)}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
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
		Use:          "cancel [invocation-id]",
		Short:        "Cancel a pending WebMCP invocation",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.executeDirect(cmd, values, "cancel", webmcp.ErrorInvocationFailed, func(ctx context.Context, broker webmcp.Broker, _ config.BrowserConfig) (any, error) {
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
				if err := broker.Cancel(ctx, webmcp.CancelRequest{InvocationID: webmcp.InvocationID(invocationID), Reason: values.reason}); err != nil {
					return nil, err
				}
				return WebMCPDirectCancelData{InvocationID: invocationID, Status: "cancel_requested"}, nil
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
	cmd.Flags().StringVar(&values.invocationID, "invocation", "", "Exact invocation ID")
	cmd.Flags().StringVar(&values.reason, "reason", "direct CLI cancellation", "User-facing cancellation reason")
	cmd.Flags().BoolVar(&values.json, "json", false, "Write one machine-readable JSON result")
	return cmd
}

func (c *WebMCPOperationsCommand) watchCommand() *cobra.Command {
	values := newWebMCPDirectFlags()
	cmd := &cobra.Command{
		Use:          "watch",
		Short:        "Watch WebMCP broker events",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.executeDirectWithContext(cmd, values, "watch", webmcp.ErrorEndpointUnreachable, func(ctx context.Context, broker webmcp.Broker, _ config.BrowserConfig) (any, error) {
				watchCtx := ctx
				if values.timeout > 0 {
					var cancel context.CancelFunc
					watchCtx, cancel = context.WithTimeout(ctx, values.timeout)
					defer cancel()
				}
				return runDirectWatch(watchCtx, broker, values.once)
			})
		},
	}
	registerWebMCPDirectBrowserFlags(cmd, &values.browser)
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
	var data any
	var operationErr error
	if values != nil && values.timeout < 0 {
		operationErr = directInvalidInputError("--timeout must be positive", "/timeout")
	} else {
		data, operationErr = c.runDirect(ctx, cmd, values, operation)
	}
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
		factory = unavailableWebMCPDoctorFactory
	}
	runtime, factoryErr := factory(browser)
	if factoryErr != nil {
		return nil, errors.Join(factoryErr, closeWebMCPDoctorRuntime(runtime))
	}
	if runtime.Broker == nil {
		return nil, errors.Join(directRequiresLaneError("direct WebMCP operations"), closeWebMCPDoctorRuntime(runtime))
	}
	data, err = operation(ctx, runtime.Broker, browser)
	return data, errors.Join(err, closeWebMCPDoctorRuntime(runtime))
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

func directBrowserFlagChanged(cmd *cobra.Command) bool {
	return directFlagChanged(cmd, "browser", "browser-browser")
}

func directFlagChanged(cmd *cobra.Command, names ...string) bool {
	if cmd == nil {
		return false
	}
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
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

func discoverDirectBrowsers(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig, browserID string) ([]webmcp.BrowserCandidate, error) {
	if broker == nil {
		return nil, directRequiresLaneError("browser discovery")
	}
	candidates, err := broker.Discover(ctx, webmcp.DiscoverOptions{
		BrowserID:        webmcp.BrowserID(browserID),
		ExplicitOnly:     browser.Connection.CDPURL != "" || browser.Connection.WSEndpoint != "",
		AllowProcessScan: browser.Connection.AllowProcessScan,
		AllowRemoteCDP:   browser.Connection.AllowRemoteCDP,
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	if browserID != "" {
		for _, candidate := range candidates {
			if string(candidate.ID) == browserID {
				return candidates, nil
			}
		}
		return nil, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "browser_not_found",
		})
	}
	if len(candidates) == 0 {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": endpointKindFor(browser),
			"source":        string(webmcp.DiscoverySourceConfigured),
		})
	}
	return candidates, nil
}

func directRequiresLaneError(operation string) error {
	return fmt.Errorf("%w: %s requires Lane B or requires Lane D", ErrWebMCPOperationsRequiresLaneBOrD, operation)
}

type directResolvedTarget struct {
	candidate webmcp.BrowserCandidate
	target    webmcp.Target
	stored    *WebMCPSelection
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

	candidates, err := discoverDirectBrowsers(ctx, broker, browser, browserID)
	if err != nil {
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "browser_not_found", err)
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
	}
	if browserID == "" {
		if len(candidates) != 1 {
			ids := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				ids = append(ids, string(candidate.ID))
			}
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
		err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           targetID,
			"selected_generation": uint64(0),
			"reason":              "browser_not_found",
		})
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "browser_not_found", err)
		}
		return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
	}

	targets, err := broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidate.ID})
	if err != nil {
		if stored != nil {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "target_list_failed", err)
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
				return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "target_not_found", err)
			}
			return webmcp.BrowserCandidate{}, webmcp.Target{}, nil, err
		}
	} else {
		matches := make([]webmcp.Target, 0, len(targets))
		for _, possible := range targets {
			if possible.Type != "" && !strings.EqualFold(possible.Type, "page") {
				continue
			}
			if !possible.Eligible {
				continue
			}
			if browser.Selection.Origin != "" && safeOrigin(possible.Origin) != safeOrigin(browser.Selection.Origin) {
				continue
			}
			if err := directTargetPolicyError(possible, browser); err != nil {
				continue
			}
			matches = append(matches, possible)
		}
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		switch {
		case len(matches) == 0:
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{
				"browser_id":      browserID,
				"filters":         map[string]any{"origin": browser.Selection.Origin},
				"candidate_count": 0,
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
		case len(matches) > 1:
			ids := make([]string, 0, len(matches))
			for _, match := range matches {
				ids = append(ids, string(match.ID))
			}
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible browser targets matched; an exact target ID is required", map[string]any{
				"browser_id":           browserID,
				"candidate_target_ids": ids,
			})
		default:
			selected := matches[0]
			target = &selected
		}
	}

	if target == nil {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", map[string]any{
			"browser_id":      browserID,
			"candidate_count": 0,
		})
	}
	if stored != nil {
		if stored.EndpointID != "" && stored.EndpointID != string(candidate.ID) {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "endpoint_changed", nil)
		}
		if stored.Origin != "" && safeOrigin(stored.Origin) != safeOrigin(target.Origin) {
			return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, stalePersistedSelectionError(browserID, targetID, "origin_changed", nil)
		}
	}
	if err := directTargetPolicyError(*target, browser); err != nil {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, err
	}
	if browser.Selection.Origin != "" && safeOrigin(target.Origin) != safeOrigin(browser.Selection.Origin) {
		return webmcp.BrowserCandidate{}, webmcp.Target{}, stored, webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "the selected target does not match the requested origin", map[string]any{
			"browser_id":      browserID,
			"target_id":       string(target.ID),
			"filters":         map[string]any{"origin": safeOrigin(browser.Selection.Origin)},
			"candidate_count": 0,
		})
	}
	return candidate, *target, stored, nil
}

func stalePersistedSelectionError(browserID, targetID, reason string, cause error) error {
	details := map[string]any{
		"browser_id":          browserID,
		"target_id":           targetID,
		"selected_generation": uint64(0),
		"reason":              reason,
	}
	err := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the persisted browser target selection is no longer current", details)
	err.Cause = cause
	return err
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

func (c *WebMCPOperationsCommand) loadDirectSelection() (WebMCPSelection, error) {
	store, err := c.selectionStore()
	if err != nil {
		return WebMCPSelection{}, err
	}
	return store.Load()
}

func (c *WebMCPOperationsCommand) saveDirectSelection(selection WebMCPSelection) error {
	store, err := c.selectionStore()
	if err != nil {
		return err
	}
	return store.Save(selection)
}

func (c *WebMCPOperationsCommand) selectionStore() (WebMCPSelectionStore, error) {
	if c != nil && c.SelectionStore != nil {
		return c.SelectionStore, nil
	}
	configDir := ""
	if c != nil && c.globalFlags != nil {
		configDir = c.globalFlags.ConfigDir()
	}
	store := NewFileWebMCPSelectionStore(configDir)
	if store.Path == "" {
		return nil, errors.New("WebMCP selection store is unavailable")
	}
	return store, nil
}

func (c *WebMCPOperationsCommand) selectDirectTarget(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
	candidate, target, _, err := c.resolveDirectTarget(ctx, cmd, values, broker, browser)
	if err != nil {
		return nil, err
	}
	activate := browser.Selection.ActivateTab
	if directFlagChanged(cmd, "activate") {
		activate = values.activate
	}
	page, err := selectDirectTarget(ctx, broker, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}, activate)
	if err != nil {
		return nil, err
	}
	if page.Key.BrowserID == "" {
		page.Key.BrowserID = candidate.ID
	}
	if page.Key.TargetID == "" {
		page.Key.TargetID = target.ID
	}
	if page.Origin == "" {
		page.Origin = target.Origin
	}
	data, err := c.contextWithCatalog(ctx, broker, page, false)
	if err != nil {
		return nil, err
	}
	if browser.Selection.Persist {
		if err := c.saveDirectSelection(WebMCPSelection{
			Version:    WebMCPSelectionVersion,
			EndpointID: string(candidate.ID),
			BrowserID:  string(page.Key.BrowserID),
			TargetID:   string(page.Key.TargetID),
			Origin:     safeOrigin(page.Origin),
			SelectedAt: time.Now().UTC(),
		}); err != nil {
			return nil, fmt.Errorf("persist WebMCP selection: %w", err)
		}
	}
	return data, nil
}

func selectDirectTarget(ctx context.Context, broker webmcp.Broker, selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if selectorWithOptions, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	}
	if activate {
		return webmcp.PageContext{}, directRequiresLaneError("target activation")
	}
	return broker.Select(ctx, selector)
}

func (c *WebMCPOperationsCommand) ensureDirectSelection(ctx context.Context, cmd *cobra.Command, values *webmcpDirectFlags, broker webmcp.Broker, browser config.BrowserConfig) (webmcp.PageContext, error) {
	candidate, target, _, err := c.resolveDirectTarget(ctx, cmd, values, broker, browser)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	return selectDirectTarget(ctx, broker, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}, browser.Selection.ActivateTab)
}

func selectedDirectContext(ctx context.Context, broker webmcp.Broker, refresh bool) (webmcp.PageContext, error) {
	if refresher, ok := broker.(interface {
		SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
	}); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return broker.Selected(ctx)
}

func (c *WebMCPOperationsCommand) contextWithCatalog(ctx context.Context, broker webmcp.Broker, page webmcp.PageContext, refresh bool) (WebMCPDirectContext, error) {
	if refresh {
		refreshed, err := selectedDirectContext(ctx, broker, true)
		if err != nil {
			return WebMCPDirectContext{}, err
		}
		page = refreshed
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return WebMCPDirectContext{}, err
	}
	if snapshot.Context.Key.BrowserID != "" {
		page = snapshot.Context
	}
	return directContextDataWithCatalog(page, snapshot), nil
}

func directContextData(page webmcp.PageContext) WebMCPDirectContext {
	return WebMCPDirectContext{
		BrowserID:  string(page.Key.BrowserID),
		TargetID:   string(page.Key.TargetID),
		Title:      boundedDoctorText(page.Title, 160),
		URL:        redactedDirectPageURL(page.URL),
		Origin:     safeOrigin(page.Origin),
		Generation: page.Generation,
		Connected:  page.Connected,
		Ready:      page.Ready,
	}
}

func directContextDataWithCatalog(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot) WebMCPDirectContext {
	data := directContextData(page)
	data.CatalogGeneration = snapshot.Generation
	data.ToolCount = len(snapshot.Tools)
	data.CatalogReady = snapshot.Context.Ready && snapshot.Context.Connected
	if data.BrowserID == "" {
		data.BrowserID = string(snapshot.Context.Key.BrowserID)
	}
	if data.TargetID == "" {
		data.TargetID = string(snapshot.Context.Key.TargetID)
	}
	if data.Generation == 0 {
		data.Generation = snapshot.Context.Generation
	}
	if data.Origin == "" {
		data.Origin = safeOrigin(snapshot.Context.Origin)
	}
	return data
}

func directTabFromTarget(target webmcp.Target) WebMCPDirectTab {
	typeName := target.Type
	if typeName == "" {
		typeName = "page"
	}
	return WebMCPDirectTab{
		BrowserID:         string(target.BrowserID),
		TargetID:          string(target.ID),
		Type:              boundedDoctorText(typeName, 40),
		Title:             boundedDoctorText(target.Title, 160),
		Origin:            safeOrigin(target.Origin),
		Eligible:          target.Eligible,
		EligibilityReason: boundedDoctorText(target.EligibilityReason, 160),
		Attached:          target.Attached,
	}
}

func directToolsData(page webmcp.PageContext, snapshot webmcp.ToolCatalogSnapshot, includeSchemas bool) WebMCPDirectToolsData {
	contextValue := snapshot.Context
	if contextValue.Key.BrowserID == "" {
		contextValue = page
	}
	tools := make([]WebMCPDirectTool, 0, len(snapshot.Tools))
	for _, descriptor := range snapshot.Tools {
		tools = append(tools, directToolFromDescriptor(descriptor, includeSchemas))
	}
	sort.SliceStable(tools, func(i, j int) bool {
		if tools[i].Frame.ID != tools[j].Frame.ID {
			return tools[i].Frame.ID < tools[j].Frame.ID
		}
		return tools[i].Name < tools[j].Name
	})
	return WebMCPDirectToolsData{
		BrowserID:  string(contextValue.Key.BrowserID),
		TargetID:   string(contextValue.Key.TargetID),
		Generation: snapshot.Generation,
		Tools:      tools,
	}
}

func directToolFromDescriptor(descriptor webmcp.ToolDescriptor, includeSchemas bool) WebMCPDirectTool {
	schema := json.RawMessage(nil)
	if includeSchemas {
		schema = append(json.RawMessage(nil), descriptor.InputSchema...)
		if len(bytes.TrimSpace(schema)) == 0 || !json.Valid(schema) {
			schema = json.RawMessage("null")
		}
	}
	annotations := make(map[string]any)
	if descriptor.Annotations.ReadOnly != nil {
		annotations["read_only"] = *descriptor.Annotations.ReadOnly
	}
	if descriptor.Annotations.UntrustedContent != nil {
		annotations["untrusted_content"] = *descriptor.Annotations.UntrustedContent
	}
	if descriptor.Annotations.AutoSubmit != nil {
		annotations["autosubmit"] = *descriptor.Annotations.AutoSubmit
	}
	return WebMCPDirectTool{
		Ref:         string(descriptor.Ref),
		Name:        boundedDoctorText(descriptor.Name, 160),
		Description: boundedDoctorText(descriptor.Description, 500),
		InputSchema: schema,
		Annotations: annotations,
		Frame:       WebMCPDirectFrame{ID: string(descriptor.FrameID), Origin: safeOrigin(descriptor.Origin)},
		Generation:  descriptor.Generation,
	}
}

func resolveDirectInvocation(args []string, values *webmcpDirectFlags, broker webmcp.Broker, ctx context.Context) (webmcp.ToolRef, json.RawMessage, error) {
	if values == nil {
		return "", nil, errors.New("invoke flags are required")
	}
	if values.toolRef != "" && len(args) > 0 {
		return "", nil, directInvalidInputError("--tool-ref cannot be combined with a positional tool name", "/tool_ref")
	}
	if len(args) > 1 && values.inputJSON != "" {
		return "", nil, directInvalidInputError("--input-json cannot be combined with key=value arguments", "/input_json")
	}
	input := json.RawMessage(values.inputJSON)
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	if !json.Valid(input) {
		return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "--input-json must contain one valid JSON value", map[string]any{
			"tool_ref": values.toolRef,
			"issues":   []webmcp.ToolResultIssue{{Path: "/input-json", Code: "invalid_json"}},
		})
	}
	if values.toolRef != "" {
		return webmcp.ToolRef(values.toolRef), append(json.RawMessage(nil), input...), nil
	}
	if len(args) == 0 {
		return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "a tool reference or unique tool name is required", map[string]any{
			"issues": []webmcp.ToolResultIssue{{Path: "/tool_ref", Code: "required"}},
		})
	}
	toolName := args[0]
	keyValues, err := parseKeyValueArgs(args[1:])
	if err != nil {
		return "", nil, err
	}
	if len(args) > 1 {
		encoded, err := json.Marshal(keyValues)
		if err != nil {
			return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "key=value arguments could not be encoded as JSON", map[string]any{
				"issues": []webmcp.ToolResultIssue{{Path: "/arguments", Code: "invalid_json"}},
			})
		}
		input = encoded
	}
	snapshot, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: false})
	if err != nil {
		return "", nil, err
	}
	var match *webmcp.ToolDescriptor
	for index := range snapshot.Tools {
		if snapshot.Tools[index].Name != toolName {
			continue
		}
		if match != nil {
			return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the positional tool name is ambiguous; use --tool-ref", map[string]any{
				"issues": []webmcp.ToolResultIssue{{Path: "/tool_name", Code: "ambiguous"}},
			})
		}
		selected := snapshot.Tools[index]
		match = &selected
	}
	if match == nil {
		return "", nil, webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, "the positional tool name was not found in the current catalog", map[string]any{
			"issues": []webmcp.ToolResultIssue{{Path: "/tool_name", Code: "unknown_tool"}},
		})
	}
	return match.Ref, append(json.RawMessage(nil), input...), nil
}

func directInvalidInputError(message, path string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, message, map[string]any{
		"issues": []webmcp.ToolResultIssue{{Path: path, Code: "invalid"}},
	})
}

func directInvocationFailed(state webmcp.InvocationState) bool {
	switch state {
	case webmcp.InvocationError, webmcp.InvocationCanceled, webmcp.InvocationTimedOut, webmcp.InvocationOrphaned, webmcp.InvocationPolicyDenied:
		return true
	default:
		return false
	}
}

func directInvocationResultError(result webmcp.InvokeResult, toolRef webmcp.ToolRef) error {
	code := webmcp.ErrorCode(result.ErrorCode)
	if !webmcp.IsKnownErrorCode(code) {
		switch result.State {
		case webmcp.InvocationCanceled:
			code = webmcp.ErrorInvocationCanceled
		case webmcp.InvocationTimedOut:
			code = webmcp.ErrorInvocationTimedOut
		case webmcp.InvocationOrphaned:
			code = webmcp.ErrorInvocationOrphaned
		default:
			code = webmcp.ErrorInvocationFailed
		}
	}
	details := result.ErrorDetails
	if details == nil {
		details = map[string]any{"invocation_id": string(result.InvocationID), "tool_ref": string(toolRef), "phase": "invoke"}
	}
	return webmcp.NewClassifiedError(code, "the WebMCP invocation could not be completed", details)
}

func runDirectWatch(ctx context.Context, broker webmcp.Broker, once bool) (WebMCPDirectWatchData, error) {
	if broker == nil {
		return WebMCPDirectWatchData{}, directRequiresLaneError("broker watch")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stream := broker.Watch(ctx)
	data := WebMCPDirectWatchData{Status: webmcpDirectWatchStatusEnded, Events: []WebMCPDirectEvent{}}
	for {
		select {
		case <-ctx.Done():
			data.Status = webmcpDirectWatchStatusCanceled
			return data, nil
		case event, ok := <-stream:
			if !ok {
				return data, nil
			}
			data.Events = append(data.Events, directEventFrom(event))
			if once {
				data.Status = webmcpDirectWatchStatusOnce
				return data, nil
			}
		}
	}
}

func directEventFrom(event webmcp.BrokerEvent) WebMCPDirectEvent {
	return WebMCPDirectEvent{
		Version:      event.Version,
		Type:         string(event.Type),
		Sequence:     event.Sequence,
		BrowserID:    string(event.BrowserID),
		TargetID:     string(event.TargetID),
		Generation:   event.Generation,
		InvocationID: string(event.InvocationID),
		ToolRef:      string(event.ToolRef),
		State:        string(event.State),
		Reason:       boundedDoctorText(event.Reason, 160),
	}
}

func writeWebMCPDirectJSON(out io.Writer, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errors.New("WebMCP command output writer is required")
	}
	var encoded []byte
	var err error
	if operationErr != nil {
		resultError := webmcpDirectErrorFor(operationErr, fallback)
		encoded, err = webmcp.EncodeToolResult(nil, &resultError)
	} else {
		if data == nil {
			data = map[string]any{"status": "ok"}
		}
		encoded, err = webmcp.EncodeToolResult(data, nil)
	}
	if err != nil {
		return fmt.Errorf("encode WebMCP command result: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := out.Write(encoded); err != nil {
		return fmt.Errorf("write WebMCP command result: %w", err)
	}
	return nil
}

func writeWebMCPDirectHuman(out io.Writer, kind string, data any, operationErr error, fallback webmcp.ErrorCode) error {
	if out == nil {
		return errors.New("WebMCP command output writer is required")
	}
	if operationErr != nil {
		resultError := webmcpDirectErrorFor(operationErr, fallback)
		_, err := fmt.Fprintf(out, "Error: %s — %s\n", resultError.Code, resultError.Message)
		if err != nil {
			return fmt.Errorf("write WebMCP command error: %w", err)
		}
		return nil
	}

	var err error
	switch value := data.(type) {
	case WebMCPDirectBrowsersData:
		_, err = io.WriteString(out, "Browsers:\n")
		for _, browser := range value.Browsers {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  %s  %s  source=%s scope=%s", browser.ID, displayDoctorValue(browser.Product, "unknown"), browser.Source, browser.Scope)
			if browser.Endpoint != "" {
				if err == nil {
					_, err = fmt.Fprintf(out, " endpoint=%s", browser.Endpoint)
				}
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	case WebMCPDirectTabsData:
		_, err = io.WriteString(out, "Tabs:\n")
		for _, tab := range value.Tabs {
			if err != nil {
				break
			}
			marker := " "
			if tab.Selected {
				marker = "*"
			}
			_, err = fmt.Fprintf(out, "  %s %s/%s  %q  origin=%s eligible=%t connected=%t", marker, tab.BrowserID, tab.TargetID, tab.Title, displayDoctorValue(tab.Origin, "unknown"), tab.Eligible, tab.Attached)
			if tab.Generation > 0 {
				if err == nil {
					_, err = fmt.Fprintf(out, " generation=%d", tab.Generation)
				}
			}
			if tab.ToolCount != nil && err == nil {
				_, err = fmt.Fprintf(out, " tools=%d", *tab.ToolCount)
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	case WebMCPDirectContext:
		_, err = fmt.Fprintf(out, "Context: %s/%s\n  Title:      %q\n  Origin:     %s\n  URL:        %s\n  Generation: %d\n  Connected:  %t\n  Ready:      %t\n  Catalog:    %t (%d tools)\n", value.BrowserID, value.TargetID, value.Title, displayDoctorValue(value.Origin, "unknown"), displayDoctorValue(value.URL, "unknown"), value.Generation, value.Connected, value.Ready, value.CatalogReady, value.ToolCount)
	case WebMCPDirectToolsData:
		_, err = fmt.Fprintf(out, "Tools: %s/%s generation=%d\n", value.BrowserID, value.TargetID, value.Generation)
		for _, tool := range value.Tools {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  %s  %s  frame=%s origin=%s\n", tool.Ref, tool.Name, tool.Frame.ID, displayDoctorValue(tool.Frame.Origin, "unknown"))
		}
	case WebMCPDirectInvocation:
		_, err = fmt.Fprintf(out, "Invocation: %s status=%s tool_ref=%s\nOutput: %s\n", value.InvocationID, value.Status, value.ToolRef, compactDirectJSON(value.Output))
	case WebMCPDirectCancelData:
		_, err = fmt.Fprintf(out, "Invocation %s: %s\n", value.InvocationID, value.Status)
	case WebMCPDirectWatchData:
		_, err = fmt.Fprintf(out, "Watch: %s (%d events)\n", value.Status, len(value.Events))
		for _, event := range value.Events {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "  #%d %s", event.Sequence, event.Type)
			if event.BrowserID != "" || event.TargetID != "" {
				if err == nil {
					_, err = fmt.Fprintf(out, " %s/%s", event.BrowserID, event.TargetID)
				}
			}
			if event.Reason != "" && err == nil {
				_, err = fmt.Fprintf(out, " (%s)", event.Reason)
			}
			if err == nil {
				_, err = fmt.Fprintln(out)
			}
		}
	default:
		var encoded []byte
		encoded, err = json.MarshalIndent(data, "", "  ")
		if err == nil {
			_, err = fmt.Fprintln(out, string(encoded))
		}
	}
	if err != nil {
		return fmt.Errorf("write WebMCP command result: %w", err)
	}
	return nil
}

func webmcpDirectErrorFor(err error, fallback webmcp.ErrorCode) webmcp.ToolResultError {
	if errors.Is(err, ErrWebMCPOperationsRequiresLaneBOrD) {
		return webmcp.ToolResultError{
			Code:      string(webmcp.ErrorEndpointUnreachable),
			Message:   "This WebMCP operation is unavailable: it requires Lane B or requires Lane D for production browser discovery and protocol support.",
			Retryable: false,
			Details:   map[string]any{"requires": []string{"Lane B", "Lane D"}},
		}
	}
	result := webmcp.ResultErrorFor(err, fallback, nil)
	return result
}

func compactDirectJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func redactedDirectPageURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		if index := strings.IndexAny(raw, "?#"); index >= 0 {
			raw = raw[:index]
		}
		return boundedDoctorText(raw, 240)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return boundedDoctorText(parsed.String(), 240)
}
