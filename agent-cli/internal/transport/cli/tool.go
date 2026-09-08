package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/logger"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	"github.com/spf13/cobra"
)

// ToolCommand wraps the tool subcommand for invoking tools directly (debugging).
// It loads config at run time and uses only enabled tools from config.
type ToolCommand struct {
	globalFlags          *flags.GlobalFlags
	capabilityLoader     func() (runtimeTools.Capability, error)
	configStorageFactory func(string) (*config.ConfigStorage, error)
	runtimeService       runtimeTools.Service
}

var (
	errToolNotFound     = errors.New("tool not found")
	errToolArguments    = errors.New("invalid tool argument")
	errToolUnavailable  = errors.New("tool unavailable in this build")
	errToolConfig       = errors.New("tool configuration error")
	errToolFlagConflict = errors.New("tool flag conflict")
	errToolIDRequired   = errors.New("tool id required")
	errToolRefusal      = errors.New("filesystem operation refused")
)

type toolCommandError struct {
	kind    error
	message string
	cause   error
}

func (e *toolCommandError) Error() string { return e.message }

func (e *toolCommandError) Unwrap() error {
	if e.cause == nil {
		return e.kind
	}
	return errors.Join(e.kind, e.cause)
}

func newToolCommandError(kind error, message string, cause error) error {
	return &toolCommandError{kind: kind, message: message, cause: cause}
}

// NewToolCommand creates the ToolCommand with global flags (used to load config and resolve enabled tools).
func NewToolCommand(globalFlags *flags.GlobalFlags) *ToolCommand {
	return &ToolCommand{globalFlags: globalFlags, runtimeService: runtimeToolsWire.NewService()}
}

// getCapability loads config and resolves a request-scoped runtime tool
// capability. The CLI owns the host path snapshot; registry construction and
// tool execution stay inside the reusable tools service.
func (c *ToolCommand) getCapability() (runtimeTools.Capability, error) {
	policy, err := c.filesystemPolicy()
	if err != nil {
		return runtimeTools.Capability{}, newToolCommandError(errToolConfig, fmt.Sprintf("filesystem scope: %v", err), err)
	}
	if c.capabilityLoader != nil {
		capability, err := c.capabilityLoader()
		if err != nil {
			return runtimeTools.Capability{}, newToolCommandError(errToolConfig, err.Error(), err)
		}
		return capability, nil
	}
	storageFactory := config.NewDefaultConfigStorage
	if c.configStorageFactory != nil {
		storageFactory = c.configStorageFactory
	}
	storage, err := storageFactory(c.globalFlags.ConfigDir())
	if err != nil {
		return runtimeTools.Capability{}, newToolCommandError(errToolConfig, fmt.Sprintf("config: %v", err), err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return runtimeTools.Capability{}, newToolCommandError(errToolConfig, fmt.Sprintf("load config: %v", err), err)
	}
	service := c.runtimeService
	if service == nil {
		service = runtimeToolsWire.NewService()
	}
	selections := make([]runtimeTools.ToolSelection, 0, len(cfg.Tools.List))
	for _, entry := range cfg.Tools.List {
		selections = append(selections, runtimeTools.ToolSelection{ID: entry.ID, Enabled: entry.Enabled})
	}
	capability, err := service.Resolve(context.Background(), runtimeTools.Request{
		WorkDir:    policy.PrimaryRoot(),
		AllowPaths: policy.AdditionalRoots(),
		Selections: selections,
		Exec: runtimeTools.ExecPolicy{
			EnableDenyPatterns: cfg.Tools.Exec.EnableDenyPatterns,
			CustomDenyPatterns: append([]string(nil), cfg.Tools.Exec.CustomDenyPatterns...),
			Configured:         true,
		},
		UseDefaultTool: true,
	})
	if err != nil {
		return runtimeTools.Capability{}, newToolCommandError(errToolConfig, fmt.Sprintf("resolve tools: %v", err), err)
	}
	return capability, nil
}

func (c *ToolCommand) filesystemPolicy() (*tools.FilesystemPolicy, error) {
	if c == nil {
		return tools.ResolveFilesystemPolicy("")
	}
	var workdir string
	var allowPaths []string
	if c.globalFlags != nil {
		workdir = c.globalFlags.WorkDir()
		allowPaths = c.globalFlags.AllowPaths()
	}
	return tools.ResolveFilesystemPolicy(workdir, allowPaths...)
}

// parseKeyValueArgs parses args of the form "key=value" into a map.
// Values are trimmed of surrounding double or single quotes. Values are kept as strings
// unless they parse as json (number, boolean), in which case they are stored as the parsed type.
func parseKeyValueArgs(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, s := range args {
		idx := strings.Index(s, "=")
		if idx <= 0 {
			return nil, newToolCommandError(errToolArguments, fmt.Sprintf("invalid argument %q: expected key=value", s), nil)
		}
		key := strings.TrimSpace(s[:idx])
		valStr := strings.TrimSpace(s[idx+1:])
		if key == "" {
			return nil, newToolCommandError(errToolArguments, fmt.Sprintf("invalid argument %q: empty key", s), nil)
		}
		valStr = unquoteValue(valStr)
		out[key] = coerceValue(valStr)
	}
	return out, nil
}

func unquoteValue(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// coerceValue keeps value as string; optionally parses numbers and booleans for tool schema compatibility.
func coerceValue(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}

// Generate returns the cobra command for tool.
func (c *ToolCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "tool <tool-id> [key=value...]",
		Short:         "Invoke a tool directly by name and key=value args (for debugging)",
		Long:          "Invoke a tool directly for debugging.\n\n" + filesystemPolicyHelp,
		Example:       "  yui tool read_file path=./foo.txt\n  yui tool exec command=\"go test ./...\"",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ArbitraryArgs,
		RunE:          c.run,
	}
	cmd.Flags().Bool("list", false, "List available tool IDs")
	return cmd
}

func (c *ToolCommand) run(cmd *cobra.Command, args []string) error {
	list, err := cmd.Flags().GetBool("list")
	if err != nil {
		return fmt.Errorf("read --list flag: %w", err)
	}
	if err := validateToolCommandArgs(list, args); err != nil {
		return err
	}
	capability, err := c.getCapability()
	if err != nil {
		return err
	}
	if list {
		return c.listTools(cmd.OutOrStdout(), capability.Definitions)
	}
	return c.invoke(cmd, capability, args)
}

func validateToolCommandArgs(list bool, args []string) error {
	if list && len(args) > 0 {
		return newToolCommandError(errToolFlagConflict, "cannot combine --list with a tool id", nil)
	}
	if !list && len(args) == 0 {
		return newToolCommandError(errToolIDRequired, "tool-id required (e.g. agent tool read_file path=./foo.txt). Use --list to list tools", nil)
	}
	return nil
}

func (c *ToolCommand) invoke(cmd *cobra.Command, capability runtimeTools.Capability, args []string) error {
	toolID := args[0]
	parsed, err := parseKeyValueArgs(args[1:])
	if err != nil {
		return err
	}
	if !hasToolDefinition(capability.Definitions, toolID) {
		return fmt.Errorf("tool %q: %w", toolID, newToolCommandError(errToolNotFound, fmt.Sprintf("tool %q not found", toolID), nil))
	}
	ctx := toolCommandContext(cmd.Context())
	return c.execute(cmd, ctx, capability, toolID, parsed)
}

func toolCommandContext(ctx context.Context) context.Context {
	if currentLogger := logger.GetRequestLoggerFromContext(ctx); currentLogger == nil || currentLogger == logger.GetDefaultLogger() {
		return logger.WithLogger(ctx, logger.GetDefaultLogger())
	}
	return ctx
}

func (c *ToolCommand) execute(cmd *cobra.Command, ctx context.Context, capability runtimeTools.Capability, toolID string, parsed map[string]any) error {
	arguments, err := json.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("tool %q: encode arguments: %w", toolID, err)
	}
	if capability.Executor == nil {
		return fmt.Errorf("tool %q: %w", toolID, errToolUnavailable)
	}
	response, err := capability.Executor.Execute(ctx, messages.ToolCall{ID: toolID, Name: toolID, Arguments: string(arguments)})
	if err != nil {
		return fmt.Errorf("tool %q: %w", toolID, err)
	}
	return c.writeToolResponse(cmd, toolID, response)
}

