package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	probescenario "github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenario"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/spf13/cobra"
)

// runScenarioV2 executes browser-aware probe.scenario.v2 selections. The
// scenariov2 package owns execution and evidence; this command owns flag
// resolution and the result/summary output streams.
func (c *ProbeRunCommand) runScenarioV2(cmd *cobra.Command, selections []string) (err error) {
	entries, err := scenariov2.LoadSelections(selections, replay.Lookup{})
	if err != nil {
		return err
	}
	browserOptions, err := c.probeScenarioV2BrowserExecutorOptions(cmd)
	if err != nil {
		return err
	}
	resultsOut, summaryOut, closeOutputs, err := c.openProbeOutputs(cmd)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeOutputs(); err == nil {
			err = closeErr
		}
	}()
	recordingRoot, err := scenariov2.PrepareRecordingRoot(c.RecordingRoot, len(entries))
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := scenariov2.Runner{Replay: c.replayService, Analyze: replay.Analyze, Deadline: probescenario.DefaultDeadline}
	emit := func(result scenariov2.Result) error { return writeProbeScenarioV2Result(resultsOut, result) }
	summary, err := runner.Run(ctx, entries, recordingRoot, emit, browserOptions.Options()...)
	if err != nil {
		return err
	}
	return c.writeProbeScenarioV2Summary(cmd, summaryOut, summary)
}

func writeProbeScenarioV2Result(out io.Writer, result scenariov2.Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode result for scenario %q: %w", result.Name, err)
	}
	if _, err := fmt.Fprintf(out, "%s\n", encoded); err != nil {
		return fmt.Errorf("write result for scenario %q: %w", result.Name, err)
	}
	return nil
}

func (c *ProbeRunCommand) writeProbeScenarioV2Summary(cmd *cobra.Command, out io.Writer, summary probe.RunSummary) error {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode v2 probe summary: %w", err)
	}
	if _, err := fmt.Fprintf(out, "%s\n", encoded); err != nil {
		return fmt.Errorf("write v2 probe summary: %w", err)
	}
	return c.reportProbeSummary(cmd, summary)
}
