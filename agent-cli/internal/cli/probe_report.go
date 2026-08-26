package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/spf13/cobra"
)

// ProbeReportCommand aggregates one or more probe runner JSONL artifacts.
type ProbeReportCommand struct {
	Inputs      []string
	JSONPath    string
	SummaryPath string
	NoFail      bool
}

// NewProbeReportCommand returns the probe report command constructor.
func NewProbeReportCommand() *ProbeReportCommand {
	return &ProbeReportCommand{}
}

// Generate returns the cobra command for probe report.
func (c *ProbeReportCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report --out <result.jsonl>...",
		Short: "Aggregate probe result artifacts into a friction report",
		Long: "Aggregate probe result artifacts into a friction report.\n\n" +
			"Read one or more probe runner JSONL artifacts and write a deterministic friction report.\n" +
			"Use --out - to read an artifact from stdin. JSON defaults to stdout and the\n" +
			"human-readable summary defaults to stderr.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.run(cmd)
		},
	}
	cmd.Flags().StringArrayVar(&c.Inputs, "out", nil, "Probe result JSONL file (repeatable; use - for stdin)")
	cmd.Flags().StringVar(&c.JSONPath, "json", "", "Path for the JSON friction report (default stdout; use - for stdout)")
	cmd.Flags().StringVar(&c.SummaryPath, "summary", "", "Path for the human-readable summary (default stderr; use - for stderr)")
	cmd.Flags().BoolVar(&c.NoFail, "no-fail", false, "Always exit zero when the report is healthy enough to render")
	return cmd
}

func (c *ProbeReportCommand) run(cmd *cobra.Command) error {
	if len(c.Inputs) == 0 {
		return fmt.Errorf("at least one --out <result.jsonl> input is required")
	}

	inputs := make([]probe.FrictionReportInput, 0, len(c.Inputs))
	files := make([]*os.File, 0, len(c.Inputs))
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()

	for _, inputPath := range c.Inputs {
		inputPath = strings.TrimSpace(inputPath)
		if inputPath == "" {
			return fmt.Errorf("--out input path must not be empty")
		}
		if inputPath == "-" {
			inputs = append(inputs, probe.FrictionReportInput{Name: "-", Reader: cmd.InOrStdin()})
			continue
		}

		file, err := os.Open(inputPath)
		if err != nil {
			return &probe.FrictionReportError{
				Source: inputPath,
				Err:    fmt.Errorf("open input: %w", err),
			}
		}
		files = append(files, file)
		inputs = append(inputs, probe.FrictionReportInput{Name: inputPath, Reader: file})
	}

	report, err := probe.AggregateFrictionReport(inputs...)
	if err != nil {
		return err
	}

	jsonBytes, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode friction report: %w", err)
	}
	jsonBytes = append(jsonBytes, '\n')
	if err := writeProbeReportOutput(c.JSONPath, "JSON report", cmd.OutOrStdout(), jsonBytes); err != nil {
		return err
	}

	summary := renderFrictionReportSummary(report)
	if err := writeProbeReportOutput(c.SummaryPath, "summary", cmd.ErrOrStderr(), []byte(summary)); err != nil {
		return err
	}

	if report.Failed > 0 && !c.NoFail {
		return fmt.Errorf("probe report contains %d failed scenarios (%d stuck); pass --no-fail to override", report.Failed, report.Stuck)
	}
	return nil
}

func writeProbeReportOutput(path, label string, defaultWriter io.Writer, data []byte) error {
	if path == "" || path == "-" {
		if _, err := defaultWriter.Write(data); err != nil {
			return fmt.Errorf("write %s: %w", label, err)
		}
		return nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s %q: %w", label, path, err)
	}
	return nil
}

func renderFrictionReportSummary(report probe.FrictionReport) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "Probe friction report\n")
	fmt.Fprintf(&summary, "Scenarios: %d total, %d passed, %d failed, %d stuck\n", report.Total, report.Passed, report.Failed, report.Stuck)

	summary.WriteString("Scenario rollups:\n")
	if len(report.Scenarios) == 0 {
		summary.WriteString("  (none)\n")
	} else {
		for _, scenario := range report.Scenarios {
			fmt.Fprintf(&summary, "  %s: total=%d passed=%d failed=%d stuck=%d\n", scenario.Name, scenario.Total, scenario.Passed, scenario.Failed, scenario.Stuck)
		}
	}

	summary.WriteString("Terminal reasons:\n")
	if len(report.TerminalReasons) == 0 {
		summary.WriteString("  (none)\n")
	} else {
		for _, reason := range report.TerminalReasons {
			fmt.Fprintf(&summary, "  %s: %d\n", reason.Reason, reason.Count)
		}
	}

	summary.WriteString("Error classes:\n")
	if len(report.ErrorClasses) == 0 {
		summary.WriteString("  (none)\n")
	} else {
		for _, class := range report.ErrorClasses {
			fmt.Fprintf(&summary, "  %s: %d\n", class.Class, class.Count)
		}
	}

	status := "pass"
	if report.Failed > 0 {
		status = "fail"
	}
	fmt.Fprintf(&summary, "Health: %s\n", status)
	return summary.String()
}