func (c *ToolCommand) writeToolResponse(cmd *cobra.Command, toolID string, response messages.ToolCallResponse) error {
	if refusal, ok := filesystemRefusalFromResponse(response); ok {
		if err := c.writeRefusal(cmd.ErrOrStderr(), refusal); err != nil {
			return err
		}
		return newToolCommandError(errToolRefusal, refusal.Error(), &tools.FilesystemRefusalError{Refusal: refusal})
	}
	return c.writeResponse(cmd.OutOrStdout(), response)
}

func filesystemRefusalFromResponse(response messages.ToolCallResponse) (tools.FilesystemRefusal, bool) {
	if refusal, ok := tools.FilesystemRefusalFromContent(response.Content); ok {
		return refusal, true
	}
	return tools.FilesystemRefusal{}, false
}

func (c *ToolCommand) writeRefusal(w io.Writer, refusal tools.FilesystemRefusal) error {
	encoded, err := tools.MarshalFilesystemRefusal(refusal)
	if err != nil {
		return fmt.Errorf("encode filesystem refusal: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(encoded)); err != nil {
		return fmt.Errorf("write filesystem refusal: %w", err)
	}
	return nil
}

func (c *ToolCommand) listTools(w io.Writer, definitions []messages.ToolDefinition) error {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintln(w, name); err != nil {
			return err
		}
	}
	return nil
}

func hasToolDefinition(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func (c *ToolCommand) writeResponse(w io.Writer, response messages.ToolCallResponse) error {
	if response.Content != "" {
		if _, err := fmt.Fprint(w, response.Content); err != nil {
			return err
		}
		return nil
	}
	return c.writeMessages(w, []messages.Message{{ContentParts: response.ContentParts}})
}

func (c *ToolCommand) writeMessages(w io.Writer, msgs []messages.Message) error {
	for _, m := range msgs {
		text := m.TextContent()
		if text != "" {
			if _, err := fmt.Fprint(w, text); err != nil {
				return err
			}
		}
	}
	return nil
}
