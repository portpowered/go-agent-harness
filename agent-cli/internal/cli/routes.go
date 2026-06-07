package cli

import (
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

// Router defines the routes and wiring for all agent CLI commands.
type Router struct {
	Flags *flags.GlobalFlags

	RootCommand *RootCommand

	AskCommand  *AskCommand
	ChatCommand *ChatCommand

	ToolCommand *ToolCommand

	InteractionCommand       *InteractionCommand
	InteractionReplayCommand *InteractionReplayCommand

	SessionCommand       *SessionCommand
	SessionShowCommand   *SessionShowCommand
	SessionListCommand   *SessionListCommand
	SessionDeleteCommand *SessionDeleteCommand

	ConfigCommand         *ConfigCommand
	ConfigAddLocalCommand *ConfigAddLocalCommand
}

// NewRouter constructs a Router with the given dependencies.
func NewRouter(
	flags *flags.GlobalFlags,
	rootCommand *RootCommand,
	askCommand *AskCommand,
	chatCommand *ChatCommand,
	toolCommand *ToolCommand,
	interactionCommand *InteractionCommand,
	interactionReplayCommand *InteractionReplayCommand,
	sessionCommand *SessionCommand,
	sessionShowCommand *SessionShowCommand,
	sessionListCommand *SessionListCommand,
	sessionDeleteCommand *SessionDeleteCommand,
	configCommand *ConfigCommand,
	configAddLocalCommand *ConfigAddLocalCommand,
) *Router {
	return &Router{
		Flags:                    flags,
		RootCommand:              rootCommand,
		AskCommand:               askCommand,
		ChatCommand:              chatCommand,
		ToolCommand:              toolCommand,
		InteractionCommand:       interactionCommand,
		InteractionReplayCommand: interactionReplayCommand,
		SessionCommand:           sessionCommand,
		SessionShowCommand:       sessionShowCommand,
		SessionListCommand:       sessionListCommand,
		SessionDeleteCommand:     sessionDeleteCommand,
		ConfigCommand:            configCommand,
		ConfigAddLocalCommand:    configAddLocalCommand,
	}
}

// BuildRoot defines the overall routing structure and returns the root cobra command.
func (r *Router) BuildRoot() *cobra.Command {
	root := NewPath("agent", r.RootCommand.Generate())

	root.AddCommand(NewPath("ask [prompt] [files...]", r.AskCommand.Generate()))
	root.AddCommand(NewPath("chat", r.ChatCommand.Generate()))
	root.AddCommand(NewPath("tool <tool-id> [key=value...]", r.ToolCommand.Generate()))

	interactionGroup := NewPath("interaction", r.InteractionCommand.Generate())
	interactionGroup.AddCommand(NewPath("replay <fixture-path>", r.InteractionReplayCommand.Generate()))
	root.AddCommand(interactionGroup)

	sessionGroup := NewPath("session", r.SessionCommand.Generate())
	sessionGroup.AddCommand(NewPath("show <session-id>", r.SessionShowCommand.Generate()))
	sessionGroup.AddCommand(NewPath("list", r.SessionListCommand.Generate()))
	sessionGroup.AddCommand(NewPath("delete <session-id>", r.SessionDeleteCommand.Generate()))
	root.AddCommand(sessionGroup)

	configGroup := NewPath("config", r.ConfigCommand.Generate())
	configGroup.AddCommand(NewPath("add-local", r.ConfigAddLocalCommand.Generate()))
	root.AddCommand(configGroup)

	cmd := root.CreateCommand()
	cmd.PersistentFlags().CountVarP(&r.Flags.VerboseMode, "verbose", "v", "Enable verbose output (use -v for info, -vv for debug)")
	cmd.PersistentFlags().StringVarP(&r.Flags.ConfigDirPath, "config-dir", "C", r.Flags.ConfigDirPath, "Directory for agent CLI config (default: ~/.agent-cli)")
	cmd.PersistentFlags().BoolVar(&r.Flags.LogToStdout, "log-to-stdout", false, "Log to stdout/stderr instead of file (default: logs to file in config directory)")

	return cmd
}
