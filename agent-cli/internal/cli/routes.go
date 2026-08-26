package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

// Router defines the routes and wiring for all agent CLI commands.
type Router struct {
	Flags *flags.GlobalFlags

	deviceRegistry audio.DeviceRegistry

	RootCommand *RootCommand

	AskCommand  *AskCommand
	ChatCommand *ChatCommand

	ToolCommand *ToolCommand

	InteractionCommand       *InteractionCommand
	InteractionReplayCommand *InteractionReplayCommand

	ProbeCommand           *ProbeCommand
	ProbeRunCommand        *ProbeRunCommand
	ProbeGateCommand       *ProbeGateCommand
	ProbeReportCommand     *ProbeReportCommand
	ProbeAcceptanceCommand *ProbeAcceptanceCommand
	ProbeFleetCommand      *ProbeFleetCommand
	MediaCommand           *MediaCommand

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
	probeCommand *ProbeCommand,
	probeRunCommand *ProbeRunCommand,
	probeGateCommand *ProbeGateCommand,
	probeReportCommand *ProbeReportCommand,
	probeFleetCommand *ProbeFleetCommand,
	sessionCommand *SessionCommand,
	sessionShowCommand *SessionShowCommand,
	sessionListCommand *SessionListCommand,
	sessionDeleteCommand *SessionDeleteCommand,
	configCommand *ConfigCommand,
	configAddLocalCommand *ConfigAddLocalCommand,
	acceptanceCommands ...*ProbeAcceptanceCommand,
) *Router {
	acceptanceCommand := NewProbeAcceptanceCommand()
	if len(acceptanceCommands) > 0 && acceptanceCommands[0] != nil {
		acceptanceCommand = acceptanceCommands[0]
	}
	deviceRegistry := newDefaultDeviceRegistry()
	if probeRunCommand != nil && probeRunCommand.deviceRegistry != nil {
		deviceRegistry = probeRunCommand.deviceRegistry
	}
	return &Router{
		Flags:                    flags,
		deviceRegistry:           deviceRegistry,
		RootCommand:              rootCommand,
		AskCommand:               askCommand,
		ChatCommand:              chatCommand,
		ToolCommand:              toolCommand,
		InteractionCommand:       interactionCommand,
		InteractionReplayCommand: interactionReplayCommand,
		ProbeCommand:             probeCommand,
		ProbeRunCommand:          probeRunCommand,
		ProbeGateCommand:         probeGateCommand,
		ProbeReportCommand:       probeReportCommand,
		ProbeAcceptanceCommand:   acceptanceCommand,
		ProbeFleetCommand:        probeFleetCommand,
		MediaCommand:             NewMediaCommand(),
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

	probeGroup := NewPath("probe", r.ProbeCommand.Generate())
	probeGroup.AddCommand(NewPath("run [scenario-path...]", r.ProbeRunCommand.Generate()))
	probeGroup.AddCommand(NewPath("gate --out <result.jsonl>...", r.ProbeGateCommand.Generate()))
	probeGroup.AddCommand(NewPath("report --out <result.jsonl>...", r.ProbeReportCommand.Generate()))
	probeGroup.AddCommand(NewPath("acceptance <binary> <goal>", r.ProbeAcceptanceCommand.Generate()))
	probeGroup.AddCommand(NewPath("fleet --manifest <file>", r.ProbeFleetCommand.Generate()))
	root.AddCommand(probeGroup)

	mediaCommand := r.MediaCommand
	if mediaCommand == nil {
		mediaCommand = NewMediaCommand()
	}
	root.AddCommand(NewPath("media", mediaCommand.Generate()))

	sessionGroup := NewPath("session", r.SessionCommand.Generate())
	sessionGroup.AddCommand(NewPath("show <session-id>", r.SessionShowCommand.Generate()))
	sessionGroup.AddCommand(NewPath("list", r.SessionListCommand.Generate()))
	sessionGroup.AddCommand(NewPath("delete <session-id>", r.SessionDeleteCommand.Generate()))
	root.AddCommand(sessionGroup)

	configGroup := NewPath("config", r.ConfigCommand.Generate())
	configGroup.AddCommand(NewPath("add-local", r.ConfigAddLocalCommand.Generate()))
	root.AddCommand(configGroup)

	devicesGroup := NewPath("devices", NewDevicesCommand().Generate())
	registry := r.deviceRegistry
	if registry == nil {
		registry = newDefaultDeviceRegistry()
	}
	devicesGroup.AddCommand(NewPath("list", NewDevicesListCommand(registry).Generate()))
	root.AddCommand(devicesGroup)

	mediaGroup := NewPath("media", NewMediaCommand().Generate())
	mediaGroup.AddCommand(NewPath("probe <url>", NewMediaProbeCommand().Generate()))
	root.AddCommand(mediaGroup)

	cmd := root.CreateCommand()
	cmd.PersistentFlags().CountVarP(&r.Flags.VerboseMode, "verbose", "v", "Enable verbose output (use -v for info, -vv for debug)")
	cmd.PersistentFlags().StringVarP(&r.Flags.ConfigDirPath, "config-dir", "C", r.Flags.ConfigDirPath, "Directory for agent CLI config (default: ~/.agent-cli)")
	cmd.PersistentFlags().BoolVar(&r.Flags.LogToStdout, "log-to-stdout", false, "Log to stdout/stderr instead of file (default: logs to file in config directory)")

	return cmd
}
