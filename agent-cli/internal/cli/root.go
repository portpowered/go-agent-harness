package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
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
func (c *AgentCLI) SetSessionStreamObserver(observer services.SessionStreamObserver) {
	if c == nil || c.router == nil || c.router.SessionCommand == nil {
		return
	}
	c.router.SessionCommand.SetSessionStreamObserver(observer)
}

// RootCommand holds the root "agent" command. Subcommands and persistent flags are wired in routes.go.
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
		Use:   "agent",
		Short: "Port OS Agent CLI - run agentic loops from the command line",
		Long:  "A CLI that runs Port OS agentic loops with configurable LLM providers.",
		// SilenceErrors: cmd/agent's main.go is the single place that prints
		// "Error: %s" for any error Execute() returns. Cobra also prints its
		// own "Error: ..." line for two cases this per-command
		// SilenceErrors can never reach because they happen at root
		// resolution, before any subcommand's RunE runs: an unrecognized
		// top-level command (agent unknown-command) and a flag rejected
		// while still parsing the root command's own flags (agent
		// --unknown-flag). Setting it here, in addition to the individual
		// leaf commands that already set it (ask, probe run, probe fleet,
		// probe report, room run), closes that gap so every error path
		// prints exactly once.
		SilenceErrors: true,
	}
}
