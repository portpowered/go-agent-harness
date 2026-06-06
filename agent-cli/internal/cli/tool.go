package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/logger"
	"github.com/portpowered/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

// ToolCommand wraps the tool subcommand for invoking tools directly (debugging).
// It loads config at run time and uses only enabled tools from config.
type ToolCommand struct {
	globalFlags *flags.GlobalFlags
}

// NewToolCommand creates the ToolCommand with global flags (used to load config and resolve enabled tools).
func NewToolCommand(globalFlags *flags.GlobalFlags) *ToolCommand {
	return &ToolCommand{globalFlags: globalFlags}
}

// getRegistry loads config and returns a registry with only enabled tools.
func (c *ToolCommand) getRegistry() (*tools.ToolRegistry, error) {
	storage, err := config.NewDefaultConfigStorage(c.globalFlags.ConfigDir())
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return tools.NewToolRegistryFromConfig(cfg), nil
}

// parseKeyValueArgs parses args of the form "key=value" into a map.
// Values are trimmed of surrounding double or single quotes. Values are kept as strings
// unless they parse as json (number, boolean), in which case they are stored as the parsed type.
func parseKeyValueArgs(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, s := range args {
		idx := strings.Index(s, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid argument %q: expected key=value", s)
		}
		key := strings.TrimSpace(s[:idx])
		valStr := strings.TrimSpace(s[idx+1:])
		if key == "" {
			return nil, fmt.Errorf("invalid argument %q: empty key", s)
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
		Use:   "tool <tool-id> [key=value...]",
		Short: "Invoke a tool directly by name and key=value args (for debugging)",
		Long:  "Invoke a tool directly. Example: agent tool read_file path=./foo.txt",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := c.getRegistry()
			if err != nil {
				return err
			}
			list, _ := cmd.Flags().GetBool("list")
			if list {
				return c.listTools(cmd.OutOrStdout(), registry)
			}
			if len(args) < 1 {
				return fmt.Errorf("tool-id required (e.g. agent tool read_file path=./foo.txt). Use --list to list tools")
			}

			toolID := args[0]
			keyValues := args[1:]

			parsed, err := parseKeyValueArgs(keyValues)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			if l := logger.GetRequestLoggerFromContext(ctx); l == nil || l == logger.GetDefaultLogger() {
				ctx = logger.WithLogger(ctx, logger.GetDefaultLogger())
			}

			msgs, err := registry.Execute(ctx, toolID, parsed)
			if err != nil {
				return fmt.Errorf("tool %q: %w", toolID, err)
			}

			return c.writeMessages(cmd.OutOrStdout(), msgs)
		},
	}
	cmd.Flags().Bool("list", false, "List available tool IDs")
	return cmd
}

func (c *ToolCommand) listTools(w io.Writer, registry *tools.ToolRegistry) error {
	for _, name := range registry.List() {
		fmt.Fprintln(w, name)
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
