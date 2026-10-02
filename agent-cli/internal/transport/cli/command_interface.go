package cli

import (
	"bytes"
	"fmt"
	"io"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/spf13/cobra"
)

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

// requireFlags marks registered flags as required. Cobra only rejects a flag
// name that was never registered, which is a defect in the command
// definition, so it panics instead of silently shipping an optional flag.
func requireFlags(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("mark flag %q required: %v", name, err)) //nolint:forbidigo // must-style: names are constants of the command definition; an unregistered name is a programming defect
		}
	}
}

// writeAdvisory writes an advisory diagnostic line. Diagnostic sinks and
// reachability hints have no error channel back to the command, and a failed
// advisory write must not change the command outcome, so it is dropped.
func writeAdvisory(out io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		return
	}
}

// discardCloseError marks a close error as deliberately ignored on a path
// whose outcome is already decided (a read-only probe whose status was read,
// or an abandoned connection); a close failure cannot change that outcome.
// Callers pass the Close call inline so the close stays visible at the site.
func discardCloseError(error) {}

// writeToolDiagnostic writes one tool diagnostic as a single advisory line.
// A recovered panic's stack follows that line as its own block, so the line
// itself stays one line for log readers.
func writeToolDiagnostic(out io.Writer, diagnostic serviceSession.SessionToolDiagnostic) {
	writeAdvisory(out, "tool diagnostic: tool=%q call_id=%q source=%q error_code=%q detail=%s\n", diagnostic.ToolName, diagnostic.ToolCallID, diagnostic.Source, diagnostic.ErrorCode, diagnostic.Error)
	if len(diagnostic.Stack) > 0 {
		writeAdvisory(out, "tool diagnostic stack: call_id=%q\n%s\n", diagnostic.ToolCallID, bytes.TrimRight(diagnostic.Stack, "\n"))
	}
}
