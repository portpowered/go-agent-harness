package cli

import "github.com/spf13/cobra"

// Command produces a cobra command for wiring into the router.
type Command interface {
	Generate() *cobra.Command
}

// CommandPath builds a cobra command tree with path-based wiring.
type CommandPath interface {
	CreateCommand() *cobra.Command
	SetPath(path string)
	AddCommand(command CommandPath)
}

// BasePath implements CommandPath for routing subcommands.
type BasePath struct {
	command  *cobra.Command
	children []CommandPath
}

var _ CommandPath = &BasePath{}

// NewPath returns a CommandPath that wraps the given command with the given path (Use).
func NewPath(path string, command *cobra.Command) CommandPath {
	p := &BasePath{command: command}
	p.SetPath(path)
	return p
}

func (c *BasePath) SetPath(path string) {
	c.command.Use = path
}

func (c *BasePath) CreateCommand() *cobra.Command {
	for _, child := range c.children {
		c.command.AddCommand(child.CreateCommand())
	}
	return c.command
}

func (c *BasePath) AddCommand(command CommandPath) {
	c.children = append(c.children, command)
}
