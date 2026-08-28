package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

// WebMCPCommand is the protocol-specific direct WebMCP command group. The
// remaining operation commands are added by the direct-operations story;
// doctor is available independently because it has its own diagnostic seam.
type WebMCPCommand struct {
	DoctorCommand *WebMCPDoctorCommand
}

// NewWebMCPCommand constructs the direct WebMCP group with an optional doctor
// factory for hermetic command tests and alternate composition roots.
func NewWebMCPCommand(globalFlags *flags.GlobalFlags, factories ...WebMCPDoctorFactory) *WebMCPCommand {
	return &WebMCPCommand{DoctorCommand: NewWebMCPDoctorCommand(globalFlags, factories...)}
}

func (c *WebMCPCommand) Generate() *cobra.Command {
	doctor := c.DoctorCommand
	if doctor == nil {
		doctor = NewWebMCPDoctorCommand(nil)
	}
	command := &cobra.Command{
		Use:   "webmcp",
		Short: "Inspect WebMCP browser readiness",
		Long:  "Inspect WebMCP browser readiness and operate the CLI-owned browser protocol.",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	command.AddCommand(NewPath("doctor", doctor.Generate()).CreateCommand())
	return command
}
