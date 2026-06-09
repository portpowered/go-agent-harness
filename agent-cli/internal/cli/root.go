package cli

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
	}
}
