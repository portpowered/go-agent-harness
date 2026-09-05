package cli

import serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

// AgentCLI holds the root command for the agent CLI. Used by wire and tests.
type AgentCLI struct {
	router *Router
}

// NewAgentCLI constructs the CLI app with the given router.
func NewAgentCLI(router *Router) *AgentCLI {
	return &AgentCLI{router: router}
}

// Generate returns the root cobra command (same shape as port CLI for tests).
func (c *AgentCLI) Generate() *cobra.Command {
	return c.router.BuildRoot()
}

// SetSessionStreamObserver installs an optional observer on the composed
// session command before Generate/Execute. The default nil observer preserves
// the existing CLI behavior.
func (c *AgentCLI) SetSessionStreamObserver(observer serviceSession.SessionStreamObserver) {
	if c == nil || c.router == nil || c.router.SessionCommand == nil {
		return
	}
	c.router.SessionCommand.SetSessionStreamObserver(observer)
}

// RootCommand holds the root "yui" command. Subcommands and persistent flags are wired in core_router.go.
type RootCommand struct {
	Flags *flags.GlobalFlags
}

// NewRootCommand constructs a RootCommand with the given dependencies.
func NewRootCommand(flags *flags.GlobalFlags) *RootCommand {
	return &RootCommand{Flags: flags}
}

// Generate returns the root cobra command (subcommands and persistent flags added in Router.BuildRoot).
func (c *RootCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "yui",
		Short: "Run realtime voice agents from the command line",
		Long: "Yui is a cross-platform voice-agent CLI for realtime conversations, local tools, recordings, replay, and structured WebMCP browser control.\n\n" +
			"Set an OpenAI key, then start the default microphone and speaker session:\n\n" +
			"  export OPENAI_API_KEY=\"your-openai-api-key\"\n" +
			"  yui session\n\n" +
			"Run `yui session --help` for audio, model, voice, recording, and browser options.",
		Example: "  # Start a voice session in the current project\n" +
			"  yui --workdir \"$PWD\" session\n\n" +
			"  # Open a page and expose its WebMCP tools\n" +
			"  yui session --browser-tools webmcp --browser-open https://example.com/",
	}
}
