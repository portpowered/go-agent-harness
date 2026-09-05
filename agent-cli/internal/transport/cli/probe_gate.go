package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/spf13/cobra"
)

// ProbeGateCommand combines one or more recorded probe run result artifacts
// into a single fleet-wide pass/fail verdict with an exit-code contract.
type ProbeGateCommand struct {
	// Artifacts holds every --out value: result file paths, or "-" for stdin.
	Artifacts []string
	// JSONPath optionally receives the exact verdict bytes written to stdout.
	JSONPath string
}

// NewProbeGateCommand returns the probe gate command constructor.
func NewProbeGateCommand() *ProbeGateCommand {
	return &ProbeGateCommand{}
}

// Generate returns the cobra command for probe gate.
func (c *ProbeGateCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate --out <result.jsonl>...",
		Short: "Combine probe run results into one fleet-wide pass/fail verdict",
		Long: "Read one or more probe run result artifacts (--out, repeatable; '-' for standard input)\n" +
			"and combine every scenario across every source into a single deterministic fleet\n" +
			"verdict printed as JSON on stdout. The same bytes are written to --json when given.\n\n" +
			"The exit code is 0 only when every scenario in every source passed without stuck\n" +
			"evidence; any failure, stuck marker, malformed line, unreadable file, or empty\n" +
			"artifact exits non-zero.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd)
		},
	}
	cmd.Flags().StringArrayVar(&c.Artifacts, "out", nil, "Run result artifact path (repeatable; '-' reads standard input)")
	cmd.Flags().StringVar(&c.JSONPath, "json", "", "Path that also receives the verdict JSON bytes")
	return cmd
}

func (c *ProbeGateCommand) run(cmd *cobra.Command) error {
	if len(c.Artifacts) == 0 {
		return fmt.Errorf("fleet gate requires at least one result artifact: pass --out <result.jsonl> (repeatable), or --out - for standard input")
	}
	artifacts := make([]probe.FleetArtifact, 0, len(c.Artifacts))
	var opened []*os.File
	for _, path := range c.Artifacts {
		if path == "-" {
			artifacts = append(artifacts, probe.FleetArtifact{Name: "-", Reader: cmd.InOrStdin()})
			continue
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			c.closeAll(opened)
			return fmt.Errorf("read result artifact: %w", openErr)
		}
		opened = append(opened, file)
		artifacts = append(artifacts, probe.FleetArtifact{Name: path, Reader: file})
	}
	verdict, evalErr := probe.EvaluateFleetGate(artifacts)
	c.closeAll(opened)
	if evalErr != nil {
		return evalErr
	}

	line, marshalErr := json.Marshal(verdict)
	if marshalErr != nil {
		return fmt.Errorf("encode fleet verdict: %w", marshalErr)
	}
	line = append(line, '\n')
	if _, writeErr := cmd.OutOrStdout().Write(line); writeErr != nil {
		return fmt.Errorf("write fleet verdict: %w", writeErr)
	}
	if c.JSONPath != "" {
		if writeErr := os.WriteFile(c.JSONPath, line, 0o644); writeErr != nil {
			return fmt.Errorf("write fleet verdict to --json %q: %w", c.JSONPath, writeErr)
		}
	}
	if verdict.Status != probe.StatusPass {
		notPassing := verdict.Failed + verdict.Stuck
		return fmt.Errorf("fleet gate: %s (%d of %d scenarios not passing across %d sources)",
			verdict.Status, notPassing, verdict.Total, len(verdict.Sources))
	}
	return nil
}

func (c *ProbeGateCommand) closeAll(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}
