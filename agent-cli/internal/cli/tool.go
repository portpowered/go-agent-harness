package cli

import (
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
	"github.com/spf13/cobra"
)

// ToolCommand wraps the tool subcommand for invoking tools directly (debugging).
// It loads config at run time and uses only enabled tools from config.
type ToolCommand struct {
	globalFlags          *flags.GlobalFlags
	registryLoader       func() (*tools.ToolRegistry, error)
	configStorageFactory func(string) (*config.ConfigStorage, error)
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
	return &ToolCommand{globalFlags: globalFlags}
}

// getRegistry loads config and returns a registry with only enabled tools.
func (c *ToolCommand) getRegistry() (*tools.ToolRegistry, error) {
	policy, err := c.filesystemPolicy()
	if err != nil {
		return nil, newToolCommandError(errToolConfig, fmt.Sprintf("filesystem scope: %v", err), err)
	}
	if c.registryLoader != nil {
		registry, err := c.registryLoader()
		if err != nil {
			return nil, newToolCommandError(errToolConfig, err.Error(), err)
		}
		return registry.WithFilesystemPolicy(policy), nil
	}
	storageFactory := config.NewDefaultConfigStorage
	if c.configStorageFactory != nil {
		storageFactory = c.configStorageFactory
	}
	storage, err := storageFactory(c.globalFlags.ConfigDir())
	if err != nil {
		return nil, newToolCommandError(errToolConfig, fmt.Sprintf("config: %v", err), err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return nil, newToolCommandError(errToolConfig, fmt.Sprintf("load config: %v", err), err)
	}
	return tools.NewToolRegistryFromConfigWithPolicy(cfg, policy), nil
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
		Long:          "Invoke a tool directly. Example: agent tool read_file path=./foo.txt\n\n" + filesystemPolicyHelp,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			list, _ := cmd.Flags().GetBool("list")
			if list && len(args) > 0 {
				return newToolCommandError(errToolFlagConflict, "cannot combine --list with a tool id", nil)
			}
			registry, err := c.getRegistry()
			if err != nil {
				return err
			}
			if list {
				return c.listTools(cmd.OutOrStdout(), registry)
			}
			if len(args) < 1 {
				return newToolCommandError(errToolIDRequired, "tool-id required (e.g. agent tool read_file path=./foo.txt). Use --list to list tools", nil)
			}

			toolID := args[0]
			keyValues := args[1:]

			parsed, err := parseKeyValueArgs(keyValues)
			if err != nil {
				return err
			}
			if _, ok := registry.Get(toolID); !ok {
				return fmt.Errorf("tool %q: %w", toolID, newToolCommandError(errToolNotFound, fmt.Sprintf("tool %q not found", toolID), nil))
			}

			ctx := cmd.Context()
			if l := logger.GetRequestLoggerFromContext(ctx); l == nil || l == logger.GetDefaultLogger() {
				ctx = logger.WithLogger(ctx, logger.GetDefaultLogger())
			}

			msgs, err := registry.Execute(ctx, toolID, parsed)
			if err != nil {
				return fmt.Errorf("tool %q: %w", toolID, err)
			}
			if refusal, ok := filesystemRefusalFromMessages(msgs); ok {
				if err := c.writeRefusal(cmd.ErrOrStderr(), refusal); err != nil {
					return err
				}
				return newToolCommandError(errToolRefusal, refusal.Error(), &tools.FilesystemRefusalError{Refusal: refusal})
			}

			return c.writeMessages(cmd.OutOrStdout(), msgs)
		},
	}
	cmd.Flags().Bool("list", false, "List available tool IDs")
	return cmd
}

func filesystemRefusalFromMessages(msgs []messages.Message) (tools.FilesystemRefusal, bool) {
	for _, message := range msgs {
		if refusal, ok := tools.FilesystemRefusalFromContent(message.TextContent()); ok {
			return refusal, true
		}
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

func (c *ToolCommand) listTools(w io.Writer, registry *tools.ToolRegistry) error {
	names := registry.List()
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintln(w, name); err != nil {
			return err
		}
	}
	return nil
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
