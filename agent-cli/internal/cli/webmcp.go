package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

// WebMCPCommand is the protocol-specific direct WebMCP command group.
type WebMCPCommand struct {
	DoctorCommand     *WebMCPDoctorCommand
	OperationsCommand *WebMCPOperationsCommand
}

// NewWebMCPCommand constructs the direct WebMCP group with an optional
// request-scoped runtime factory for hermetic command tests and alternate
// composition roots. The same factory is shared by doctor and direct
// operations so a composition root cannot accidentally give the two surfaces
// different browser ownership semantics.
func NewWebMCPCommand(globalFlags *flags.GlobalFlags, factories ...WebMCPDoctorFactory) *WebMCPCommand {
	factory := defaultWebMCPDoctorFactory(globalFlags)
	if len(factories) > 0 && factories[0] != nil {
		factory = factories[0]
	}
	return &WebMCPCommand{
		DoctorCommand:     NewWebMCPDoctorCommand(globalFlags, factory),
		OperationsCommand: NewWebMCPOperationsCommand(globalFlags, factory),
	}
}

func (c *WebMCPCommand) Generate() *cobra.Command {
	var (
		doctor      *WebMCPDoctorCommand
		operations  *WebMCPOperationsCommand
		globalFlags *flags.GlobalFlags
		factory     WebMCPDoctorFactory
	)
	if c != nil {
		doctor = c.DoctorCommand
		operations = c.OperationsCommand
		if doctor != nil {
			globalFlags = doctor.globalFlags
			factory = doctor.factory
		}
		if operations != nil {
			if globalFlags == nil {
				globalFlags = operations.globalFlags
			}
			if factory == nil {
				factory = operations.factory
			}
		}
	}
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(globalFlags)
	}
	if doctor == nil {
		doctor = NewWebMCPDoctorCommand(globalFlags, factory)
	}
	if operations == nil {
		operations = NewWebMCPOperationsCommand(globalFlags, factory)
	}
	command := &cobra.Command{
		Use:   "webmcp",
		Short: "Inspect WebMCP browser readiness",
		Long:  "Inspect WebMCP browser readiness and operate the CLI-owned browser protocol.",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	command.AddCommand(NewPath("doctor", doctor.Generate()).CreateCommand())
	operations.AddCommands(command)
	return command
}
