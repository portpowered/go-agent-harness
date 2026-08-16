package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

// SessionCommand is the session group (parent command); subcommands are wired in routes.go.
type SessionCommand struct {
	askFlags                  *flags.AskFlags
	globalFlags               *flags.GlobalFlags
	sessionInferencerOverride messages.SessionInferencer
}

// NewSessionCommand returns the session group command constructor.
func NewSessionCommand(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, sessionInferencerOverride messages.SessionInferencer) *SessionCommand {
	return &SessionCommand{askFlags: askFlags, globalFlags: globalFlags, sessionInferencerOverride: sessionInferencerOverride}
}

// Generate returns the cobra command for the session group.
func (c *SessionCommand) Generate() *cobra.Command {
	var prompt string
	cmd := &cobra.Command{
		Use:   "session [message]",
		Short: "Run or manage agent sessions",
		Long: "Run a bidirectional session inference capture or replay a session capture file.\n" +
			"Use --record <file>.json to capture live session traffic, or --replay <file>.json to replay a saved capture without live provider network calls.\n\n" +
			"Session history management remains available through the show, list, and delete subcommands.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.askFlags.RecordCapturePath == "" && c.askFlags.ReplayCapturePath == "" {
				return cmd.Help()
			}
			return services.RunSessionWithTextSeed(cmd.Context(), cmd.OutOrStdout(), services.SessionRunOptions{
				RecordPath:        c.askFlags.RecordCapturePath,
				ReplayPath:        c.askFlags.ReplayCapturePath,
				Provider:          c.askFlags.Provider,
				Model:             c.askFlags.Model,
				ModelProvided:     cmd.Flags().Changed("model"),
				APIKey:            c.askFlags.APIKey,
				BaseURL:           c.askFlags.BaseURL,
				ConfigDir:         c.globalFlags.ConfigDir(),
				Prompt:            strings.Join(args, " "),
				SessionInferencer: c.sessionInferencerOverride,
			}, services.SessionTextSeed{
				Value:   prompt,
				Present: cmd.Flags().Changed("prompt"),
			})
		},
	}
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record bidirectional session traffic to a JSON capture file")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay bidirectional session traffic from a JSON capture file without live provider network calls")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Seed the realtime session with text")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Session provider ID (use grok or openai for live record mode)")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Session model ID for live record mode")
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "Session provider API key for live record mode")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Session provider base URL override")
	return cmd
}

// getSessionStorage resolves workspace from global flags and returns session storage.
func getSessionStorage(globalFlags *flags.GlobalFlags) (*session.Storage, error) {
	workspaceDir := globalFlags.ConfigDir()
	if workspaceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get workspace dir: %w", err)
		}
		workspaceDir = filepath.Join(home, config.ConfigDirName)
	}
	workspaceDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("get workspace dir: %w", err)
	}
	return session.NewStorage(workspaceDir), nil
}

// SessionShowCommand wraps the session show subcommand.
type SessionShowCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionShowCommand creates the SessionShowCommand with the given flags.
func NewSessionShowCommand(flags *flags.GlobalFlags) *SessionShowCommand {
	return &SessionShowCommand{flags: flags}
}

// Generate returns the cobra command for session show.
func (c *SessionShowCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show a session by ID",
		Long:  "Load and print the conversation history for the given session.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			msgs, err := storage.Load(sessionID)
			if err != nil {
				return err
			}
			if msgs == nil {
				return fmt.Errorf("session %s not found", sessionID)
			}
			return writeSessionTo(cmd.OutOrStdout(), sessionID, msgs)
		},
	}
}

func writeSessionTo(w io.Writer, sessionID string, msgs []messages.Message) error {
	if _, err := fmt.Fprintf(w, "Session: %s\n", sessionID); err != nil {
		return err
	}
	for _, m := range msgs {
		role := string(m.Role)
		text := m.TextContent()
		if _, err := fmt.Fprintf(w, "[%s] %s\n", role, text); err != nil {
			return err
		}
	}
	return nil
}

// SessionListCommand wraps the session list subcommand.
type SessionListCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionListCommand creates the SessionListCommand with the given flags.
func NewSessionListCommand(flags *flags.GlobalFlags) *SessionListCommand {
	return &SessionListCommand{flags: flags}
}

// Generate returns the cobra command for session list.
func (c *SessionListCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all sessions",
		Long:  "List session IDs with last modified time, newest first.",
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			infos, err := storage.List()
			if err != nil {
				return err
			}
			if len(infos) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				return err
			}
			for _, info := range infos {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", info.ID, info.ModTime.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// SessionDeleteCommand wraps the session delete subcommand.
type SessionDeleteCommand struct {
	flags *flags.GlobalFlags
}

// NewSessionDeleteCommand creates the SessionDeleteCommand with the given flags.
func NewSessionDeleteCommand(flags *flags.GlobalFlags) *SessionDeleteCommand {
	return &SessionDeleteCommand{flags: flags}
}

// Generate returns the cobra command for session delete.
func (c *SessionDeleteCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a session by ID",
		Long:  "Remove the session file. Use session list to see IDs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storage, err := getSessionStorage(c.flags)
			if err != nil {
				return err
			}
			sessionID := args[0]
			if err := storage.Delete(sessionID); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted session %s\n", sessionID); err != nil {
				return err
			}
			return nil
		},
	}
}
