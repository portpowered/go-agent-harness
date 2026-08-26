package cli

import (
	"context"
	"encoding/json"
	"fmt"

	acceptanceprobe "github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	loopprobe "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/spf13/cobra"
)

// AcceptanceProbeRunner is the narrow command seam. A replay-backed runner
// can be supplied by CI while the production constructor uses live transport;
// both return the same typed verdict.
type AcceptanceProbeRunner interface {
	Run(context.Context, loopprobe.AcceptanceInput) (loopprobe.AcceptanceVerdict, error)
}

// ProbeAcceptanceCommand runs exactly one blind acceptance probe. Its only
// user inputs are the executable path and plain-English goal; the runner
// provisions the empty working directory.
type ProbeAcceptanceCommand struct {
	Runner AcceptanceProbeRunner
}

// AcceptanceProbeCommand is a compatibility spelling for callers that name
// the command after the full feature rather than its probe-group path.
type AcceptanceProbeCommand = ProbeAcceptanceCommand

// NewProbeAcceptanceCommand constructs the public acceptance command. The
// optional runner is an injection seam for replay and hermetic tests; omitting
// it selects the live process transport.
func NewProbeAcceptanceCommand(runners ...AcceptanceProbeRunner) *ProbeAcceptanceCommand {
	command := &ProbeAcceptanceCommand{Runner: acceptanceprobe.NewLiveRunner(nil)}
	if len(runners) > 0 && runners[0] != nil {
		command.Runner = runners[0]
	}
	return command
}

// NewAcceptanceProbeCommand is an alias for the public constructor.
func NewAcceptanceProbeCommand(runners ...AcceptanceProbeRunner) *ProbeAcceptanceCommand {
	return NewProbeAcceptanceCommand(runners...)
}

// Generate returns the cobra command for one blind acceptance probe.
func (c *ProbeAcceptanceCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "acceptance <binary> <goal>",
		Aliases: []string{"accept"},
		Short:   "Run one blind customer-acceptance probe",
		Long: "Run one blind customer-acceptance probe against an executable. The probe receives " +
			"only the executable and your plain-English goal, and starts in a fresh empty working " +
			"directory. It prints one machine-readable verdict and exits non-zero unless recorded " +
			"artifacts verify the goal and the experience rating is not confusing.",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd, args)
		},
	}
	return cmd
}

func (c *ProbeAcceptanceCommand) run(cmd *cobra.Command, args []string) error {
	if c == nil || c.Runner == nil {
		return fmt.Errorf("acceptance probe runner is not configured")
	}
	input := loopprobe.AcceptanceInput{BinaryPath: args[0], Goal: args[1]}
	verdict, runErr := c.Runner.Run(cmd.Context(), input)
	if verdict.Goal != "" || verdict.ScenarioResult.Name != "" {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(verdict); err != nil {
			return fmt.Errorf("write acceptance probe verdict: %w", err)
		}
	}
	if runErr != nil {
		return runErr
	}
	if !verdict.Pass {
		if verdict.Error != "" {
			return fmt.Errorf("acceptance probe failed: %s", verdict.Error)
		}
		return fmt.Errorf("acceptance probe failed")
	}
	return nil
}
